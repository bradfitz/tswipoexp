package main

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// logRow is one line in the log viewer. It draws its own selection
// highlight and handles the mouse itself so the viewer can offer
// multi-row selection, which Fyne's List doesn't: click selects,
// Shift-click extends from the anchor, Ctrl-click toggles, and
// right-click opens a Copy menu.
type logRow struct {
	widget.BaseWidget
	v     *logViewer
	index int
	text  *canvas.Text
	bg    *canvas.Rectangle
}

func newLogRow(v *logViewer) *logRow {
	r := &logRow{v: v, index: -1}
	r.text = canvas.NewText("", theme.Color(theme.ColorNameForeground))
	r.text.TextStyle = fyne.TextStyle{Monospace: true}
	r.bg = canvas.NewRectangle(color.Transparent)
	r.ExtendBaseWidget(r)
	return r
}

// set updates the row for a list item.
func (r *logRow) set(index int, text string, selected bool) {
	r.index = index
	if r.text.Text != text {
		r.text.Text = text
		r.text.Refresh()
	}
	c := color.Color(color.Transparent)
	if selected {
		c = theme.Color(theme.ColorNameSelection)
	}
	if r.bg.FillColor != c {
		r.bg.FillColor = c
		r.bg.Refresh()
	}
}

func (r *logRow) CreateRenderer() fyne.WidgetRenderer {
	return &logRowRenderer{r: r}
}

func (r *logRow) MinSize() fyne.Size {
	return r.text.MinSize().Add(fyne.NewSize(2*theme.Padding(), theme.Padding()))
}

// Tapped exists so the List's own item wrapper doesn't take the tap
// for its single-selection; MouseDown does the work since it carries
// the modifier keys.
func (r *logRow) Tapped(*fyne.PointEvent) {}

func (r *logRow) MouseDown(e *desktop.MouseEvent) {
	if r.index < 0 || e.Button != desktop.MouseButtonPrimary {
		return
	}
	switch {
	case e.Modifier&fyne.KeyModifierShift != 0:
		r.v.selectRange(r.index)
	case e.Modifier&fyne.KeyModifierControl != 0 || e.Modifier&fyne.KeyModifierSuper != 0:
		r.v.toggleSelect(r.index)
	default:
		r.v.selectOne(r.index)
	}
}

func (r *logRow) MouseUp(*desktop.MouseEvent) {}

// TappedSecondary shows the context menu. A right-click on an
// unselected row selects it first, like most list controls.
func (r *logRow) TappedSecondary(e *fyne.PointEvent) {
	if r.index < 0 {
		return
	}
	if !r.v.selected[r.index] {
		r.v.selectOne(r.index)
	}
	menu := fyne.NewMenu("",
		fyne.NewMenuItem("Copy", r.v.copySelection),
		fyne.NewMenuItem("Copy all", r.v.copyAll),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Clear selection", r.v.clearSelection),
	)
	widget.ShowPopUpMenuAtPosition(menu, fyne.CurrentApp().Driver().CanvasForObject(r), e.AbsolutePosition)
}

type logRowRenderer struct {
	r *logRow
}

func (rr *logRowRenderer) Layout(size fyne.Size) {
	rr.r.bg.Resize(size)
	rr.r.text.Move(fyne.NewPos(theme.Padding(), theme.Padding()/2))
	rr.r.text.Resize(fyne.NewSize(size.Width-2*theme.Padding(), size.Height-theme.Padding()))
}

func (rr *logRowRenderer) MinSize() fyne.Size { return rr.r.MinSize() }
func (rr *logRowRenderer) Refresh()           { rr.r.bg.Refresh(); rr.r.text.Refresh() }
func (rr *logRowRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{rr.r.bg, rr.r.text}
}
func (rr *logRowRenderer) Destroy() {}
