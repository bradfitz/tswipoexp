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
	"sync"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/net/proxymux"
	"tailscale.com/net/socks5"
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

	ts *tsnet.Server
	lc *local.Client

	proxyLn net.Listener

	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	status   *ipnstate.Status
	authURL  string // most recent BrowseToURL from the IPN bus
	lastErr  string // most recent error worth showing the user
	closed   bool
	closeErr error
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
		Logf:     logger.WithPrefix(b.logf, "tsnet: "),
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

	if err := b.startProxy(); err != nil {
		b.setErr("proxy: %v", err)
	}
	return nil
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
	if b.proxyLn != nil {
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
	b.mu.Unlock()
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
		Handler:  httpProxyHandler(b.dial),
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

// dial is the proxy's dialer onto the tailnet. Unlike tsnet's Dial it
// doesn't block waiting for the node to reach Running, since a
// browser hanging forever is worse than a fast error.
func (b *Backend) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	st := b.Status()
	if st == nil || st.BackendState != ipn.Running.String() {
		state := "unknown"
		if st != nil {
			state = st.BackendState
		}
		return nil, fmt.Errorf("tswipoexp is not connected to the tailnet (state %s)", state)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
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
