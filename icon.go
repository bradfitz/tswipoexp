package main

import (
	_ "embed"

	"fyne.io/fyne/v2"
)

//go:embed icon.png
var iconPNG []byte

// appIcon is the window and tray icon.
var appIcon = fyne.NewStaticResource("icon.png", iconPNG)
