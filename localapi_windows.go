package main

import (
	"errors"
	"fmt"
	"net"

	"github.com/tailscale/go-winio"
	"golang.org/x/sys/windows"
)

// localAPIPipeName is the named pipe on which tswipoexp serves the
// LocalAPI for tspo.exe. It's a fixed name, so only one instance can
// run at a time, whichever profile it has loaded.
const localAPIPipeName = `\\.\pipe\tswipoexp`

// listenLocalAPISocket listens on the LocalAPI named pipe. The state
// root directory is unused on Windows.
//
// tailscaled's safesocket package isn't used here because its
// security descriptor names Builtin Administrators as the owner,
// which a non-admin user can't do. Instead the pipe is restricted to
// the current user (and SYSTEM), which is also tighter than
// tailscaled's all-users ACL: anything that can reach the pipe has
// full control of the node.
func listenLocalAPISocket(stateRoot string) (net.Listener, error) {
	sid, err := currentUserSID()
	if err != nil {
		return nil, err
	}
	ln, err := winio.ListenPipe(localAPIPipeName, &winio.PipeConfig{
		SecurityDescriptor: fmt.Sprintf("D:P(A;;GA;;;%s)(A;;GA;;;SY)", sid),
		InputBufferSize:    256 * 1024,
		OutputBufferSize:   256 * 1024,
	})
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_PIPE_BUSY) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("named pipe %s: %w", localAPIPipeName, err)
	}
	return ln, nil
}

// currentUserSID returns the SID string of the user running this
// process.
func currentUserSID() (string, error) {
	tok, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return "", err
	}
	defer tok.Close()
	u, err := tok.GetTokenUser()
	if err != nil {
		return "", err
	}
	return u.User.Sid.String(), nil
}
