//go:build !windows

package main

// showFatal is a no-op off Windows, where stderr is visible.
func showFatal(msg string) {}
