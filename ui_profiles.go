package main

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// profileManager is the window listing profiles with their last
// known details, and the Rename and Delete actions.
type profileManager struct {
	u        *UI
	win      fyne.Window
	names    []string
	list     *widget.List
	selected string
	details  *widget.Label
	title    *widget.Label
	rename   *widget.Button
	delete   *widget.Button
}

// showProfileManager opens the profile window, or brings it forward.
func (u *UI) showProfileManager() {
	if u.profileMgr != nil {
		u.profileMgr.reload()
		u.profileMgr.win.Show()
		u.profileMgr.win.RequestFocus()
		return
	}
	m := &profileManager{u: u}
	m.win = u.app.fy.NewWindow("tswipoexp profiles")
	m.win.SetIcon(appIcon)

	m.list = widget.NewList(
		func() int { return len(m.names) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(i widget.ListItemID, o fyne.CanvasObject) {
			name := m.names[i]
			if name == u.app.currentProfile() {
				name += "  (running)"
			}
			o.(*widget.Label).SetText(name)
		},
	)
	m.list.OnSelected = func(i widget.ListItemID) {
		m.selected = m.names[i]
		m.showDetails()
	}
	u.reg("profiles.list", m.list)

	m.rename = widget.NewButton("Rename...", m.askRename)
	m.delete = widget.NewButton("Delete...", m.askDelete)
	u.reg("profiles.rename", m.rename)
	u.reg("profiles.delete", m.delete)

	m.title = widget.NewLabel("")
	m.title.TextStyle = fyne.TextStyle{Bold: true}
	m.details = widget.NewLabel("Select a profile.")
	m.details.Wrapping = fyne.TextWrapWord
	u.reg("profiles.details", m.details)

	left := container.NewBorder(nil, container.NewHBox(m.rename, m.delete), nil, nil, m.list)
	right := container.NewBorder(m.title, nil, nil, nil, container.NewVScroll(m.details))
	split := container.NewHSplit(left, right)
	split.SetOffset(0.32)
	m.win.SetContent(split)
	m.win.Resize(fyne.NewSize(720, 400))
	m.win.SetOnClosed(func() {
		u.unregDialogWidgets("profiles.")
		u.profileMgr = nil
	})
	u.profileMgr = m
	m.reload()
	m.win.Show()
}

// reload refreshes the list and reselects the current selection (or
// the running profile).
func (m *profileManager) reload() {
	names, _ := m.u.app.profiles.List()
	m.names = names
	if m.selected == "" {
		m.selected = m.u.app.currentProfile()
	}
	m.list.Refresh()
	for i, n := range names {
		if n == m.selected {
			m.list.Select(i)
			return
		}
	}
	m.selected = ""
	m.list.UnselectAll()
	m.showDetails()
}

// showDetails renders the selected profile's last known details.
func (m *profileManager) showDetails() {
	name := m.selected
	if name == "" {
		m.title.SetText("")
		m.details.SetText("Select a profile.")
		m.rename.Disable()
		m.delete.Disable()
		return
	}
	a := m.u.app
	running := a.currentProfile() == name
	m.rename.Enable()
	if running {
		m.delete.Disable()
	} else {
		m.delete.Enable()
	}
	s := a.profiles.LoadSummary(name)
	cfg, _ := a.profiles.LoadConfig(name)
	var b strings.Builder
	fmt.Fprintf(&b, "Directory: %s\n\n", a.profiles.Dir(name))
	if s.UpdatedAt.IsZero() {
		b.WriteString("No details yet: this profile hasn't run.\n")
	} else {
		if running {
			b.WriteString("Currently running.\n\n")
		} else {
			fmt.Fprintf(&b, "Last seen %s\n\n", s.UpdatedAt.Local().Format("2006-01-02 15:04"))
		}
		fmt.Fprintf(&b, "State: %s\n", stateText(s.State))
		fmt.Fprintf(&b, "Tailnet: %s\n", s.Tailnet)
		fmt.Fprintf(&b, "Account: %s\n", s.Account)
		fmt.Fprintf(&b, "Hostname: %s\n", s.Hostname)
		fmt.Fprintf(&b, "DNS name: %s\n", s.DNSName)
		fmt.Fprintf(&b, "Tailscale IPs: %s\n", strings.Join(s.IPs, ", "))
	}
	if cfg != nil {
		b.WriteString("\nSettings:\n")
		fmt.Fprintf(&b, "  Outbound access: %v (%s)\n", cfg.registerProxy(), cfg.proxyMode())
		fmt.Fprintf(&b, "  SSH: %v\n", cfg.SSH)
		if cfg.SyncHostname {
			b.WriteString("  Hostname follows this computer's name\n")
		}
	}
	m.title.SetText(name)
	m.details.SetText(b.String())
}

// askRename prompts for a new name and renames, restarting the
// profile if it's the running one.
func (m *profileManager) askRename() {
	from := m.selected
	if from == "" {
		return
	}
	entry := widget.NewEntry()
	entry.SetText(from)
	m.u.reg("rename.name", entry)
	note := ""
	if m.u.app.currentProfile() == from {
		note = "\nThis profile is running; it will be restarted under the new name."
	}
	d := dialog.NewCustomConfirm("Rename profile", "Rename", "Cancel",
		container.NewVBox(widget.NewLabel("New name for \""+from+"\":"+note), entry), func(ok bool) {
			defer m.u.unregDialogWidgets("rename.")
			to := strings.TrimSpace(entry.Text)
			if !ok || to == "" || to == from {
				return
			}
			m.rename.Disable()
			m.u.app.renameProfile(from, to, func(err error) {
				if err != nil {
					dialog.ShowError(err, m.win)
				} else {
					m.selected = to
				}
				m.reload()
			})
		}, m.win)
	m.u.renameDialog = d
	d.SetOnClosed(func() { m.u.renameDialog = nil })
	d.Resize(fyne.NewSize(460, 180))
	d.Show()
}

// askDelete confirms and deletes the selected, non-running profile.
func (m *profileManager) askDelete() {
	name := m.selected
	if name == "" {
		return
	}
	d := dialog.NewConfirm("Delete profile",
		fmt.Sprintf("Delete profile %q and all its state, including its node key?\n\nThe device stays listed in the tailnet's admin console until removed there. This cannot be undone.", name),
		func(ok bool) {
			if !ok {
				return
			}
			if err := m.u.app.deleteProfile(name); err != nil {
				dialog.ShowError(err, m.win)
			} else {
				m.selected = ""
			}
			m.reload()
			m.u.scheduleRefresh()
		}, m.win)
	d.SetConfirmImportance(widget.DangerImportance)
	m.u.deleteDialog = d
	d.SetOnClosed(func() { m.u.deleteDialog = nil })
	d.Show()
}
