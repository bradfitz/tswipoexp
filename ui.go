package main

import (
	"context"
	"fmt"
	"image/color"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
)

// UI is the main window and its widgets.
type UI struct {
	app *App
	win fyne.Window

	// Widgets are registered by name so the debug endpoint can
	// find them. Everything the user can act on has a name.
	mu      sync.Mutex
	widgets map[string]fyne.CanvasObject

	profileSel       *widget.Select
	stateDot         *canvas.Circle
	stateLabel       *widget.Label
	newProfileDialog *dialog.ConfirmDialog
	ipsLabel         *widget.Label
	userLabel        *widget.Label
	hostLabel        *widget.Label
	proxyLabel       *widget.Label
	errLabel         *widget.Label
	loginBtn         *widget.Button
	authKey          *widget.Entry
	authKeyCell      *fyne.Container
	authKeyBtn       *widget.Button
	logoutBtn        *widget.Button
	connectBtn       *widget.Button
	outbound         *widget.Check // registered as the system proxy
	inbound          *widget.Check // the inverse of Shields Up
	// outboundDialog and inboundDialog are the open settings dialogs,
	// or nil.
	outboundDialog *dialog.ConfirmDialog
	inboundDialog  *dialog.ConfirmDialog
	exitNode       *widget.Select
	exitNodeLabel  *widget.Label
	exitNodeIDs    map[string]tailcfg.StableNodeID // select option label to node
	peersLabel     *widget.Label
	peersTable     *widget.Table
	peersHeader    []string
	peersWin       fyne.Window // the separate peers window, or nil when closed
	hostnameDialog *dialog.ConfirmDialog

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
		switch {
		case name == "":
		case name == newProfileItem:
			u.showNewProfileDialog()
		case a.currentProfile() != name:
			a.switchProfile(name)
		}
	})
	u.reg("profile", u.profileSel)

	// The state dot: a small circle whose color tracks the backend
	// state. It sits in a fixed cell so the row's height matches the
	// labels next to it.
	u.stateDot = canvas.NewCircle(stateColorGray)
	u.stateDot.Resize(fyne.NewSize(14, 14))
	u.stateDot.Move(fyne.NewPos(4, 12))
	dotCell := container.NewGridWrap(fyne.NewSize(22, 38), container.NewWithoutLayout(u.stateDot))

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
	// An entry in an HBox gets only its minimum width, so give it a
	// fixed cell. The cell, not the entry, is what gets hidden.
	u.authKeyCell = container.NewGridWrap(fyne.NewSize(300, 38), u.authKey)
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

	u.outbound = widget.NewCheck("Outbound access: use this node as the Windows system proxy while running", func(on bool) {
		// Update the look right away; the Windows registration
		// work behind setOutboundEnabled takes a moment.
		u.setOutboundLook(on)
		a.setOutboundEnabled(on)
	})
	u.reg("outbound", u.outbound)
	outboundBtn := widget.NewButton("Settings...", u.showOutboundSettings)
	u.reg("outboundSettings", outboundBtn)

	u.inbound = widget.NewCheck("Inbound access: let tailnet peers connect to this node (off = Shields Up)", func(on bool) {
		b := a.backend
		if b == nil {
			return
		}
		if prefs := a.prefs(); prefs != nil && prefs.ShieldsUp == !on {
			return // programmatic update
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := b.SetShieldsUp(ctx, !on); err != nil {
				u.showErrAsync(err)
			}
		}()
	})
	u.reg("inbound", u.inbound)
	inboundBtn := widget.NewButton("Settings...", u.showInboundSettings)
	u.reg("inboundSettings", inboundBtn)

	u.exitNodeIDs = map[string]tailcfg.StableNodeID{}
	u.exitNode = widget.NewSelect([]string{exitNodeNone}, func(label string) {
		b := a.backend
		if b == nil || label == "" {
			return
		}
		id := u.exitNodeIDs[label] // zero for exitNodeNone
		if cur := a.prefs(); cur != nil && cur.ExitNodeID == id {
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := b.SetExitNode(ctx, id); err != nil {
				u.showErrAsync(err)
			}
		}()
	})
	u.exitNode.PlaceHolder = exitNodeNone
	u.reg("exitNode", u.exitNode)
	u.exitNodeLabel = widget.NewLabel("Exit node:")

	hostBtn := widget.NewButton("Edit...", u.showHostnameDialog)
	u.reg("editHostname", hostBtn)

	u.peersLabel = widget.NewLabel("Peers: 0")
	u.reg("peersCount", u.peersLabel)
	peersBtn := widget.NewButton("View...", u.showPeersWindow)
	u.reg("viewPeers", peersBtn)

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
	for i, w := range []float32{270, 130, 80, 70, 200} {
		u.peersTable.SetColumnWidth(i, w)
	}
	u.reg("peers", u.peersTable)

	quitBtn := widget.NewButton("Quit and disconnect", a.quit)
	u.reg("quit", quitBtn)
	closeHint := widget.NewLabel("Closing the window keeps tswipoexp running in the tray.")
	closeHint.TextStyle = fyne.TextStyle{Italic: true}

	top := container.NewVBox(
		container.NewBorder(nil, nil, widget.NewLabel("Profile:"), nil, u.profileSel),
		widget.NewSeparator(),
		container.NewHBox(dotCell, u.stateLabel, u.loginBtn, u.authKeyCell, u.authKeyBtn, u.connectBtn, u.logoutBtn),
		u.ipsLabel,
		u.userLabel,
		container.NewBorder(nil, nil, nil, hostBtn, u.hostLabel),
		container.NewBorder(nil, nil, nil, peersBtn, u.peersLabel),
		u.errLabel,
		widget.NewSeparator(),
		container.NewBorder(nil, nil, nil, outboundBtn, u.outbound),
		indent(u.proxyLabel),
		indent(container.NewBorder(nil, nil, u.exitNodeLabel, nil, u.exitNode)),
		container.NewBorder(nil, nil, nil, inboundBtn, u.inbound),
	)
	bottom := container.NewBorder(nil, nil, quitBtn, nil, closeHint)
	u.win.SetContent(container.NewBorder(top, bottom, nil, nil, layout.NewSpacer()))
	size := defaultWindowSize
	if gs := loadGUIState(a.profiles.Root); gs.WindowWidth > 200 && gs.WindowHeight > 200 {
		size = fyne.NewSize(gs.WindowWidth, gs.WindowHeight)
	}
	u.win.Resize(size)
	u.win.SetIcon(appIcon)

	// With a tray icon, closing the window just hides it; the node
	// keeps running. Quit is in the tray menu and the window.
	if desk, ok := a.fy.(desktop.App); ok {
		desk.SetSystemTrayIcon(appIcon)
		desk.SetSystemTrayMenu(fyne.NewMenu("tswipoexp",
			fyne.NewMenuItem("Open tswipoexp", func() {
				u.win.Show()
				u.win.RequestFocus()
			}),
			fyne.NewMenuItemSeparator(),
			fyne.NewMenuItem("Quit", a.quit),
		))
		u.win.SetCloseIntercept(func() {
			u.saveWindowSize()
			u.win.Hide()
		})
	} else {
		u.win.SetCloseIntercept(a.quit)
	}
}

// fitToScreen shrinks the window if it's taller or wider than the
// screen, which happens on small, heavily scaled displays. It measures
// the real on-screen window rather than trusting the canvas scale,
// since the two didn't agree on a high-DPI laptop. It must run on the
// UI goroutine after the window is shown.
func (u *UI) fitToScreen() {
	screenW, screenH, ok := screenSizePixels()
	if !ok {
		return
	}
	winW, winH, ok := windowSizePixels(u.win.Title())
	if !ok || winW <= 0 || winH <= 0 {
		return
	}
	sz := u.win.Canvas().Size()
	// Leave room for the taskbar and some margin.
	maxW, maxH := screenW*0.95, screenH*0.88
	u.app.logf("fitToScreen: screen %vx%v px, window %vx%v px, canvas %vx%v pt", screenW, screenH, winW, winH, sz.Width, sz.Height)
	newSz := sz
	if winW > maxW {
		newSz.Width = sz.Width * maxW / winW
	}
	if winH > maxH {
		newSz.Height = sz.Height * maxH / winH
	}
	if newSz != sz {
		u.app.logf("fitToScreen: resizing canvas to %vx%v pt", newSz.Width, newSz.Height)
		u.win.Resize(newSz)
		u.win.CenterOnScreen()
	}
}

// saveWindowSize records the window size for next time. It must run
// on the UI goroutine.
func (u *UI) saveWindowSize() {
	sz := u.win.Canvas().Size()
	if sz.Width < 200 || sz.Height < 200 {
		return
	}
	if err := saveGUIState(u.app.profiles.Root, guiState{WindowWidth: sz.Width, WindowHeight: sz.Height}); err != nil {
		u.app.logf("saving window size: %v", err)
	}
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
	u.profileSel.Options = append(append([]string(nil), names...), newProfileItem)
	if cur := a.currentProfile(); u.profileSel.Selected != cur {
		u.profileSel.SetSelected(cur)
	} else {
		u.profileSel.Refresh()
	}

	b := a.backend
	if b == nil {
		u.setState("No profile loaded", stateColorGray)
		u.ipsLabel.SetText("")
		u.userLabel.SetText("")
		u.hostLabel.SetText("")
		u.proxyLabel.SetText("")
		u.peers = nil
		u.peersLabel.SetText("Peers: 0")
		u.peersTable.Refresh()
		return
	}
	cfg := b.Config()
	st := b.Status()
	if st == nil {
		u.setState("Starting", stateColorGray)
		return
	}
	state := st.BackendState
	switch state {
	case ipn.Running.String():
		u.setState("Running", stateColorGreen)
	case ipn.NeedsLogin.String(), ipn.NeedsMachineAuth.String():
		u.setState(stateText(state), stateColorRed)
	default:
		u.setState(stateText(state), stateColorGray)
	}

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

	if pa := b.ProxyAddr(); pa != "" {
		reg := "not registered with Windows"
		if b.ProxyRegistered() {
			if cfg.proxyMode() == proxyModeStatic {
				reg = "registered as the Windows system proxy (fixed, all traffic)"
			} else if pacInputsFrom(st, cfg, pa).AllTraffic {
				reg = "registered as the Windows system proxy (PAC, all traffic)"
			} else {
				reg = "registered as the Windows system proxy (PAC, tailnet only)"
			}
		}
		u.proxyLabel.SetText(fmt.Sprintf("Proxy (SOCKS5 + HTTP): %s, %s", pa, reg))
	} else {
		u.proxyLabel.SetText("Proxy: not running")
	}
	u.errLabel.SetText(b.LastErr())

	authURL := b.AuthURL()
	show := func(o fyne.CanvasObject, on bool) {
		if on {
			o.Show()
		} else {
			o.Hide()
		}
	}
	needsLogin := state == ipn.NeedsLogin.String() || state == ipn.NoState.String()
	show(u.loginBtn, needsLogin)
	show(u.authKeyCell, needsLogin)
	show(u.authKeyBtn, needsLogin)
	if authURL != "" {
		u.loginBtn.SetText("Log in with browser (open link)")
	} else {
		u.loginBtn.SetText("Log in with browser")
	}
	running := state == ipn.Running.String()
	stopped := state == ipn.Stopped.String()
	show(u.connectBtn, running || stopped)
	show(u.logoutBtn, running || stopped)
	if running {
		u.connectBtn.SetText("Disconnect")
	} else {
		u.connectBtn.SetText("Connect")
	}

	prefs := a.prefs()
	if prefs != nil && u.inbound.Checked != !prefs.ShieldsUp {
		u.inbound.SetChecked(!prefs.ShieldsUp)
	}
	if u.outbound.Checked != cfg.registerProxy() {
		u.outbound.SetChecked(cfg.registerProxy())
	}
	u.setOutboundLook(cfg.registerProxy())
	u.refreshExitNodes(st, prefs)

	u.peers = peerRows(st)
	online := 0
	for _, p := range u.peers {
		if p.Online == "yes" {
			online++
		}
	}
	u.peersLabel.SetText(fmt.Sprintf("Peers: %d (%d online)", len(u.peers), online))
	u.peersTable.Refresh()
}

// showPeersWindow opens, or brings forward, the window with the peer
// table. The table is created once and kept in the main window's
// widget registry; it just moves between windows' content.
func (u *UI) showPeersWindow() {
	if u.peersWin != nil {
		u.peersWin.Show()
		u.peersWin.RequestFocus()
		return
	}
	w := u.app.fy.NewWindow("tswipoexp peers")
	w.SetIcon(appIcon)
	w.SetContent(u.peersTable)
	w.Resize(fyne.NewSize(760, 480))
	w.SetOnClosed(func() { u.peersWin = nil })
	u.peersWin = w
	w.Show()
}

// showHostnameDialog lets the user change the hostname advertised to
// the control plane, or tie it to the computer's name.
func (u *UI) showHostnameDialog() {
	a := u.app
	b := a.backend
	if b == nil {
		u.showErr(fmt.Errorf("no profile loaded"))
		return
	}
	cfg := *b.Config()
	entry := widget.NewEntry()
	entry.SetPlaceHolder(defaultHostname)
	entry.SetText(cfg.hostname())
	u.reg("hostname.entry", entry)
	sync := widget.NewCheck("Use this computer's name instead", func(on bool) {
		if on {
			entry.Disable()
		} else {
			entry.Enable()
		}
	})
	sync.SetChecked(cfg.SyncHostname)
	u.reg("hostname.sync", sync)
	content := container.NewVBox(
		widget.NewLabel("Enter the hostname to advertise to the control plane.\nThis influences what DNS name you're assigned."),
		entry,
		sync,
	)
	d := dialog.NewCustomConfirm("Hostname", "Save", "Cancel", content, func(ok bool) {
		defer u.unregDialogWidgets("hostname.")
		if !ok {
			return
		}
		a.setHostname(strings.TrimSpace(entry.Text), sync.Checked)
	}, u.win)
	u.hostnameDialog = d
	d.SetOnClosed(func() { u.hostnameDialog = nil })
	d.Resize(fyne.NewSize(520, 220))
	d.Show()
}

// exitNodeNone is the exit node dropdown's entry for no exit node.
const exitNodeNone = "None"

// refreshExitNodes rebuilds the exit node dropdown from the peers
// that offer to be one and selects the current choice.
func (u *UI) refreshExitNodes(st *ipnstate.Status, prefs *ipn.Prefs) {
	labels := []string{exitNodeNone}
	ids := map[string]tailcfg.StableNodeID{}
	selected := exitNodeNone
	for _, p := range st.Peer {
		if !p.ExitNodeOption {
			continue
		}
		label := shortName(p)
		if !p.Online {
			label += " (offline)"
		}
		labels = append(labels, label)
		ids[label] = p.ID
		if prefs != nil && prefs.ExitNodeID == p.ID {
			selected = label
		}
	}
	sort.Strings(labels[1:])
	if prefs != nil && selected == exitNodeNone && prefs.ExitNodeID != "" {
		// Set to a node we don't see (yet); show its ID rather
		// than pretend there's none.
		selected = string(prefs.ExitNodeID)
		labels = append(labels, selected)
		ids[selected] = prefs.ExitNodeID
	}
	u.exitNodeIDs = ids
	u.exitNode.Options = labels
	if u.exitNode.Selected != selected {
		u.exitNode.SetSelected(selected)
	} else {
		u.exitNode.Refresh()
	}
}

// shortName returns a peer's first DNS label, falling back to its
// hostname.
func shortName(p *ipnstate.PeerStatus) string {
	name := strings.TrimSuffix(p.DNSName, ".")
	if i := strings.IndexByte(name, '.'); i > 0 {
		name = name[:i]
	}
	if name == "" {
		name = p.HostName
	}
	return name
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
		name := shortName(p)
		if p.ExitNode {
			name += " (exit node)"
		} else if p.ExitNodeOption {
			name += " (exit node option)"
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

// newProfileItem is the dropdown entry that creates a profile.
const newProfileItem = "(New...)"

// State dot colors.
var (
	stateColorGreen = color.NRGBA{R: 0x2e, G: 0xb8, B: 0x5c, A: 0xff}
	stateColorRed   = color.NRGBA{R: 0xd0, G: 0x3a, B: 0x3a, A: 0xff}
	stateColorGray  = color.NRGBA{R: 0x9a, G: 0x9a, B: 0x9a, A: 0xff}
)

// setState updates the state text and dot color.
func (u *UI) setState(text string, c color.Color) {
	u.stateLabel.SetText(text)
	if u.stateDot.FillColor != c {
		u.stateDot.FillColor = c
		u.stateDot.Refresh()
	}
}

// stateText turns an ipn.State name into words.
func stateText(state string) string {
	switch state {
	case ipn.NeedsLogin.String():
		return "Needs login"
	case ipn.NeedsMachineAuth.String():
		return "Needs approval in the admin console"
	case ipn.Stopped.String():
		return "Disconnected"
	case ipn.Starting.String():
		return "Starting"
	case ipn.NoState.String():
		return "Starting"
	}
	return state
}

// showNewProfileDialog asks for a profile name, creates it, and
// switches to it. Cancelling puts the dropdown back on the current
// profile.
func (u *UI) showNewProfileDialog() {
	a := u.app
	entry := widget.NewEntry()
	entry.SetPlaceHolder("Profile name, for example Work or Home")
	u.reg("profile.name", entry)
	content := container.NewVBox(
		widget.NewLabel("Each profile is a separate node with its own login and settings,\nstored in its own directory under tswipoexp-state."),
		entry,
	)
	d := dialog.NewCustomConfirm("New profile", "Create", "Cancel", content, func(ok bool) {
		defer u.unregDialogWidgets("profile.")
		cur := a.currentProfile()
		if !ok {
			u.profileSel.SetSelected(cur)
			return
		}
		name := strings.TrimSpace(entry.Text)
		if name == "" {
			u.profileSel.SetSelected(cur)
			return
		}
		if err := a.profiles.Create(name); err != nil {
			u.showErr(err)
			u.profileSel.SetSelected(cur)
			return
		}
		a.switchProfile(name)
	}, u.win)
	u.newProfileDialog = d
	d.SetOnClosed(func() { u.newProfileDialog = nil })
	d.Resize(fyne.NewSize(520, 200))
	d.Show()
}

// setOutboundLook grays out the proxy line and exit node picker when
// the node isn't the system proxy, since neither applies then.
func (u *UI) setOutboundLook(on bool) {
	imp := widget.LowImportance
	if on {
		imp = widget.MediumImportance
		u.exitNode.Enable()
	} else {
		u.exitNode.Disable()
	}
	if u.proxyLabel.Importance != imp {
		u.proxyLabel.Importance = imp
		u.proxyLabel.Refresh()
		u.exitNodeLabel.Importance = imp
		u.exitNodeLabel.Refresh()
	}
}

// indent pushes a row right, for rows that belong to the checkbox
// above them.
func indent(o fyne.CanvasObject) fyne.CanvasObject {
	return container.NewBorder(nil, nil, container.NewGridWrap(fyne.NewSize(28, 1)), nil, o)
}
