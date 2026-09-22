// The devtarget command is a tsnet node that serves a small HTTP
// echo server on the tailnet. It exists as something for tswipoexp
// to fetch through its proxy during development.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"tailscale.com/ipn"
	"tailscale.com/net/tsaddr"
	"tailscale.com/tsnet"
)

func main() {
	hostname := flag.String("hostname", "tswipoexp-target", "tailnet hostname")
	dir := flag.String("dir", "", "state directory; defaults to ~/.cache/tswipoexp-devtarget")
	exitNode := flag.Bool("exit-node", false, "advertise as an exit node (needs approval in the admin console)")
	verbose := flag.Bool("verbose", false, "log tsnet internals")
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
	if *verbose {
		s.Logf = log.Printf
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
	if *exitNode {
		lc, err := s.LocalClient()
		if err != nil {
			log.Fatal(err)
		}
		_, err = lc.EditPrefs(context.Background(), &ipn.MaskedPrefs{
			Prefs:              ipn.Prefs{AdvertiseRoutes: tsaddr.ExitRoutes()},
			AdvertiseRoutesSet: true,
		})
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("advertising exit node routes")
		// tsnet resets TCP flows that no listener claims, so without
		// this a tsnet node can't forward exit node traffic. Forward
		// flows for non-local destinations to the real destination.
		s.RegisterFallbackTCPHandler(func(src, dst netip.AddrPort) (func(net.Conn), bool) {
			if tsaddr.IsTailscaleIP(dst.Addr()) {
				return nil, false
			}
			return func(c net.Conn) {
				defer c.Close()
				out, err := net.DialTimeout("tcp", dst.String(), 30*time.Second)
				if err != nil {
					log.Printf("exit forward %v -> %v: %v", src, dst, err)
					return
				}
				defer out.Close()
				log.Printf("exit forward %v -> %v", src, dst)
				done := make(chan struct{}, 2)
				go func() { io.Copy(out, c); done <- struct{}{} }()
				go func() { io.Copy(c, out); done <- struct{}{} }()
				<-done
			}, true
		})
	}
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
