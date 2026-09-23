//go:build !windows

package main

// redirectStderr is a no-op off Windows, where stderr is visible.
func redirectStderr(stateRoot string) {}
