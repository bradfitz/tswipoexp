package main

import (
	"fmt"
	"net"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// showOutboundSettings opens the per-profile "Outbound access
// settings" dialog: how the proxy is registered with the Windows
// session and which safety nets restore the user's settings if
// tswipoexp dies. Whether it's registered at all is the "Outbound
// access" checkbox in the main window. Widgets are registered under "outbound."
// names for the debug endpoint while the dialog is open.
func (u *UI) showOutboundSettings() {
	a := u.app
	b := a.backend
	if b == nil {
		u.showErr(fmt.Errorf("no profile loaded"))
		return
	}
	cfg := *b.Config() // a snapshot for the initial widget values only

	mode := widget.NewRadioGroup([]string{
		"Auto-config script (PAC): only tailnet traffic uses the proxy; apps go direct when tswipoexp isn't running",
		"Fixed proxy: all traffic uses the proxy; needs the safety nets below if tswipoexp dies",
	}, nil)
	mode.Required = true
	if cfg.proxyMode() == proxyModeStatic {
		mode.SetSelected(mode.Options[1])
	} else {
		mode.SetSelected(mode.Options[0])
	}

	allTraffic := widget.NewCheck("PAC: route all traffic through the proxy, not only the tailnet (an exit node does this anyway)", nil)
	allTraffic.SetChecked(cfg.PACAllTraffic)

	envVars := widget.NewCheck("Also set HTTP_PROXY, HTTPS_PROXY, and NO_PROXY for command line tools", nil)
	envVars.SetChecked(cfg.setEnvVars())

	watchdog := widget.NewCheck("Run a watchdog that restores your settings within seconds if tswipoexp dies", nil)
	watchdog.SetChecked(cfg.watchdog())

	runOnce := widget.NewCheck("Restore your settings at next logon if the computer restarts while tswipoexp is running", nil)
	runOnce.SetChecked(cfg.runOnceRestore())

	addr := widget.NewEntry()
	addr.SetText(cfg.proxyAddr())
	addr.SetPlaceHolder(defaultProxyAddr)

	updateEnabled := func() {
		if mode.Selected == mode.Options[0] {
			allTraffic.Enable()
		} else {
			allTraffic.Disable()
		}
	}
	mode.OnChanged = func(string) { updateEnabled() }
	updateEnabled()

	for name, w := range map[string]fyne.CanvasObject{
		"outbound.mode":       mode,
		"outbound.allTraffic": allTraffic,
		"outbound.envVars":    envVars,
		"outbound.watchdog":   watchdog,
		"outbound.runOnce":    runOnce,
		"outbound.addr":       addr,
	} {
		u.reg(name, w)
	}

	content := container.NewVBox(
		widget.NewLabel("These apply while \"Outbound access\" is checked in the main window."),
		widget.NewSeparator(),
		widget.NewLabel("How apps find the proxy:"),
		mode,
		allTraffic,
		widget.NewSeparator(),
		envVars,
		widget.NewLabel("Safety nets (the proxy settings are also restored on every clean exit and every start):"),
		watchdog,
		runOnce,
		widget.NewSeparator(),
		container.NewBorder(nil, nil, widget.NewLabel("Proxy listen address:"), nil, addr),
		widget.NewLabel("Changing the address restarts the profile's node."),
	)

	d := dialog.NewCustomConfirm("Outbound access settings", "Save", "Cancel", content, func(ok bool) {
		defer u.unregDialogWidgets("outbound.")
		if !ok {
			return
		}
		newAddr := strings.TrimSpace(addr.Text)
		if newAddr == "" {
			newAddr = defaultProxyAddr
		}
		if _, _, err := net.SplitHostPort(newAddr); err != nil {
			u.showErr(fmt.Errorf("proxy address %q: %v", newAddr, err))
			return
		}
		env, wd, ro := envVars.Checked, watchdog.Checked, runOnce.Checked
		static := mode.Selected == mode.Options[1]
		all := allTraffic.Checked
		addrChanged := newAddr != b.Config().proxyAddr()
		a.applyOutbound(func(c *Config) {
			c.SetEnvVars = &env
			c.Watchdog = &wd
			c.RunOnceRestore = &ro
			c.PACAllTraffic = all
			if static {
				c.ProxyMode = proxyModeStatic
			} else {
				c.ProxyMode = proxyModePAC
			}
			if newAddr == defaultProxyAddr {
				c.ProxyAddr = ""
			} else {
				c.ProxyAddr = newAddr
			}
		}, addrChanged)
	}, u.win)
	u.outboundDialog = d
	d.SetOnClosed(func() { u.outboundDialog = nil })
	d.Resize(fyne.NewSize(700, 520))
	d.Show()
}

// unregDialogWidgets drops a dialog's widgets (registered under the
// given name prefix) from the debug registry once it closes.
func (u *UI) unregDialogWidgets(prefix string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for name := range u.widgets {
		if strings.HasPrefix(name, prefix) {
			delete(u.widgets, name)
		}
	}
}

// showInboundSettings opens the per-profile "Inbound access settings"
// dialog. Whether inbound connections are allowed at all is the
// "Inbound access" checkbox in the main window (Shields Up when off);
// this holds what the node offers to peers when they are allowed.
func (u *UI) showInboundSettings() {
	a := u.app
	b := a.backend
	if b == nil {
		u.showErr(fmt.Errorf("no profile loaded"))
		return
	}
	cfg := *b.Config()

	sshCheck := widget.NewCheck("SSH server: let my other tailnet devices open a PowerShell session as this Windows user", nil)
	sshCheck.SetChecked(cfg.SSH)
	u.reg("inbound.ssh", sshCheck)

	content := container.NewVBox(
		widget.NewLabel("These apply while \"Inbound access\" is checked in the main window."),
		widget.NewSeparator(),
		sshCheck,
		widget.NewLabel("Only devices signed in to the same tailnet account as this node are accepted."),
	)
	d := dialog.NewCustomConfirm("Inbound access settings", "Save", "Cancel", content, func(ok bool) {
		defer u.unregDialogWidgets("inbound.")
		if !ok {
			return
		}
		a.setSSH(sshCheck.Checked)
	}, u.win)
	u.inboundDialog = d
	d.SetOnClosed(func() { u.inboundDialog = nil })
	d.Resize(fyne.NewSize(620, 260))
	d.Show()
}
