package main

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// redirectStderr sends the process's stderr, which is where Go writes
// panic traces, to crash.log in the state directory. The GUI binary
// has no console, so without this a panic leaves no trace at all. The
// file is truncated at each start; a crash from the previous run is
// still readable until then.
func redirectStderr(stateRoot string) {
	path := filepath.Join(stateRoot, "crash.log")
	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		os.Rename(path, filepath.Join(stateRoot, "crash.prev.log"))
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return
	}
	if err := windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(f.Fd())); err != nil {
		f.Close()
		return
	}
	os.Stderr = f
}
