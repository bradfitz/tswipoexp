package main

import (
	"fmt"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// logViewer is the window showing the in-memory log buffer as a live
// stream.
type logViewer struct {
	u      *UI
	win    fyne.Window
	list   *widget.List
	follow *widget.Check
	count  *widget.Label
	lines  []string
	next   uint64 // next sequence number to fetch
	gen    uint64 // buffer generation last seen

	// lastBottom is the scroll offset right after the last time we
	// scrolled to the bottom. If the list is later found scrolled
	// above it, the user scrolled up, and Follow turns itself off.
	lastBottom float32
	stop       chan struct{}
}

// showLogViewer opens the log window, or brings it forward.
func (u *UI) showLogViewer() {
	if u.logs != nil {
		u.logs.win.Show()
		u.logs.win.RequestFocus()
		return
	}
	v := &logViewer{u: u, stop: make(chan struct{})}
	v.win = u.app.fy.NewWindow("tswipoexp logs")
	v.win.SetIcon(appIcon)

	v.list = widget.NewList(
		func() int { return len(v.lines) },
		func() fyne.CanvasObject {
			l := widget.NewLabel("")
			l.TextStyle = fyne.TextStyle{Monospace: true}
			return l
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			if i < len(v.lines) {
				o.(*widget.Label).SetText(v.lines[i])
			}
		},
	)
	u.reg("logs.list", v.list)

	v.follow = widget.NewCheck("Follow", func(on bool) {
		if on {
			v.scrollToBottom()
		}
	})
	v.follow.SetChecked(true)
	u.reg("logs.follow", v.follow)

	clear := widget.NewButton("Clear", func() {
		u.app.logs.Clear()
		v.lines = nil
		v.list.Refresh()
		v.updateCount()
	})
	u.reg("logs.clear", clear)
	v.count = widget.NewLabel("")
	u.reg("logs.count", v.count)

	top := container.NewBorder(nil, nil, container.NewHBox(v.follow, clear), v.count, widget.NewLabel(""))
	v.win.SetContent(container.NewBorder(top, nil, nil, nil, v.list))
	v.win.Resize(fyne.NewSize(1000, 600))
	v.win.SetOnClosed(func() {
		close(v.stop)
		u.unregDialogWidgets("logs.")
		u.logs = nil
	})
	u.logs = v
	v.pull()
	v.win.Show()
	v.scrollToBottom()
	go v.loop()
}

// loop pulls new lines a few times a second while the window is open.
func (v *logViewer) loop() {
	t := time.NewTicker(300 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			fyne.Do(v.tick)
		case <-v.stop:
			return
		}
	}
}

// tick runs on the UI goroutine: detect a user scroll-up, fetch new
// lines, and follow if asked.
func (v *logViewer) tick() {
	if v.follow.Checked && len(v.lines) > 0 && v.list.GetScrollOffset() < v.lastBottom-1 {
		v.follow.SetChecked(false)
	}
	if v.pull() && v.follow.Checked {
		v.scrollToBottom()
	}
}

// pull fetches new lines; it reports whether anything changed.
func (v *logViewer) pull() bool {
	lines, next, gen := v.u.app.logs.Since(v.next)
	changed := false
	if gen != v.gen {
		v.gen = gen
		v.lines = nil
		changed = true
	}
	if len(lines) > 0 {
		v.lines = append(v.lines, lines...)
		changed = true
	}
	v.next = next
	if changed {
		// Mirror the sink's byte bound loosely by line count so the
		// viewer can't grow without limit if the sink evicts.
		if n, _ := v.u.app.logs.Stats(); len(v.lines) > n {
			v.lines = v.lines[len(v.lines)-n:]
		}
		v.list.Refresh()
		v.updateCount()
	}
	return changed
}

func (v *logViewer) scrollToBottom() {
	v.list.ScrollToBottom()
	v.lastBottom = v.list.GetScrollOffset()
}

func (v *logViewer) updateCount() {
	n, b := v.u.app.logs.Stats()
	v.count.SetText(fmt.Sprintf("%d lines, %.1f MB (max 50 MB)", n, float64(b)/(1<<20)))
}
