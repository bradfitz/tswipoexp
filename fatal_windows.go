package main

import (
	"syscall"
	"unsafe"
)

var procMessageBoxW = user32.NewProc("MessageBoxW")

const (
	mbOK        = 0x0
	mbIconError = 0x10
)

// showFatal shows a message box, since the GUI binary has no console
// for the message to go to.
func showFatal(msg string) {
	text, err := syscall.UTF16PtrFromString(msg)
	if err != nil {
		return
	}
	title, _ := syscall.UTF16PtrFromString("tswipoexp")
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), mbOK|mbIconError)
}
