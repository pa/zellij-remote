package proxy

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const origin = "https://zellij-mac.example.ts.net"

// upstream stands in for zellij web: it echoes request details on plain
// requests and, on a WebSocket upgrade, echoes raw bytes back.
func upstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "websocket" {
			conn, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
			rw.Flush()
			io.Copy(conn, rw)
			return
		}
		w.Header().Set("Set-Cookie", "session_token=abc; HttpOnly; SameSite=Strict; Path=/")
		io.WriteString(w, strings.Join([]string{
			r.Host, r.Header.Get("X-Forwarded-Proto"), r.Header.Get("X-Forwarded-Host"), r.Header.Get("X-Forwarded-For"),
		}, "|"))
	}))
}

func newProxy(t *testing.T, up *httptest.Server) *httptest.Server {
	t.Helper()
	u, _ := url.Parse(up.URL)
	return httptest.NewTLSServer(New(u, origin, nil))
}

func TestForwardsAndKeepsHost(t *testing.T) {
	up := upstream(t)
	defer up.Close()
	px := newProxy(t, up)
	defer px.Close()

	req, _ := http.NewRequest("GET", px.URL+"/", nil)
	req.Host = "zellij-mac.example.ts.net"
	req.Header.Set("X-Forwarded-For", "6.6.6.6")
	res, err := px.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	got := strings.Split(string(b), "|")
	if got[0] != "zellij-mac.example.ts.net" {
		t.Errorf("Host = %q, want the browser's", got[0])
	}
	if got[1] != "https" || got[2] != "zellij-mac.example.ts.net" {
		t.Errorf("X-Forwarded-Proto/Host = %q/%q", got[1], got[2])
	}
	if strings.Contains(got[3], "6.6.6.6") || got[3] != "127.0.0.1" {
		t.Errorf("X-Forwarded-For = %q, want only this hop", got[3])
	}
	if c := res.Header.Get("Set-Cookie"); !strings.HasPrefix(c, "session_token=abc") {
		t.Errorf("Set-Cookie not passed through: %q", c)
	}
}

func TestOriginCheck(t *testing.T) {
	up := upstream(t)
	defer up.Close()
	px := newProxy(t, up)
	defer px.Close()

	for _, tc := range []struct {
		origin string
		want   int
	}{
		{"", http.StatusOK},
		{origin, http.StatusOK},
		{"https://other-device.example.ts.net", http.StatusForbidden},
		{"http://zellij-mac.example.ts.net", http.StatusForbidden},
		{"null", http.StatusForbidden},
	} {
		req, _ := http.NewRequest("POST", px.URL+"/command/login", strings.NewReader("{}"))
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		res, err := px.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != tc.want {
			t.Errorf("Origin %q: status %d, want %d", tc.origin, res.StatusCode, tc.want)
		}
	}
}

func TestWebSocketPassesThrough(t *testing.T) {
	up := upstream(t)
	defer up.Close()
	u, _ := url.Parse(up.URL)
	px := httptest.NewServer(New(u, origin, nil)) // plain: easier to speak raw HTTP to
	defer px.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(px.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(conn, "GET /ws/terminal HTTP/1.1\r\nHost: zellij-mac.example.ts.net\r\n"+
		"Origin: "+origin+"\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n"+
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n")
	br := bufio.NewReader(conn)
	res, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status %d, want 101", res.StatusCode)
	}
	io.WriteString(conn, "ping")
	buf := make([]byte, 4)
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo = %q, %v", buf, err)
	}
}

func TestUpstreamDown(t *testing.T) {
	u, _ := url.Parse("http://127.0.0.1:1")
	px := httptest.NewServer(New(u, origin, nil))
	defer px.Close()
	res, err := http.Get(px.URL)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadGateway {
		t.Errorf("status %d, want 502", res.StatusCode)
	}
}
