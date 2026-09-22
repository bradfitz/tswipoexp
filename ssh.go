package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	ssh "github.com/tailscale/gliderssh"
	gossh "golang.org/x/crypto/ssh"
	"tailscale.com/client/local"
	"tailscale.com/types/logger"
)

// sshHostKeyFile is the host key file inside a profile directory. It
// travels with the profile so the host key stays stable across
// computers, which is what a roaming node should do.
const sshHostKeyFile = "ssh_host_ed25519_key"

// sshDebugf logs SSH session internals. It's a package variable so
// the platform session runners, which have no server handle, can
// use it; the App sets it at startup.
var sshDebugf = func(format string, args ...any) {}

// sshServer accepts SSH connections on the tailnet and runs shells
// as the user running tswipoexp. Tailscale's own SSH server doesn't
// build on Windows, so this follows tailcat's approach instead: the
// WireGuard tunnel identifies the peer, WhoIs maps it to a tailnet
// user, and only peers owned by the same login as this node get in.
type sshServer struct {
	logf   logger.Logf
	lc     *local.Client
	dir    string // profile directory, for the host key
	selfID func() (login string, ok bool)
	mode   func() string // current sshMode, read per connection

	ln     net.Listener
	closed atomic.Bool

	hostKeyOnce sync.Once
	hostKey     gossh.Signer
	hostKeyErr  error
}

// serve accepts connections until the listener is closed.
func (s *sshServer) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			if !s.closed.Load() {
				s.logf("ssh: accept: %v", err)
			}
			return
		}
		go s.handleConn(c)
	}
}

func (s *sshServer) close() {
	s.closed.Store(true)
	s.ln.Close()
}

// handleConn authorizes the peer and, if allowed, serves the SSH
// connection.
func (s *sshServer) handleConn(c net.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	who, err := s.lc.WhoIs(ctx, c.RemoteAddr().String())
	cancel()
	if err != nil {
		s.logf("ssh: rejecting %v: whois: %v", c.RemoteAddr(), err)
		c.Close()
		return
	}
	peerLogin := ""
	if who.UserProfile != nil {
		peerLogin = who.UserProfile.LoginName
	}
	switch mode := s.mode(); mode {
	case sshAllUsers:
		// Reachability is the tailnet ACL's decision; anyone who
		// can open the connection is let in.
	case sshSameUser:
		selfLogin, ok := s.selfID()
		if !ok || peerLogin == "" || peerLogin != selfLogin {
			s.logf("ssh: rejecting %v (%s, user %q): not the same tailnet user as this node (%q)", c.RemoteAddr(), who.Node.Name, peerLogin, selfLogin)
			c.Close()
			return
		}
	default:
		s.logf("ssh: rejecting %v: SSH is %s", c.RemoteAddr(), mode)
		c.Close()
		return
	}
	hk, err := s.getHostKey()
	if err != nil {
		s.logf("ssh: host key: %v", err)
		c.Close()
		return
	}
	s.logf("ssh: session from %s (%s)", who.Node.Name, peerLogin)
	srv := &ssh.Server{
		Handler:             s.sessionHandler,
		NoClientAuthHandler: func(ctx ssh.Context) error { return nil },
		ChannelHandlers:     map[string]ssh.ChannelHandler{"session": ssh.DefaultSessionHandler},
		RequestHandlers:     map[string]ssh.RequestHandler{},
	}
	srv.AddHostKey(hk)
	srv.HandleConn(c)
}

// sessionHandler runs one shell or command session.
func (s *sshServer) sessionHandler(sess ssh.Session) {
	u, err := user.Current()
	if err != nil {
		fmt.Fprintf(sess.Stderr(), "current user: %v\r\n", err)
		sess.Exit(1)
		return
	}
	cmd := newSessionCommand(u, sess.RawCommand())
	for _, env := range sess.Environ() {
		if acceptEnvPair(env) {
			cmd.Env = append(cmd.Env, env)
		}
	}
	ptyReq, winCh, isPTY := sess.Pty()
	if isPTY {
		sess.DisablePTYEmulation()
		runWithPTY(sess, cmd, ptyReq, winCh)
		return
	}
	runWithPipes(sess, cmd)
}

// getHostKey loads or generates the profile's host key.
func (s *sshServer) getHostKey() (gossh.Signer, error) {
	s.hostKeyOnce.Do(func() {
		path := filepath.Join(s.dir, sshHostKeyFile)
		pemData, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			pemData, err = generateHostKey(path)
		}
		if err != nil {
			s.hostKeyErr = err
			return
		}
		s.hostKey, s.hostKeyErr = gossh.ParsePrivateKey(pemData)
	})
	return s.hostKey, s.hostKeyErr
}

func generateHostKey(path string) ([]byte, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}
	pemData := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := writeFileAtomic(path, pemData); err != nil {
		return nil, err
	}
	return pemData, nil
}

// acceptEnvPair reports whether a client-supplied environment
// variable is passed to the session, with OpenSSH's default AcceptEnv
// set.
func acceptEnvPair(kv string) bool {
	k, _, ok := strings.Cut(kv, "=")
	if !ok {
		return false
	}
	return k == "TERM" || k == "LANG" || strings.HasPrefix(k, "LC_")
}

// runWithPipes runs cmd with its stdio connected to the session
// through pipes, for sessions without a PTY.
func runWithPipes(sess ssh.Session, cmd *exec.Cmd) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		fmt.Fprintf(sess.Stderr(), "stdin pipe: %v\r\n", err)
		sess.Exit(1)
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintf(sess.Stderr(), "stdout pipe: %v\r\n", err)
		sess.Exit(1)
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		fmt.Fprintf(sess.Stderr(), "stderr pipe: %v\r\n", err)
		sess.Exit(1)
		return
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(sess.Stderr(), "start: %v\r\n", err)
		sess.Exit(1)
		return
	}
	go func() {
		defer stdin.Close()
		io.Copy(stdin, sess)
	}()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); io.Copy(sess, stdout) }()
	go func() { defer wg.Done(); io.Copy(sess.Stderr(), stderr) }()
	// Drain output before Wait, which closes the pipes and would
	// otherwise race with the copies for fast-exiting commands.
	wg.Wait()
	err = cmd.Wait()
	sess.Exit(exitCode(err))
}

// exitCode extracts a process exit code from cmd.Wait's error.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 1
}
