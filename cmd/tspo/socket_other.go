//go:build !windows

package main

import (
	"os"
	"path/filepath"
)

// localAPISocket returns the Unix socket in the state directory next
// to this executable, matching what the root package listens on when
// developing on non-Windows platforms.
func localAPISocket() string {
	exe, err := os.Executable()
	if err != nil {
		return "tswipoexp-state/tswipoexp.sock"
	}
	return filepath.Join(filepath.Dir(exe), "tswipoexp-state", "tswipoexp.sock")
}
