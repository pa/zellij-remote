// Package proxy forwards HTTPS requests from the tailnet to zellij web on
// localhost.
package proxy

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
)

// New returns a handler that forwards every request to target, which is
// zellij web on 127.0.0.1. WebSocket upgrades (the terminal itself) pass
// through: httputil.ReverseProxy hijacks the connection and copies both ways.
//
// origin is this device's own address, like https://zellij-mac.tailnet.ts.net.
// A request whose Origin header names anything else gets a 403. zellij 0.45
// doesn't check Origin on its WebSockets, and its session cookie is only
// SameSite=Strict. Every *.<tailnet>.ts.net host counts as the same site, so
// without this a page served by any other device on the tailnet could open a
// terminal with the browser's cookie. Requests without an Origin (curl, not
// a browser) pass; browsers always send one on WebSockets and on POSTs.
//
// The browser's Host is kept. X-Forwarded-For/-Host/-Proto are set from this
// hop only; whatever the client sent in them is dropped.
func New(target *url.URL, origin string, logger *log.Logger) http.Handler {
	rp := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Host = r.In.Host
			r.SetXForwarded()
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if logger != nil {
				logger.Printf("zellij web unreachable at %s: %v", target, err)
			}
			http.Error(w, "zellij web isn't answering on "+target.Host+
				". Is it running? (zellij-remote status)", http.StatusBadGateway)
		},
		ErrorLog: logger,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" && o != origin {
			if logger != nil {
				logger.Printf("refused %s %s from %s: Origin %q isn't %s", r.Method, r.URL.Path, r.RemoteAddr, o, origin)
			}
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
			return
		}
		rp.ServeHTTP(w, r)
	})
}
