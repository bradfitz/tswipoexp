package main

import (
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net"
	"net/http"
	"sort"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// serveDebug starts the debug and automation HTTP endpoint on addr.
// It exists so the GUI can be driven and inspected remotely during
// development. It's off unless --debug-addr is given, and it refuses
// non-loopback addresses.
func (a *App) serveDebug(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("debug address %q must be a loopback IP", addr)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /debug/state", a.debugState)
	mux.HandleFunc("GET /debug/tree", a.debugTree)
	mux.HandleFunc("POST /debug/tap", a.debugTap)
	mux.HandleFunc("POST /debug/set", a.debugSet)
	mux.HandleFunc("GET /debug/screenshot", a.debugScreenshot)
	mux.HandleFunc("GET /debug/log", a.debugLog)
	mux.HandleFunc("GET /debug/status", a.debugStatus)
	mux.HandleFunc("POST /debug/quit", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "quitting")
		fyne.Do(a.quit)
	})
	a.logf("debug endpoint listening on http://%v/debug/state", ln.Addr())
	logged := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/debug/log" && r.URL.Path != "/debug/state" && r.URL.Path != "/debug/tree" && r.URL.Path != "/debug/status" {
			a.logf("debug: %s %s", r.Method, r.URL.RequestURI())
		}
		mux.ServeHTTP(w, r)
	})
	go http.Serve(ln, logged)
	return nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

// debugState summarizes the app state as JSON.
func (a *App) debugState(w http.ResponseWriter, r *http.Request) {
	type state struct {
		Profile     string
		Profiles    []string
		StateDir    string
		Backend     string
		AuthURL     string
		LastErr     string
		ProxyAddr   string
		SSH         string // sshMode, or "off"
		TailscaleIP []string
		Hostname    string
		DNSName     string
		Peers       []peerRow
	}
	names, _ := a.profiles.List()
	s := state{Profile: a.currentProfile(), Profiles: names, StateDir: a.profiles.Root}
	a.mu.Lock()
	b := a.backend
	a.mu.Unlock()
	if b != nil {
		s.AuthURL = b.AuthURL()
		s.LastErr = b.LastErr()
		s.ProxyAddr = b.ProxyAddr()
		s.SSH = sshOff
		if b.SSHRunning() {
			s.SSH = b.Config().sshMode()
		}
		if st := b.Status(); st != nil {
			s.Backend = st.BackendState
			for _, ip := range st.TailscaleIPs {
				s.TailscaleIP = append(s.TailscaleIP, ip.String())
			}
			if st.Self != nil {
				s.Hostname = st.Self.HostName
				s.DNSName = st.Self.DNSName
			}
			s.Peers = peerRows(st)
		}
	}
	writeJSON(w, s)
}

// debugStatus dumps the raw ipnstate.Status.
func (a *App) debugStatus(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	b := a.backend
	a.mu.Unlock()
	if b == nil {
		http.Error(w, "no backend", 503)
		return
	}
	writeJSON(w, b.Status())
}

// widgetInfo describes one named widget.
type widgetInfo struct {
	Name     string
	Type     string
	Text     string   `json:",omitempty"`
	Options  []string `json:",omitempty"`
	Checked  *bool    `json:",omitempty"`
	Disabled bool     `json:",omitempty"`
	Visible  bool
	Rows     int `json:",omitempty"`
}

func describe(name string, o fyne.CanvasObject, u *UI) widgetInfo {
	wi := widgetInfo{Name: name, Type: fmt.Sprintf("%T", o), Visible: o.Visible()}
	if d, ok := o.(fyne.Disableable); ok {
		wi.Disabled = d.Disabled()
	}
	switch v := o.(type) {
	case *widget.Label:
		wi.Text = v.Text
	case *widget.Button:
		wi.Text = v.Text
	case *widget.Entry:
		wi.Text = v.Text
	case *widget.Select:
		wi.Text = v.Selected
		wi.Options = v.Options
	case *widget.Check:
		wi.Text = v.Text
		c := v.Checked
		wi.Checked = &c
	case *widget.RadioGroup:
		wi.Text = v.Selected
		wi.Options = v.Options
	case *copyText:
		wi.Text = v.value
	case *widget.Table:
		wi.Rows = len(u.peers)
	}
	return wi
}

// debugTree lists every named widget with its current state. Fyne
// widgets are read on the UI goroutine.
func (a *App) debugTree(w http.ResponseWriter, r *http.Request) {
	var out []widgetInfo
	fyne.DoAndWait(func() {
		u := a.ui
		u.mu.Lock()
		defer u.mu.Unlock()
		for name, o := range u.widgets {
			out = append(out, describe(name, o, u))
		}
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, out)
}

func (a *App) lookupWidget(name string) (fyne.CanvasObject, bool) {
	a.ui.mu.Lock()
	defer a.ui.mu.Unlock()
	o, ok := a.ui.widgets[name]
	return o, ok
}

// debugTap taps the named button, or toggles the named check.
func (a *App) debugTap(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	if prefix, action, ok := strings.Cut(name, "."); ok && (action == "save" || action == "cancel") {
		// The settings dialogs aren't canvas objects; drive them
		// directly.
		var err error
		fyne.DoAndWait(func() {
			var d *dialog.ConfirmDialog
			switch prefix {
			case "outbound":
				d = a.ui.outboundDialog
			case "inbound":
				d = a.ui.inboundDialog
			case "hostname":
				d = a.ui.hostnameDialog
			case "profile":
				d = a.ui.newProfileDialog
			case "rename":
				d = a.ui.renameDialog
			case "delete":
				d = a.ui.deleteDialog
			}
			if d == nil {
				err = fmt.Errorf("%s settings dialog is not open", prefix)
				return
			}
			if action == "save" {
				d.Confirm()
			} else {
				d.Hide()
			}
		})
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		fmt.Fprintf(w, "tapped %s\n", name)
		return
	}
	o, ok := a.lookupWidget(name)
	if !ok {
		http.Error(w, "no widget named "+name, 404)
		return
	}
	var err error
	fyne.DoAndWait(func() {
		switch v := o.(type) {
		case *widget.Button:
			if v.Disabled() {
				err = fmt.Errorf("button %q is disabled", name)
				return
			}
			if v.OnTapped != nil {
				v.OnTapped()
			}
		case *widget.Check:
			v.SetChecked(!v.Checked)
		case *copyText:
			v.Tapped(nil)
		default:
			err = fmt.Errorf("widget %q is a %T, not tappable", name, o)
		}
	})
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	fmt.Fprintf(w, "tapped %s\n", name)
}

// debugSet sets an entry's text, a select's choice, or a check's
// state (with text "true" or "false").
func (a *App) debugSet(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	text := r.FormValue("text")
	o, ok := a.lookupWidget(name)
	if !ok {
		http.Error(w, "no widget named "+name, 404)
		return
	}
	var err error
	fyne.DoAndWait(func() {
		switch v := o.(type) {
		case *widget.Entry:
			v.SetText(text)
		case *widget.RadioGroup:
			// Accept an option index or a prefix of the label.
			for i, opt := range v.Options {
				if text == fmt.Sprint(i) || strings.HasPrefix(opt, text) {
					v.SetSelected(opt)
					return
				}
			}
			err = fmt.Errorf("no option matching %q", text)
		case *widget.Select:
			// Accept an exact option or a unique prefix of one.
			var match string
			for _, opt := range v.Options {
				if opt == text {
					match = opt
					break
				}
				if strings.HasPrefix(opt, text) {
					if match != "" {
						err = fmt.Errorf("option prefix %q is ambiguous", text)
						return
					}
					match = opt
				}
			}
			if match == "" {
				err = fmt.Errorf("no option matching %q", text)
				return
			}
			v.SetSelected(match)
		case *widget.List:
			if a.ui.profileMgr != nil && a.ui.profileMgr.list == v {
				for i, n := range a.ui.profileMgr.names {
					if n == text {
						v.Select(i)
						return
					}
				}
				err = fmt.Errorf("no profile named %q", text)
			}
		case *widget.Check:
			v.SetChecked(strings.EqualFold(text, "true"))
		default:
			err = fmt.Errorf("widget %q is a %T, not settable", name, o)
		}
	})
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	fmt.Fprintf(w, "set %s\n", name)
}

// debugScreenshot renders the window's canvas to a PNG. This doesn't
// touch the real screen, so it works even when the desktop is locked.
func (a *App) debugScreenshot(w http.ResponseWriter, r *http.Request) {
	var img image.Image
	fyne.DoAndWait(func() {
		img = a.ui.win.Canvas().Capture()
	})
	w.Header().Set("Content-Type", "image/png")
	if err := png.Encode(w, img); err != nil {
		a.logf("screenshot: %v", err)
	}
}

// debugLog returns the most recent log lines as text.
func (a *App) debugLog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	for _, l := range a.logs.Lines(500) {
		w.Write([]byte(l))
	}
}
