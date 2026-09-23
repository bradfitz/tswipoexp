package main

import (
	"fmt"
	"strings"
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

	// Selection: a set of row indexes plus the anchor for Shift-click
	// ranges. Indexes refer to v.lines and are dropped whenever the
	// lines shift.
	selected map[int]bool
	anchor   int

	// lastBottom is the scroll offset right after the last time we
	// scrolled to the bottom. If the list is later found scrolled
	// above it, the user scrolled up, and Follow turns itself off.
	lastBottom float32
	shown      bool // the window has been shown, so the list can scroll
	stop       chan struct{}
}

// showLogViewer opens the log window, or brings it forward.
func (u *UI) showLogViewer() {
	if u.logs != nil {
		u.logs.win.Show()
		u.logs.win.RequestFocus()
		return
	}
	v := &logViewer{u: u, stop: make(chan struct{}), selected: map[int]bool{}, anchor: -1}
	v.win = u.app.fy.NewWindow("tswipoexp logs")
	v.win.SetIcon(appIcon)

	v.list = widget.NewList(
		func() int { return len(v.lines) },
		func() fyne.CanvasObject { return newLogRow(v) },
		func(i widget.ListItemID, o fyne.CanvasObject) {
			if i < len(v.lines) {
				o.(*logRow).set(i, v.lines[i], v.selected[i])
			}
		},
	)
	u.reg("logs.list", v.list)

	v.follow = widget.NewCheck("Follow", func(on bool) {
		if on {
			// A selection sitting mid-stream while the view jumps
			// to the end would be misleading.
			v.clearSelection()
			v.scrollToBottom()
		}
	})
	// Set the initial state directly: SetChecked would fire the
	// callback and scroll a list that has no renderer yet, which
	// panics inside Fyne.
	v.follow.Checked = true
	u.reg("logs.follow", v.follow)

	clear := widget.NewButton("Clear", func() {
		u.app.logs.Clear()
		v.lines = nil
		v.clearSelection()
		v.list.Refresh()
		v.updateCount()
	})
	u.reg("logs.clear", clear)
	v.count = widget.NewLabel("")
	u.reg("logs.count", v.count)

	top := container.NewBorder(nil, nil, container.NewHBox(v.follow, clear), v.count, widget.NewLabel(""))
	v.win.SetContent(container.NewBorder(top, nil, nil, nil, v.list))
	v.win.Resize(fyne.NewSize(1000, 600))
	v.win.Canvas().AddShortcut(&fyne.ShortcutCopy{}, func(fyne.Shortcut) { v.copySelection() })
	v.win.SetOnClosed(func() {
		close(v.stop)
		u.unregDialogWidgets("logs.")
		u.logs = nil
	})
	u.logs = v
	v.pull()
	v.win.Show()
	v.shown = true
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
		v.clearSelection()
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
			v.clearSelection() // indexes shifted
		}
		v.list.Refresh()
		v.updateCount()
	}
	return changed
}

func (v *logViewer) scrollToBottom() {
	if !v.shown {
		return
	}
	v.list.ScrollToBottom()
	v.lastBottom = v.list.GetScrollOffset()
}

func (v *logViewer) updateCount() {
	n, b := v.u.app.logs.Stats()
	v.count.SetText(fmt.Sprintf("%d lines, %.1f MB (max 50 MB)", n, float64(b)/(1<<20)))
}

// selectOne makes index the only selected row and turns Follow off,
// since a moving list under a selection is confusing.
func (v *logViewer) selectOne(index int) {
	v.stopFollowing()
	for k := range v.selected {
		delete(v.selected, k)
	}
	v.selected[index] = true
	v.anchor = index
	v.list.Refresh()
}

// toggleSelect adds or removes one row from the selection.
func (v *logViewer) toggleSelect(index int) {
	v.stopFollowing()
	if v.selected[index] {
		delete(v.selected, index)
	} else {
		v.selected[index] = true
		v.anchor = index
	}
	v.list.Refresh()
}

// selectRange selects every row between the anchor and index.
func (v *logViewer) selectRange(index int) {
	if v.anchor < 0 {
		v.selectOne(index)
		return
	}
	v.stopFollowing()
	lo, hi := v.anchor, index
	if lo > hi {
		lo, hi = hi, lo
	}
	for k := range v.selected {
		delete(v.selected, k)
	}
	for i := lo; i <= hi && i < len(v.lines); i++ {
		v.selected[i] = true
	}
	v.list.Refresh()
}

func (v *logViewer) clearSelection() {
	if len(v.selected) == 0 && v.anchor < 0 {
		return
	}
	for k := range v.selected {
		delete(v.selected, k)
	}
	v.anchor = -1
	v.list.Refresh()
}

// stopFollowing unchecks Follow without triggering its callback.
func (v *logViewer) stopFollowing() {
	if v.follow.Checked {
		v.follow.Checked = false
		v.follow.Refresh()
	}
}

// copySelection puts the selected rows, in order, on the clipboard.
func (v *logViewer) copySelection() {
	if len(v.selected) == 0 {
		return
	}
	var b strings.Builder
	for i := range v.lines {
		if v.selected[i] {
			b.WriteString(v.lines[i])
			b.WriteString("\n")
		}
	}
	v.u.app.fy.Clipboard().SetContent(b.String())
}

// copyAll puts every buffered row on the clipboard.
func (v *logViewer) copyAll() {
	v.u.app.fy.Clipboard().SetContent(strings.Join(v.lines, "\n") + "\n")
}
