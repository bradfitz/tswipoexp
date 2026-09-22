// The tswipoexp command is a portable Windows Tailscale client: a
// userspace tailscaled with a GUI that keeps its state next to the
// binary.
package main

import (
	"os"
	"runtime"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

func main() {
	a := app.New()
	w := a.NewWindow("tswipoexp")

	exe, _ := os.Executable()
	host, _ := os.Hostname()
	w.SetContent(container.NewVBox(
		widget.NewLabel("Hello from tswipoexp"),
		widget.NewLabel("Go "+runtime.Version()+" "+runtime.GOOS+"/"+runtime.GOARCH),
		widget.NewLabel("Host: "+host),
		widget.NewLabel("Exe: "+exe),
		widget.NewButton("Quit", a.Quit),
	))
	w.Resize(fyne.NewSize(600, 300))
	w.ShowAndRun()
}
