//go:build !windows

package main

import (
	"net"
	"path/filepath"

	"tailscale.com/safesocket"
)

// listenLocalAPISocket listens on a Unix socket in the state root
// directory. This exists so tswipoexp can be developed on Linux; on
// Windows it's a named pipe.
func listenLocalAPISocket(stateRoot string) (net.Listener, error) {
	return safesocket.Listen(filepath.Join(stateRoot, "tswipoexp.sock"))
}
