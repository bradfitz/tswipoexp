package main

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
)

// UI is the main window and its widgets.
type UI struct {
	app *App
	win fyne.Window

	// Widgets are registered by name so the debug endpoint can
	// find them. Everything the user can act on has a name.
	mu      sync.Mutex
	widgets map[string]fyne.CanvasObject

	profileSel  *widget.Select
	newProfile  *widget.Entry
	stateLabel  *widget.Label
	ipsLabel    *widget.Label
	userLabel   *widget.Label
	hostLabel   *widget.Label
	proxyLabel  *widget.Label
	errLabel    *widget.Label
	loginBtn    *widget.Button
	authKey     *widget.Entry
	authKeyBtn  *widget.Button
	logoutBtn   *widget.Button
	connectBtn  *widget.Button
	shieldsUp   *widget.Check
	hostEntry   *widget.Entry
	hostBtn     *widget.Button
	hostSync    *widget.Check
	peersTable  *widget.Table
	peersHeader []string

	peers []peerRow // rows currently shown in the table

	refreshTimer *time.Timer
}

// peerRow is one row of the peer table.
type peerRow struct {
	Name, IP, OS, Online, Path string
}

func newUI(a *App) *UI {
	u := &UI{
		app:         a,
		win:         a.fy.NewWindow("tswipoexp"),
		widgets:     map[string]fyne.CanvasObject{},
		peersHeader: []string{"Name", "IP", "OS", "Online", "Path"},
	}
	u.build()
	return u
}

// reg registers a widget under a name for the debug endpoint and
// returns it.
func (u *UI) reg(name string, w fyne.CanvasObject) fyne.CanvasObject {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.widgets[name] = w
	return w
}

func (u *UI) build() {
	a := u.app

	u.profileSel = widget.NewSelect(nil, func(name string) {
		if name != "" && a.currentProfile() != name {
			a.switchProfile(name)
		}
	})
	u.reg("profile", u.profileSel)
	u.newProfile = widget.NewEntry()
	u.newProfile.SetPlaceHolder("New profile name")
	u.reg("newProfileName", u.newProfile)
	newBtn := widget.NewButton("Create", func() {
		name := strings.TrimSpace(u.newProfile.Text)
		if name == "" {
			return
		}
		if err := a.profiles.Create(name); err != nil {
			u.showErr(err)
			return
		}
		u.newProfile.SetText("")
		a.switchProfile(name)
	})
	u.reg("createProfile", newBtn)

	u.stateLabel = widget.NewLabel("")
	u.reg("state", u.stateLabel)
	u.ipsLabel = widget.NewLabel("")
	u.reg("ips", u.ipsLabel)
	u.userLabel = widget.NewLabel("")
	u.reg("user", u.userLabel)
	u.hostLabel = widget.NewLabel("")
	u.reg("hostname", u.hostLabel)
	u.proxyLabel = widget.NewLabel("")
	u.reg("proxy", u.proxyLabel)
	u.errLabel = widget.NewLabel("")
	u.errLabel.Wrapping = fyne.TextWrapWord
	u.errLabel.Importance = widget.DangerImportance
	u.reg("error", u.errLabel)

	u.loginBtn = widget.NewButton("Log in with browser", func() {
		b := a.backend
		if b == nil {
			return
		}
		if url := b.AuthURL(); url != "" {
			u.openURL(url)
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := b.LoginInteractive(ctx); err != nil {
				u.showErrAsync(err)
			}
		}()
	})
	u.loginBtn.Importance = widget.HighImportance
	u.reg("login", u.loginBtn)

	u.authKey = widget.NewPasswordEntry()
	u.authKey.SetPlaceHolder("tskey-auth-...")
	u.reg("authKey", u.authKey)
	u.authKeyBtn = widget.NewButton("Log in with auth key", func() {
		b := a.backend
		key := strings.TrimSpace(u.authKey.Text)
		if b == nil || key == "" {
			return
		}
		u.authKey.SetText("")
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := b.LoginWithAuthKey(ctx, key); err != nil {
				u.showErrAsync(err)
			}
		}()
	})
	u.reg("loginAuthKey", u.authKeyBtn)

	u.logoutBtn = widget.NewButton("Log out", func() {
		b := a.backend
		if b == nil {
			return
		}
		dialog.ShowConfirm("Log out", "Log out of the tailnet? This node will need to be re-authorized to reconnect.", func(ok bool) {
			if !ok {
				return
			}
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if err := b.Logout(ctx); err != nil {
					u.showErrAsync(err)
				}
			}()
		}, u.win)
	})
	u.reg("logout", u.logoutBtn)

	u.connectBtn = widget.NewButton("Disconnect", func() {
		b := a.backend
		if b == nil {
			return
		}
		st := b.Status()
		want := st == nil || st.BackendState != ipn.Running.String()
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := b.SetWantRunning(ctx, want); err != nil {
				u.showErrAsync(err)
			}
		}()
	})
	u.reg("connect", u.connectBtn)

	u.shieldsUp = widget.NewCheck("Shields up (block incoming connections)", func(on bool) {
		b := a.backend
		if b == nil {
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := b.SetShieldsUp(ctx, on); err != nil {
				u.showErrAsync(err)
			}
		}()
	})
	u.reg("shieldsUp", u.shieldsUp)

	u.hostEntry = widget.NewEntry()
	u.hostEntry.SetPlaceHolder(defaultHostname)
	u.reg("hostnameEntry", u.hostEntry)
	u.hostBtn = widget.NewButton("Set hostname", func() {
		a.setHostname(strings.TrimSpace(u.hostEntry.Text), false)
	})
	u.reg("setHostname", u.hostBtn)
	u.hostSync = widget.NewCheck("Use this computer's name", func(on bool) {
		if on {
			a.setHostname("", true)
		} else {
			a.setHostname(strings.TrimSpace(u.hostEntry.Text), false)
		}
	})
	u.reg("hostnameSync", u.hostSync)

	u.peersTable = widget.NewTableWithHeaders(
		func() (int, int) { return len(u.peers), len(u.peersHeader) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(id widget.TableCellID, o fyne.CanvasObject) {
			l := o.(*widget.Label)
			if id.Row >= len(u.peers) {
				l.SetText("")
				return
			}
			p := u.peers[id.Row]
			l.SetText([]string{p.Name, p.IP, p.OS, p.Online, p.Path}[id.Col])
		},
	)
	u.peersTable.ShowHeaderColumn = false
	u.peersTable.CreateHeader = func() fyne.CanvasObject { return widget.NewLabel("") }
	u.peersTable.UpdateHeader = func(id widget.TableCellID, o fyne.CanvasObject) {
		o.(*widget.Label).SetText(u.peersHeader[id.Col])
	}
	for i, w := range []float32{220, 130, 80, 70, 200} {
		u.peersTable.SetColumnWidth(i, w)
	}
	u.reg("peers", u.peersTable)

	quitBtn := widget.NewButton("Quit", a.quit)
	u.reg("quit", quitBtn)

	top := container.NewVBox(
		container.NewGridWithColumns(2,
			container.NewBorder(nil, nil, widget.NewLabel("Profile:"), nil, u.profileSel),
			container.NewBorder(nil, nil, nil, newBtn, u.newProfile),
		),
		widget.NewSeparator(),
		u.stateLabel,
		u.ipsLabel,
		u.userLabel,
		u.hostLabel,
		u.proxyLabel,
		u.errLabel,
		container.NewHBox(u.loginBtn, u.connectBtn, u.logoutBtn),
		container.NewBorder(nil, nil, nil, u.authKeyBtn, u.authKey),
		u.shieldsUp,
		container.NewBorder(nil, nil, widget.NewLabel("Hostname:"), container.NewHBox(u.hostBtn, u.hostSync), u.hostEntry),
		widget.NewSeparator(),
		widget.NewLabel("Peers"),
	)
	bottom := container.NewHBox(quitBtn)
	u.win.SetContent(container.NewBorder(top, bottom, nil, nil, u.peersTable))
	u.win.Resize(fyne.NewSize(760, 720))
	u.win.SetCloseIntercept(a.quit)
}

func (u *UI) openURL(s string) {
	pu, err := url.Parse(s)
	if err != nil {
		u.showErr(err)
		return
	}
	if err := u.app.fy.OpenURL(pu); err != nil {
		u.showErr(err)
	}
}

// showErr displays an error. It must be called on the UI goroutine.
func (u *UI) showErr(err error) {
	u.app.logf("ui error: %v", err)
	u.errLabel.SetText(err.Error())
}

// showErrAsync is showErr for use from other goroutines.
func (u *UI) showErrAsync(err error) {
	fyne.Do(func() { u.showErr(err) })
}

// scheduleRefresh coalesces state change notifications into one UI
// refresh on the UI goroutine. It's safe to call from any goroutine.
func (u *UI) scheduleRefresh() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.refreshTimer != nil {
		return
	}
	u.refreshTimer = time.AfterFunc(150*time.Millisecond, func() {
		u.mu.Lock()
		u.refreshTimer = nil
		u.mu.Unlock()
		fyne.Do(u.refresh)
	})
}

// refresh redraws everything from the app's current state. It must
// be called on the UI goroutine.
func (u *UI) refresh() {
	a := u.app
	names, _ := a.profiles.List()
	u.profileSel.Options = names
	if cur := a.currentProfile(); u.profileSel.Selected != cur {
		u.profileSel.SetSelected(cur)
	} else {
		u.profileSel.Refresh()
	}

	b := a.backend
	if b == nil {
		u.stateLabel.SetText("State: no profile loaded")
		u.ipsLabel.SetText("")
		u.userLabel.SetText("")
		u.hostLabel.SetText("")
		u.proxyLabel.SetText("")
		u.peers = nil
		u.peersTable.Refresh()
		return
	}
	cfg := b.Config()
	st := b.Status()
	if st == nil {
		u.stateLabel.SetText("State: starting")
		return
	}
	state := st.BackendState
	u.stateLabel.SetText("State: " + state)

	var ips []string
	for _, ip := range st.TailscaleIPs {
		ips = append(ips, ip.String())
	}
	u.ipsLabel.SetText("Tailscale IPs: " + strings.Join(ips, ", "))

	user := ""
	if st.Self != nil {
		if up, ok := st.User[st.Self.UserID]; ok {
			user = up.LoginName
		}
	}
	tailnet := ""
	if st.CurrentTailnet != nil {
		tailnet = st.CurrentTailnet.Name
	}
	u.userLabel.SetText(fmt.Sprintf("Account: %s   Tailnet: %s", user, tailnet))

	host := cfg.hostname()
	dns := ""
	if st.Self != nil {
		host = st.Self.HostName
		dns = strings.TrimSuffix(st.Self.DNSName, ".")
	}
	u.hostLabel.SetText(fmt.Sprintf("Hostname: %s   DNS name: %s", host, dns))
	if !u.hostEntry.Disabled() && u.hostEntry.Text == "" {
		u.hostEntry.SetText(cfg.hostname())
	}
	if u.hostSync.Checked != cfg.SyncHostname {
		u.hostSync.SetChecked(cfg.SyncHostname)
	}
	if cfg.SyncHostname {
		u.hostEntry.Disable()
		u.hostBtn.Disable()
	} else {
		u.hostEntry.Enable()
		u.hostBtn.Enable()
	}

	if pa := b.ProxyAddr(); pa != "" {
		u.proxyLabel.SetText("Proxy (SOCKS5 + HTTP): " + pa)
	} else {
		u.proxyLabel.SetText("Proxy: not running")
	}
	u.errLabel.SetText(b.LastErr())

	authURL := b.AuthURL()
	switch state {
	case ipn.NeedsLogin.String(), ipn.NoState.String():
		u.loginBtn.Show()
		if authURL != "" {
			u.loginBtn.SetText("Log in with browser (open link)")
		} else {
			u.loginBtn.SetText("Log in with browser")
		}
		u.authKey.Show()
		u.authKeyBtn.Show()
		u.logoutBtn.Hide()
		u.connectBtn.Hide()
	default:
		u.loginBtn.Hide()
		u.authKey.Hide()
		u.authKeyBtn.Hide()
		u.logoutBtn.Show()
		u.connectBtn.Show()
		if state == ipn.Running.String() {
			u.connectBtn.SetText("Disconnect")
		} else {
			u.connectBtn.SetText("Connect")
		}
	}

	if prefs := a.prefs(); prefs != nil && u.shieldsUp.Checked != prefs.ShieldsUp {
		u.shieldsUp.SetChecked(prefs.ShieldsUp)
	}

	u.peers = peerRows(st)
	u.peersTable.Refresh()
}

// peerRows flattens a status into sorted table rows.
func peerRows(st *ipnstate.Status) []peerRow {
	var rows []peerRow
	for _, p := range st.Peer {
		ip := ""
		if len(p.TailscaleIPs) > 0 {
			ip = p.TailscaleIPs[0].String()
		}
		online := "no"
		if p.Online {
			online = "yes"
		}
		path := ""
		switch {
		case !p.Online:
			path = "-"
		case p.CurAddr != "":
			path = "direct " + p.CurAddr
		case p.Relay != "":
			path = "relay " + p.Relay
		case p.Active:
			path = "active"
		default:
			path = "idle"
		}
		name := strings.TrimSuffix(p.DNSName, ".")
		if i := strings.IndexByte(name, '.'); i > 0 {
			name = name[:i]
		}
		if name == "" {
			name = p.HostName
		}
		if p.ExitNode {
			name += " (exit node)"
		}
		rows = append(rows, peerRow{Name: name, IP: ip, OS: p.OS, Online: online, Path: path})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Online != rows[j].Online {
			return rows[i].Online == "yes"
		}
		return rows[i].Name < rows[j].Name
	})
	return rows
}
