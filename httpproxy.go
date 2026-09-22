package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
)

// httpProxyHandler returns an HTTP proxy handler (plain proxying and
// CONNECT tunneling) that dials through the given dialer, and serves
// the proxy auto-config script from pac at pacPath. It's adapted from
// cmd/tailscaled/proxy.go in the tailscale repo, which isn't
// importable.
func httpProxyHandler(dialer func(ctx context.Context, netw, addr string) (net.Conn, error), pac func() string) http.Handler {
	rp := &httputil.ReverseProxy{
		Director: func(r *http.Request) {}, // no change
		Transport: &http.Transport{
			DialContext: dialer,
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "CONNECT" {
			backURL := r.RequestURI
			if backURL == pacPath && pac != nil {
				w.Header().Set("Content-Type", "application/x-ns-proxy-autoconfig")
				w.Header().Set("Cache-Control", "no-cache, no-store")
				io.WriteString(w, pac())
				return
			}
			if strings.HasPrefix(backURL, "/") || backURL == "*" {
				http.Error(w, "bogus RequestURI; must be absolute URL or CONNECT", 400)
				return
			}
			rp.ServeHTTP(w, r)
			return
		}

		dst := r.RequestURI
		c, err := dialer(r.Context(), "tcp", dst)
		if err != nil {
			w.Header().Set("Tailscale-Connect-Error", err.Error())
			http.Error(w, err.Error(), 502)
			return
		}
		defer c.Close()

		cc, ccbuf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer cc.Close()

		io.WriteString(cc, "HTTP/1.1 200 OK\r\n\r\n")

		var clientSrc io.Reader = ccbuf
		if ccbuf.Reader.Buffered() == 0 {
			// With nothing buffered, read straight from the
			// connection and let the bufio pair get collected.
			clientSrc = cc
		}

		errc := make(chan error, 1)
		go func() {
			_, err := io.Copy(cc, c)
			errc <- err
		}()
		go func() {
			_, err := io.Copy(c, clientSrc)
			errc <- err
		}()
		<-errc
	})
}
