package main

import (
	"errors"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync/atomic"

	"tailscale.com/types/logger"
)

// ErrAlreadyRunning is returned when another tswipoexp already holds
// the LocalAPI socket.
var ErrAlreadyRunning = errors.New("another instance of tswipoexp is already running")

// localAPIBridge serves tsnet's LocalAPI on the platform's
// tailscaled-style socket (a named pipe on Windows) so that the real
// tailscale CLI, via tspo.exe, can talk to it. tsnet only exposes the
// LocalAPI over a loopback TCP port guarded by a credential and a
// header, so this is a small reverse proxy that adds both.
//
// The bridge outlives any one Backend: profile switches just retarget
// it. Its listener also acts as the single-instance lock, since a
// second copy of tswipoexp can't create the same named pipe.
type localAPIBridge struct {
	ln     net.Listener
	logf   logger.Logf
	target atomic.Pointer[bridgeTarget]
}

type bridgeTarget struct {
	addr string // loopback host:port of tsnet's LocalAPI
	cred string // basic auth password it requires
}

// newLocalAPIBridge takes the socket and starts serving. Until
// SetTarget is called, requests get a 503.
func newLocalAPIBridge(stateRoot string, logf logger.Logf) (*localAPIBridge, error) {
	ln, err := listenLocalAPISocket(stateRoot)
	if err != nil {
		return nil, err
	}
	br := &localAPIBridge{ln: ln, logf: logger.WithPrefix(logf, "localapi bridge: ")}
	hs := &http.Server{
		Handler:  br,
		ErrorLog: log.New(logger.FuncWriter(br.logf), "", 0),
	}
	go func() {
		if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			br.logf("serve: %v", err)
		}
	}()
	br.logf("listening on %v", ln.Addr())
	return br, nil
}

// SetTarget points the bridge at a backend's LocalAPI. A nil backend
// detaches it.
func (br *localAPIBridge) SetTarget(b *Backend) error {
	if b == nil {
		br.target.Store(nil)
		return nil
	}
	addr, _, cred, err := b.ts.Loopback()
	if err != nil {
		return err
	}
	br.target.Store(&bridgeTarget{addr: addr, cred: cred})
	return nil
}

func (br *localAPIBridge) Close() error { return br.ln.Close() }

func (br *localAPIBridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	t := br.target.Load()
	if t == nil {
		http.Error(w, "tswipoexp has no active profile", http.StatusServiceUnavailable)
		return
	}
	target := &url.URL{Scheme: "http", Host: t.addr}
	rp := httputil.NewSingleHostReverseProxy(target)
	director := rp.Director
	rp.Director = func(r *http.Request) {
		director(r)
		r.Host = target.Host
		r.Header.Set("Sec-Tailscale", "localapi")
		r.SetBasicAuth("", t.cred)
	}
	rp.ErrorLog = log.New(logger.FuncWriter(br.logf), "", 0)
	rp.ServeHTTP(w, r)
}
