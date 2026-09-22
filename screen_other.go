//go:build !windows

package main

// screenSizePixels is unknown off Windows; the window keeps its
// requested size.
func screenSizePixels() (w, h float32, ok bool) { return 0, 0, false }

func windowSizePixels(title string) (w, h float32, ok bool) { return 0, 0, false }
