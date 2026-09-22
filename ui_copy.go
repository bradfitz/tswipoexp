package main

import (
	"image/color"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// copyText is a piece of link-styled text that copies itself to the
// clipboard when clicked and shows a hint while hovered. Fyne has no
// tooltips, so the hint is a label the row owner places next to it.
type copyText struct {
	widget.BaseWidget
	text  *canvas.Text
	value string
	hint  *widget.Label // shared hint label for the row
	clip  fyne.Clipboard
	flash *time.Timer
}

func newCopyText(value string, hint *widget.Label, clip fyne.Clipboard) *copyText {
	c := &copyText{value: value, hint: hint, clip: clip}
	c.text = canvas.NewText(value, theme.Color(theme.ColorNameHyperlink))
	c.text.TextStyle = fyne.TextStyle{Monospace: true}
	c.ExtendBaseWidget(c)
	return c
}

// SetValue changes the text shown and copied.
func (c *copyText) SetValue(v string) { c.SetDisplayAndValue(v, v) }

// SetDisplayAndValue shows display but copies value, for text with
// annotations that shouldn't end up on the clipboard.
func (c *copyText) SetDisplayAndValue(display, value string) {
	if c.value == value && c.text.Text == display {
		return
	}
	c.value = value
	c.text.Text = display
	c.text.Refresh()
	c.Refresh()
}

func (c *copyText) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(c.text)
}

func (c *copyText) MinSize() fyne.Size {
	return c.text.MinSize().Add(fyne.NewSize(0, theme.Padding()))
}

// Tapped copies the value and flashes "Copied".
func (c *copyText) Tapped(*fyne.PointEvent) {
	if c.clip != nil {
		c.clip.SetContent(c.value)
	}
	c.hint.SetText("Copied " + c.value)
	if c.flash != nil {
		c.flash.Stop()
	}
	c.flash = time.AfterFunc(1500*time.Millisecond, func() {
		fyne.Do(func() {
			if c.hint.Text == "Copied "+c.value {
				c.hint.SetText("")
			}
		})
	})
}

// Cursor gives the hand pointer on hover.
func (c *copyText) Cursor() desktop.Cursor { return desktop.PointerCursor }

func (c *copyText) MouseIn(*desktop.MouseEvent) {
	if c.hint.Text == "" {
		c.hint.SetText("Click to copy")
	}
}

func (c *copyText) MouseMoved(*desktop.MouseEvent) {}

func (c *copyText) MouseOut() {
	if c.hint.Text == "Click to copy" {
		c.hint.SetText("")
	}
}

// commaText is the plain separator between copyable items.
func commaText() *canvas.Text {
	t := canvas.NewText(", ", color.Transparent)
	t.Color = theme.Color(theme.ColorNameForeground)
	return t
}
