// The devtarget command is a tsnet node that serves a small HTTP
// echo server on the tailnet. It exists as something for tswipoexp
// to fetch through its proxy during development.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"tailscale.com/tsnet"
)

func main() {
	hostname := flag.String("hostname", "tswipoexp-target", "tailnet hostname")
	dir := flag.String("dir", "", "state directory; defaults to ~/.cache/tswipoexp-devtarget")
	flag.Parse()

	if *dir == "" {
		home, _ := os.UserHomeDir()
		*dir = filepath.Join(home, ".cache", "tswipoexp-devtarget")
	}
	s := &tsnet.Server{
		Dir:      *dir,
		Hostname: *hostname,
		Logf:     func(string, ...any) {},
	}
	defer s.Close()
	ln, err := s.Listen("tcp", ":80")
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	st, err := s.Up(ctx)
	cancel()
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("devtarget up as %s %v", st.Self.DNSName, st.TailscaleIPs)
	log.Fatal(http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who, _ := s.LocalClient()
		fmt.Fprintf(w, "hello from devtarget\n")
		fmt.Fprintf(w, "time: %s\n", time.Now().UTC().Format(time.RFC3339))
		fmt.Fprintf(w, "method: %s\nurl: %s\nhost: %s\nremote: %s\n", r.Method, r.URL, r.Host, r.RemoteAddr)
		if who != nil {
			if res, err := who.WhoIs(r.Context(), r.RemoteAddr); err == nil {
				fmt.Fprintf(w, "peer: %s (%s)\n", res.Node.Name, res.UserProfile.LoginName)
			}
		}
	})))
}
