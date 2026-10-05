// Package proxy forwards HTTPS requests from the tailnet to zellij web on
// localhost.
package proxy

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// Config says where to forward and who may come through.
type Config struct {
	// Target is zellij web on 127.0.0.1, plain HTTP.
	Target *url.URL
	// Origin is this device's own address, like
	// https://zellij-mac.tailnet.ts.net. A request whose Origin header names
	// anything else gets a 403.
	//
	// zellij 0.45 doesn't check Origin on its WebSockets, and its session
	// cookie is only SameSite=Strict. Every *.<tailnet>.ts.net host counts
	// as the same site, so without this a page served by any other device
	// on the tailnet could open a terminal with the browser's cookie.
	// Requests without an Origin (curl, not a browser) pass; browsers
	// always send one on WebSockets and on POSTs.
	Origin string
	// Authorize says who sent r, or returns an error to refuse it with a
	// 403. It runs before anything reaches zellij. Required.
	Authorize func(r *http.Request) (who string, err error)
	Logger    *log.Logger
}

// New returns a handler that checks each request and forwards it to
// c.Target. WebSocket upgrades (the terminal itself) pass through:
// httputil.ReverseProxy hijacks the connection and copies both ways.
//
// The browser's Host is kept. X-Forwarded-For/-Host/-Proto are set from this
// hop only; whatever the client sent in them is dropped. Responses get HSTS,
// and zellij's cookies get the Secure flag, since this is only ever HTTPS.
//
// Nothing here logs headers, cookies or query strings, so neither login
// tokens nor session cookies end up in the log.
func New(c Config) http.Handler {
	logf := func(f string, a ...any) {
		if c.Logger != nil {
			c.Logger.Printf(f, a...)
		}
	}
	rp := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(c.Target)
			r.Out.Host = r.In.Host
			r.SetXForwarded()
		},
		ModifyResponse: func(res *http.Response) error {
			res.Header.Set("Strict-Transport-Security", "max-age=31536000")
			if cookies := res.Header.Values("Set-Cookie"); len(cookies) > 0 {
				res.Header.Del("Set-Cookie")
				for _, ck := range cookies {
					res.Header.Add("Set-Cookie", secureCookie(ck))
				}
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logf("zellij web unreachable at %s: %v", c.Target.Host, err)
			http.Error(w, "zellij web isn't answering. Is zellij-remote running? (zellij-remote status)", http.StatusBadGateway)
		},
		ErrorLog: c.Logger,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c.Authorize == nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		who, err := c.Authorize(r)
		if err != nil {
			logf("refused %s %s from %s: %v", r.Method, r.URL.Path, r.RemoteAddr, err)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && o != c.Origin {
			logf("refused %s %s from %s (%s): Origin %q isn't %s", r.Method, r.URL.Path, r.RemoteAddr, who, o, c.Origin)
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/ws/terminal") {
			logf("terminal opened by %s from %s: %s", who, r.RemoteAddr, r.URL.Path)
		} else if r.URL.Path == "/command/login" {
			logf("login attempt by %s from %s", who, r.RemoteAddr)
		}
		rp.ServeHTTP(w, r)
	})
}

// secureCookie adds the Secure attribute to a Set-Cookie value that lacks it.
func secureCookie(v string) string {
	for _, attr := range strings.Split(v, ";") {
		if strings.EqualFold(strings.TrimSpace(attr), "secure") {
			return v
		}
	}
	return v + "; Secure"
}
