package main

import (
	"syscall"
	"unsafe"
)

var (
	procGetSystemMetrics = user32.NewProc("GetSystemMetrics")
	procFindWindowW      = user32.NewProc("FindWindowW")
	procGetWindowRect    = user32.NewProc("GetWindowRect")
)

const (
	smCXScreen = 0
	smCYScreen = 1
)

type winRect struct {
	left, top, right, bottom int32
}

// screenSizePixels returns the primary screen size in physical pixels
// (the process is per-monitor DPI aware, courtesy of GLFW), or false
// if it can't be determined.
func screenSizePixels() (w, h float32, ok bool) {
	cx, _, _ := procGetSystemMetrics.Call(smCXScreen)
	cy, _, _ := procGetSystemMetrics.Call(smCYScreen)
	if cx == 0 || cy == 0 {
		return 0, 0, false
	}
	return float32(cx), float32(cy), true
}

// windowSizePixels returns the on-screen size in physical pixels of
// the top-level window with the given title, including its frame, or
// false if there's no such window.
func windowSizePixels(title string) (w, h float32, ok bool) {
	t, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return 0, 0, false
	}
	hwnd, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(t)))
	if hwnd == 0 {
		return 0, 0, false
	}
	var r winRect
	if ret, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r))); ret == 0 {
		return 0, 0, false
	}
	return float32(r.right - r.left), float32(r.bottom - r.top), true
}
