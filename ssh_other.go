//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"

	ssh "github.com/tailscale/gliderssh"
)

// newSessionCommand returns an unstarted login shell command. This
// exists for developing on Linux; it has no PTY support.
func newSessionCommand(u *user.User, rawCmd string) *exec.Cmd {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	var cmd *exec.Cmd
	if rawCmd == "" {
		cmd = exec.Command(shell, "-l")
	} else {
		cmd = exec.Command(shell, "-c", rawCmd)
	}
	cmd.Dir = u.HomeDir
	cmd.Env = os.Environ()
	return cmd
}

// runWithPTY falls back to pipes off Windows.
func runWithPTY(sess ssh.Session, cmd *exec.Cmd, ptyReq ssh.Pty, winCh <-chan ssh.Window) {
	fmt.Fprintf(sess.Stderr(), "tswipoexp: PTY sessions are only supported on Windows; running without a PTY\r\n")
	runWithPipes(sess, cmd)
}
