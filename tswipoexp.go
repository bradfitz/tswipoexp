// The tswipoexp command is a portable Windows Tailscale client: a
// userspace tailscaled with a GUI that keeps its state next to the
// binary and exposes the tailnet to other programs through a local
// SOCKS5 and HTTP proxy.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"tailscale.com/hostinfo"
	"tailscale.com/ipn"
)

// version is set by the release build (-X main.version=...).
var version = "dev"

var (
	debugAddr = flag.String("debug-addr", "", "if set, a loopback host:port on which to serve the debug and automation HTTP endpoint (off by default)")
	stateDir  = flag.String("state-dir", "", "state directory; defaults to tswipoexp-state next to the executable")
	profile   = flag.String("profile", "", "profile to load; defaults to the one named in active-profile.txt")
)

// App ties the GUI, the profiles, and the running backend together.
type App struct {
	fy       fyne.App
	ui       *UI
	profiles *Profiles
	bridge   *localAPIBridge
	logs     *logSink

	mu       sync.Mutex
	backend  *Backend
	profName string
}

func main() {
	flag.Parse()
	// Identify this client to control (shows up in the admin console
	// and in tailscale status as the app).
	hostinfo.SetApp("tswipoexp")
	logs := newLogSink()
	log.SetOutput(logs)
	log.SetFlags(0)
	logf := logs.Logf
	sshDebugf = logf

	root := *stateDir
	if root == "" {
		var err error
		root, err = exeStateDir()
		if err != nil {
			fatal(logf, "finding state directory: %v", err)
		}
	}
	profiles := &Profiles{Root: root}
	if _, err := profiles.List(); err != nil {
		fatal(logf, "state directory %s: %v", root, err)
	}
	redirectStderr(root)

	bridge, err := newLocalAPIBridge(root, logf)
	if err != nil {
		if errors.Is(err, ErrAlreadyRunning) {
			fatal(logf, "%v", err)
		}
		fatal(logf, "LocalAPI socket: %v", err)
	}
	defer bridge.Close()

	// If a previous run died with the proxy registered (crash, or
	// the USB stick was pulled), put the user's settings back
	// before doing anything else.
	if err := unregisterSystemProxy(logf); err != nil {
		logf("restoring proxy settings from a previous run: %v", err)
	}

	a := &App{
		fy:       app.NewWithID("com.github.bradfitz.tswipoexp"),
		profiles: profiles,
		bridge:   bridge,
		logs:     logs,
	}
	a.ui = newUI(a)

	name := *profile
	if name == "" {
		name, err = profiles.Active()
		if err != nil {
			fatal(logf, "active profile: %v", err)
		}
	}
	if err := a.startBackend(name); err != nil {
		logf("starting profile %q: %v", name, err)
		a.ui.showErr(err)
	}

	if *debugAddr != "" {
		if err := a.serveDebug(*debugAddr); err != nil {
			fatal(logf, "debug endpoint: %v", err)
		}
	}

	a.ui.refresh()
	// The canvas scale is only known once the window exists, so fit
	// the window to the screen shortly after it appears.
	time.AfterFunc(300*time.Millisecond, func() { fyne.Do(a.ui.fitToScreen) })
	time.AfterFunc(1500*time.Millisecond, func() { fyne.Do(a.ui.fitToScreen) })
	a.ui.win.ShowAndRun()
	a.stopBackend()
}

// fatal logs the message, shows it to the user (the GUI binary has
// no console on Windows), and exits.
func fatal(logf func(string, ...any), format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	logf("%s", msg)
	fmt.Fprintln(os.Stderr, msg)
	showFatal(msg)
	os.Exit(1)
}

func (a *App) logf(format string, args ...any) { a.logs.Logf(format, args...) }

// currentProfile returns the name of the loaded profile, or "".
func (a *App) currentProfile() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.profName
}

// startBackend loads and starts the named profile. Any previously
// running backend must already be stopped.
func (a *App) startBackend(name string) error {
	cfg, err := a.profiles.LoadConfig(name)
	if err != nil {
		return err
	}
	dir := a.profiles.Dir(name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := a.logs.SetFile(dir); err != nil {
		a.logf("opening log file: %v", err)
	}
	if err := a.profiles.SetActive(name); err != nil {
		a.logf("recording active profile: %v", err)
	}
	a.logf("starting profile %q in %s", name, dir)

	b := NewBackend(name, dir, cfg, a.logf)
	b.OnChange = a.ui.scheduleRefresh
	b.OnSummary = func(s ProfileSummary) {
		if err := a.profiles.SaveSummary(name, s); err != nil {
			a.logf("saving profile summary: %v", err)
		}
	}
	a.mu.Lock()
	a.backend = b
	a.profName = name
	a.mu.Unlock()

	if err := b.Start(""); err != nil {
		a.mu.Lock()
		a.backend = nil
		a.mu.Unlock()
		b.Close()
		return err
	}
	if err := a.bridge.SetTarget(b); err != nil {
		a.logf("LocalAPI bridge: %v", err)
	}
	a.ui.scheduleRefresh()
	return nil
}

// stopBackend shuts down the running backend, if any.
func (a *App) stopBackend() {
	a.mu.Lock()
	b := a.backend
	a.backend = nil
	a.mu.Unlock()
	if b == nil {
		return
	}
	a.bridge.SetTarget(nil)
	a.logf("stopping profile %q", b.Profile())
	if err := b.Close(); err != nil {
		a.logf("closing backend: %v", err)
	}
}

// switchProfile stops the current profile and starts another. It
// runs on the UI goroutine but does the work in the background.
func (a *App) switchProfile(name string) {
	_, file, line, _ := runtime.Caller(1)
	a.logf("switchProfile(%q) from %s:%d (current %q)", name, filepath.Base(file), line, a.currentProfile())
	a.ui.setState("Switching to profile "+name, stateColorGray)
	go func() {
		a.stopBackend()
		err := a.startBackend(name)
		fyne.Do(func() {
			if err != nil {
				a.ui.showErr(err)
			}
			a.ui.refresh()
		})
	}()
}

// setOutboundEnabled turns registration as the system proxy on or
// off for the current profile.
func (a *App) setOutboundEnabled(on bool) {
	a.mu.Lock()
	b := a.backend
	a.mu.Unlock()
	if b == nil || b.Config().registerProxy() == on {
		return // programmatic checkbox update
	}
	a.applyOutbound(func(c *Config) { c.RegisterProxy = &on }, false)
}

// applyOutbound applies a change to the current profile's outbound
// access settings: mutate edits the live config in place (so fields
// the caller doesn't own, such as SSH, are never clobbered by a stale
// copy), the result is saved, and the registration is redone. If the
// proxy address changed the profile is restarted, since the listener
// can't move while running.
func (a *App) applyOutbound(mutate func(*Config), addrChanged bool) {
	a.mu.Lock()
	b := a.backend
	a.mu.Unlock()
	if b == nil {
		return
	}
	mutate(b.Config())
	if err := a.profiles.SaveConfig(b.Profile(), b.Config()); err != nil {
		a.ui.showErr(err)
		return
	}
	if addrChanged {
		a.switchProfile(b.Profile())
		return
	}
	go func() {
		if err := b.ApplyOutbound(); err != nil {
			a.ui.showErrAsync(err)
		}
		a.ui.scheduleRefresh()
	}()
}

// renameProfile renames a profile, restarting it if it's the one
// running. It runs on the UI goroutine and does the work in the
// background.
func (a *App) renameProfile(from, to string, done func(error)) {
	go func() {
		running := a.currentProfile() == from
		if running {
			a.stopBackend()
		}
		err := a.profiles.Rename(from, to)
		if running {
			name := to
			if err != nil {
				name = from
			}
			if serr := a.startBackend(name); serr != nil && err == nil {
				err = serr
			}
		}
		fyne.Do(func() {
			a.ui.refresh()
			done(err)
		})
	}()
}

// deleteProfile removes a profile that isn't running.
func (a *App) deleteProfile(name string) error {
	if a.currentProfile() == name {
		return fmt.Errorf("switch to another profile before deleting %q", name)
	}
	return a.profiles.Delete(name)
}

// setSSHMode changes the SSH mode for the current profile and saves
// the choice.
func (a *App) setSSHMode(mode string) {
	a.mu.Lock()
	b := a.backend
	a.mu.Unlock()
	if b == nil || b.Config().sshMode() == mode {
		return
	}
	if err := b.SetSSHMode(mode); err != nil {
		a.ui.showErr(err)
	}
	if err := a.profiles.SaveConfig(b.Profile(), b.Config()); err != nil {
		a.ui.showErr(err)
	}
	a.ui.scheduleRefresh()
}

// setHostname updates the profile's hostname setting and applies it
// to the running node.
func (a *App) setHostname(name string, sync bool) {
	a.mu.Lock()
	b := a.backend
	a.mu.Unlock()
	if b == nil {
		return
	}
	cfg := b.Config()
	cfg.SyncHostname = sync
	if !sync && name != "" {
		cfg.Hostname = name
	}
	if err := a.profiles.SaveConfig(b.Profile(), cfg); err != nil {
		a.ui.showErr(err)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := b.SetHostname(ctx, cfg.hostname()); err != nil {
			a.ui.showErrAsync(err)
		}
	}()
}

// prefs returns the running node's preferences, or nil.
func (a *App) prefs() *ipn.Prefs {
	a.mu.Lock()
	b := a.backend
	a.mu.Unlock()
	if b == nil || b.LocalClient() == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	p, err := b.Prefs(ctx)
	if err != nil {
		return nil
	}
	return p
}

// quit shuts down cleanly. Proxy registration and the like are undone
// by stopBackend.
func (a *App) quit() {
	a.logf("quitting")
	a.ui.saveWindowSize()
	go func() {
		a.stopBackend()
		fyne.Do(a.fy.Quit)
	}()
}
