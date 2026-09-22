// The devssh command is a tsnet node that opens an SSH session to a
// tailnet host and runs a command, for testing tswipoexp's SSH server
// from Linux during development. It authenticates only by tunnel
// identity, which is all tswipoexp's server checks.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"

	gossh "golang.org/x/crypto/ssh"
	"tailscale.com/tsnet"
)

func main() {
	hostname := flag.String("hostname", "tswipoexp-devssh", "tailnet hostname of this node")
	dir := flag.String("dir", "", "state directory; defaults to ~/.cache/tswipoexp-devssh")
	target := flag.String("target", "tswipoexp", "host to SSH to")
	pty := flag.Bool("pty", false, "request a PTY, exercising the server's ConPTY path")
	flag.Parse()
	if flag.NArg() == 0 {
		log.Fatal("usage: devssh [flags] command...")
	}
	cmd := ""
	for i, a := range flag.Args() {
		if i > 0 {
			cmd += " "
		}
		cmd += a
	}

	if *dir == "" {
		home, _ := os.UserHomeDir()
		*dir = filepath.Join(home, ".cache", "tswipoexp-devssh")
	}
	s := &tsnet.Server{Dir: *dir, Hostname: *hostname, Logf: func(string, ...any) {}}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := s.Up(ctx); err != nil {
		log.Fatal(err)
	}
	// A freshly started tsnet node can take a while to get a path
	// to the peer, and tsnet's Dial has no timeout of its own, so
	// dial with a bounded timeout and retry.
	var conn net.Conn
	for attempt := 1; ; attempt++ {
		dctx, dcancel := context.WithTimeout(ctx, 15*time.Second)
		var err error
		conn, err = s.Dial(dctx, "tcp", *target+":22")
		dcancel()
		if err == nil {
			break
		}
		log.Printf("dial attempt %d: %v", attempt, err)
		if attempt == 4 {
			log.Fatalf("dial: giving up")
		}
	}
	cfg := &gossh.ClientConfig{
		User:            "tswipoexp",
		Auth:            []gossh.AuthMethod{},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}
	cc, chans, reqs, err := gossh.NewClientConn(conn, *target, cfg)
	if err != nil {
		log.Fatalf("ssh handshake: %v", err)
	}
	client := gossh.NewClient(cc, chans, reqs)
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		log.Fatalf("session: %v", err)
	}
	defer sess.Close()
	sess.Stdout = os.Stdout
	sess.Stderr = os.Stderr
	if *pty {
		if err := sess.RequestPty("xterm-256color", 24, 80, gossh.TerminalModes{}); err != nil {
			log.Fatalf("request pty: %v", err)
		}
	}
	if err := sess.Run(cmd); err != nil {
		fmt.Fprintf(os.Stderr, "remote command: %v\n", err)
		os.Exit(1)
	}
}
