package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/net/proxymux"
	"tailscale.com/net/socks5"
	"tailscale.com/tailcfg"
	"tailscale.com/tsnet"
	"tailscale.com/types/logger"
)

// Backend runs one profile's tsnet node along with the proxy and
// LocalAPI bridge that go with it. A Backend is used for one profile
// and then closed; switching profiles makes a new one.
type Backend struct {
	profile string
	dir     string
	cfg     *Config
	logf    logger.Logf

	// OnChange, if set, is called from a background goroutine
	// whenever the node's state may have changed. The callback
	// must not block for long.
	OnChange func()

	// OnSummary, if set, is called with a fresh summary whenever the
	// last known details worth remembering change.
	OnSummary func(ProfileSummary)

	ts *tsnet.Server
	lc *local.Client

	proxyLn net.Listener
	sshSrv  *sshServer // nil unless SSH is on

	ctx    context.Context
	cancel context.CancelFunc

	mu          sync.Mutex
	lastPAC     string // fingerprint of the last PAC inputs, to detect changes
	lastSummary string // fingerprint of the last saved ProfileSummary
	status      *ipnstate.Status
	authURL     string // most recent BrowseToURL from the IPN bus
	lastErr     string // most recent error worth showing the user
	closed      bool
	closeErr    error
}

// NewBackend prepares, but does not start, a backend for the profile
// in dir.
func NewBackend(profile, dir string, cfg *Config, logf logger.Logf) *Backend {
	ctx, cancel := context.WithCancel(context.Background())
	return &Backend{
		profile: profile,
		dir:     dir,
		cfg:     cfg,
		logf:    logger.WithPrefix(logf, "["+profile+"] "),
		ctx:     ctx,
		cancel:  cancel,
	}
}

// Start brings up tsnet, the proxy, and the LocalAPI bridge.
// If authKey is non-empty, it's used for the initial login.
func (b *Backend) Start(authKey string) error {
	b.ts = &tsnet.Server{
		Dir:      b.dir,
		Hostname: b.cfg.hostname(),
		AuthKey:  authKey,
		Logf:     b.tsnetLogf,
		UserLogf: b.logf,
	}
	if err := b.ts.Start(); err != nil {
		return fmt.Errorf("starting tsnet: %w", err)
	}
	lc, err := b.ts.LocalClient()
	if err != nil {
		return err
	}
	b.lc = lc

	go b.watchIPNBus()
	go b.pollStatus()

	if b.cfg.sshMode() != sshOff {
		if err := b.startSSH(); err != nil {
			b.setErr("ssh: %v", err)
		}
	}
	if err := b.startProxy(); err != nil {
		b.setErr("proxy: %v", err)
	} else if b.cfg.registerProxy() {
		if err := registerSystemProxy(b.cfg.outboundOptions(b.ProxyAddr()), b.logf); err != nil {
			b.setErr("registering proxy with Windows: %v", err)
		}
	}
	return nil
}

// ApplyOutbound applies changed outbound access settings from the
// config: it restores the user's original settings and registers
// again with the new options, or just restores if registration is
// now off. The caller has already updated b.cfg.
func (b *Backend) ApplyOutbound() error {
	if b.ProxyAddr() == "" {
		return nil
	}
	if err := unregisterSystemProxy(b.logf); err != nil {
		return err
	}
	if !b.cfg.registerProxy() {
		return nil
	}
	return registerSystemProxy(b.cfg.outboundOptions(b.ProxyAddr()), b.logf)
}

// startSSH listens for SSH on the node's tailnet addresses.
func (b *Backend) startSSH() error {
	ln, err := b.ts.Listen("tcp", ":22")
	if err != nil {
		return err
	}
	s := &sshServer{
		logf: b.logf,
		lc:   b.lc,
		dir:  b.dir,
		ln:   ln,
		mode: func() string { return b.cfg.sshMode() },
		selfID: func() (string, bool) {
			st := b.Status()
			if st == nil || st.Self == nil {
				return "", false
			}
			up, ok := st.User[st.Self.UserID]
			if !ok {
				return "", false
			}
			return up.LoginName, true
		},
	}
	b.mu.Lock()
	b.sshSrv = s
	b.mu.Unlock()
	go s.serve()
	b.logf("ssh: listening on the tailnet, port 22")
	return nil
}

func (b *Backend) stopSSH() {
	b.mu.Lock()
	s := b.sshSrv
	b.sshSrv = nil
	b.mu.Unlock()
	if s != nil {
		s.close()
	}
}

// SetSSHMode changes the SSH mode and starts or stops the server to
// match. The mode is read live by a running server, so switching
// between the two "on" modes needs no restart.
func (b *Backend) SetSSHMode(mode string) error {
	b.cfg.SSHMode = mode
	b.cfg.SSH = false
	if mode == sshOff {
		b.stopSSH()
		return nil
	}
	b.mu.Lock()
	running := b.sshSrv != nil
	b.mu.Unlock()
	if running {
		return nil
	}
	return b.startSSH()
}

// SSHRunning reports whether the SSH server is listening.
func (b *Backend) SSHRunning() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sshSrv != nil
}

// ProxyRegistered reports whether the user's Windows proxy settings
// currently point at our proxy.
func (b *Backend) ProxyRegistered() bool {
	return b.ProxyAddr() != "" && systemProxyIsOurs(b.ProxyAddr())
}

// tsnetLogf is tsnet's logger. It drops tsnet's every-five-seconds
// reminder about TS_AUTHKEY, which is meant for headless programs;
// the GUI shows the login URL itself.
func (b *Backend) tsnetLogf(format string, args ...any) {
	if strings.HasPrefix(format, "To start this tsnet server") {
		return
	}
	b.logf("tsnet: "+format, args...)
}

// Close shuts everything down. It's safe to call more than once.
func (b *Backend) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return b.closeErr
	}
	b.closed = true
	b.mu.Unlock()

	b.cancel()
	b.stopSSH()
	if b.proxyLn != nil {
		if err := unregisterSystemProxy(b.logf); err != nil {
			b.logf("unregistering proxy: %v", err)
		}
		b.proxyLn.Close()
	}
	if b.ts != nil {
		b.closeErr = b.ts.Close()
	}
	return b.closeErr
}

// Profile returns the profile name this backend serves.
func (b *Backend) Profile() string { return b.profile }

// Config returns the profile's config.
func (b *Backend) Config() *Config { return b.cfg }

// LocalClient returns the in-process LocalAPI client.
func (b *Backend) LocalClient() *local.Client { return b.lc }

// Status returns the most recent status snapshot, which may be nil
// before the first one arrives.
func (b *Backend) Status() *ipnstate.Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.status
}

// AuthURL returns the most recent login URL from control, or "".
func (b *Backend) AuthURL() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.authURL
}

// LastErr returns the most recent error worth showing the user, or "".
func (b *Backend) LastErr() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastErr
}

// ProxyAddr returns the proxy's listening address, or "" if the proxy
// failed to start.
func (b *Backend) ProxyAddr() string {
	if b.proxyLn == nil {
		return ""
	}
	return b.proxyLn.Addr().String()
}

func (b *Backend) setErr(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	b.logf("%s", msg)
	b.mu.Lock()
	b.lastErr = msg
	b.mu.Unlock()
	b.changed()
}

// ClearErr forgets the last error.
func (b *Backend) ClearErr() {
	b.mu.Lock()
	b.lastErr = ""
	b.mu.Unlock()
	b.changed()
}

func (b *Backend) changed() {
	if b.OnChange != nil {
		b.OnChange()
	}
}

// watchIPNBus follows the backend's notifications and refreshes the
// status snapshot whenever something relevant changes.
func (b *Backend) watchIPNBus() {
	for b.ctx.Err() == nil {
		err := b.watchIPNBusOnce()
		if b.ctx.Err() != nil {
			return
		}
		b.logf("IPN bus watcher: %v; retrying", err)
		select {
		case <-time.After(time.Second):
		case <-b.ctx.Done():
			return
		}
	}
}

func (b *Backend) watchIPNBusOnce() error {
	w, err := b.lc.WatchIPNBus(b.ctx, ipn.NotifyInitialState|ipn.NotifyInitialPrefs|ipn.NotifyInitialNetMap|ipn.NotifyNoPrivateKeys)
	if err != nil {
		return err
	}
	defer w.Close()
	b.refreshStatus()
	for {
		n, err := w.Next()
		if err != nil {
			return err
		}
		if n.BrowseToURL != nil {
			b.mu.Lock()
			b.authURL = *n.BrowseToURL
			b.mu.Unlock()
		}
		if n.LoginFinished != nil {
			b.mu.Lock()
			b.authURL = ""
			b.mu.Unlock()
		}
		if n.ErrMessage != nil {
			b.setErr("%s", *n.ErrMessage)
		}
		b.refreshStatus()
	}
}

// pollStatus refreshes the status periodically, since connection
// paths (relay versus direct) and traffic change without any IPN bus
// notification.
func (b *Backend) pollStatus() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			b.refreshStatus()
		case <-b.ctx.Done():
			return
		}
	}
}

// refreshStatus fetches a new status snapshot and notifies the UI.
func (b *Backend) refreshStatus() {
	ctx, cancel := context.WithTimeout(b.ctx, 5*time.Second)
	defer cancel()
	st, err := b.lc.Status(ctx)
	if err != nil {
		if b.ctx.Err() == nil {
			b.logf("Status: %v", err)
		}
		return
	}
	b.mu.Lock()
	b.status = st
	if st.BackendState == ipn.Running.String() {
		b.authURL = ""
	}
	summary := summarize(st)
	summaryChanged := b.lastSummary != summary.key()
	if summaryChanged {
		b.lastSummary = summary.key()
	}
	pacChanged := false
	if b.proxyLn != nil {
		in := pacInputsFrom(st, b.cfg, b.ProxyAddr())
		key := fmt.Sprintf("%v", in)
		pacChanged = b.lastPAC != "" && key != b.lastPAC
		b.lastPAC = key
	}
	b.mu.Unlock()
	if summaryChanged && b.OnSummary != nil {
		summary.UpdatedAt = time.Now()
		b.OnSummary(summary)
	}
	if pacChanged && b.cfg.registerProxy() && b.cfg.proxyMode() == proxyModePAC {
		// Browsers cache the script; tell WinINet settings changed
		// so they fetch it again.
		notifyProxySettingsChanged()
	}
	b.changed()
}

// LoginInteractive asks control for a login URL. The URL arrives via
// the IPN bus and is then available from AuthURL.
func (b *Backend) LoginInteractive(ctx context.Context) error {
	return b.lc.StartLoginInteractive(ctx)
}

// LoginWithAuthKey logs in using a pre-authorized key.
func (b *Backend) LoginWithAuthKey(ctx context.Context, authKey string) error {
	prefs, err := b.lc.GetPrefs(ctx)
	if err != nil {
		return err
	}
	prefs.WantRunning = true
	prefs.Hostname = b.cfg.hostname()
	if err := b.lc.Start(ctx, ipn.Options{AuthKey: authKey, UpdatePrefs: prefs}); err != nil {
		return err
	}
	return b.lc.StartLoginInteractive(ctx)
}

// Logout logs out of the tailnet, discarding the node key.
func (b *Backend) Logout(ctx context.Context) error {
	return b.lc.Logout(ctx)
}

// SetWantRunning connects or disconnects without logging out.
func (b *Backend) SetWantRunning(ctx context.Context, want bool) error {
	_, err := b.lc.EditPrefs(ctx, &ipn.MaskedPrefs{
		Prefs:          ipn.Prefs{WantRunning: want},
		WantRunningSet: true,
	})
	return err
}

// SetShieldsUp blocks or unblocks all incoming connections.
func (b *Backend) SetShieldsUp(ctx context.Context, up bool) error {
	_, err := b.lc.EditPrefs(ctx, &ipn.MaskedPrefs{
		Prefs:        ipn.Prefs{ShieldsUp: up},
		ShieldsUpSet: true,
	})
	return err
}

// SetExitNode routes all non-tailnet traffic through the given peer,
// or clears the exit node if id is empty.
func (b *Backend) SetExitNode(ctx context.Context, id tailcfg.StableNodeID) error {
	_, err := b.lc.EditPrefs(ctx, &ipn.MaskedPrefs{
		Prefs:         ipn.Prefs{ExitNodeID: id},
		ExitNodeIDSet: true,
	})
	return err
}

// SetHostname changes the node's hostname on the tailnet.
func (b *Backend) SetHostname(ctx context.Context, name string) error {
	_, err := b.lc.EditPrefs(ctx, &ipn.MaskedPrefs{
		Prefs:       ipn.Prefs{Hostname: name},
		HostnameSet: true,
	})
	return err
}

// Prefs returns the current preferences.
func (b *Backend) Prefs(ctx context.Context) (*ipn.Prefs, error) {
	return b.lc.GetPrefs(ctx)
}

// startProxy starts the combined SOCKS5 and HTTP proxy on the
// profile's configured loopback address.
func (b *Backend) startProxy() error {
	ln, err := net.Listen("tcp", b.cfg.proxyAddr())
	if err != nil {
		return err
	}
	b.proxyLn = ln
	socksLn, httpLn := proxymux.SplitSOCKSAndHTTP(ln)

	ss := &socks5.Server{
		Logf:   logger.WithPrefix(b.logf, "socks5: "),
		Dialer: b.dial,
	}
	go func() {
		err := ss.Serve(socksLn)
		if b.ctx.Err() == nil {
			b.setErr("SOCKS5 server exited: %v", err)
		}
	}()
	hs := &http.Server{
		Handler:  httpProxyHandler(b.dial, b.pac),
		ErrorLog: log.New(logger.FuncWriter(logger.WithPrefix(b.logf, "httpproxy: ")), "", 0),
	}
	go func() {
		err := hs.Serve(httpLn)
		if b.ctx.Err() == nil && !errors.Is(err, http.ErrServerClosed) {
			b.setErr("HTTP proxy exited: %v", err)
		}
	}()
	b.logf("proxy listening on %v (SOCKS5 and HTTP)", ln.Addr())
	return nil
}

// pac returns the current proxy auto-config script.
func (b *Backend) pac() string {
	return pacScript(pacInputsFrom(b.Status(), b.cfg, b.ProxyAddr()))
}

// dial is the proxy's dialer. While the node is Running everything
// goes through tsnet, which handles tailnet destinations itself and
// hands the rest to the OS (or the exit node, if one is set). When
// the node isn't Running, the proxy is likely still registered as the
// system proxy, so rather than break all browsing it dials directly;
// tailnet names then fail with an ordinary DNS error.
func (b *Backend) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	st := b.Status()
	if st == nil || st.BackendState != ipn.Running.String() {
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	return b.ts.Dial(ctx, network, addr)
}

// logFilePath returns the log file for a profile directory.
func logFilePath(dir string) string {
	return filepath.Join(dir, "tswipoexp.log")
}

// openLogFile opens (creating or appending) a profile's log file.
func openLogFile(dir string) (*os.File, error) {
	return os.OpenFile(logFilePath(dir), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

// summarize extracts the profile summary from a status.
func summarize(st *ipnstate.Status) ProfileSummary {
	s := ProfileSummary{State: st.BackendState}
	if st.CurrentTailnet != nil {
		s.Tailnet = st.CurrentTailnet.Name
	}
	if st.Self != nil {
		if up, ok := st.User[st.Self.UserID]; ok {
			s.Account = up.LoginName
		}
		s.Hostname = st.Self.HostName
		s.DNSName = strings.TrimSuffix(st.Self.DNSName, ".")
	}
	for _, ip := range st.TailscaleIPs {
		s.IPs = append(s.IPs, ip.String())
	}
	return s
}

// key is a comparison fingerprint that ignores UpdatedAt.
func (s ProfileSummary) key() string {
	return fmt.Sprintf("%s|%s|%s|%s|%s|%v", s.State, s.Tailnet, s.Account, s.Hostname, s.DNSName, s.IPs)
}
