package proxy

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// TestRealZellij drives a real zellij web through the proxy the way a
// browser would: log in with a token, boot a session, open the terminal
// WebSocket, and read the terminal's first bytes. It needs a running
// zellij web and a login token, so it only runs when asked:
//
//	zellij web --port 18082 &
//	ZELLIJ_IT_URL=http://127.0.0.1:18082 ZELLIJ_IT_TOKEN=<token> go test ./internal/proxy -run RealZellij -v
func TestRealZellij(t *testing.T) {
	upURL, token := os.Getenv("ZELLIJ_IT_URL"), os.Getenv("ZELLIJ_IT_TOKEN")
	if upURL == "" || token == "" {
		t.Skip("set ZELLIJ_IT_URL and ZELLIJ_IT_TOKEN to run against a real zellij web")
	}
	const host = "zellij-it.example.ts.net"
	const self = "https://" + host
	target, _ := url.Parse(upURL)
	px := httptest.NewTLSServer(New(Config{Target: target, Origin: self,
		Authorize: func(*http.Request) (string, error) { return "it@example.com", nil }}))
	defer px.Close()
	addr := strings.TrimPrefix(px.URL, "https://")

	// A client that dials the test server but speaks to it as the tailnet name.
	cl := px.Client()
	cl.Timeout = 10 * time.Second
	do := func(method, path, body string, cookie string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, px.URL+path, strings.NewReader(body))
		req.Host = host
		req.Header.Set("Origin", self)
		req.Header.Set("Content-Type", "application/json")
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		res, err := cl.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	res := do("POST", "/command/login", `{"auth_token":"`+token+`","remember_me":false}`, "")
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("login: %d", res.StatusCode)
	}
	var cookie string
	for _, c := range res.Cookies() {
		if c.Name == "session_token" {
			cookie = c.Name + "=" + c.Value
		}
	}
	if cookie == "" {
		t.Fatal("login set no session_token cookie")
	}

	session := "zellij-remote-it"
	res = do("POST", "/session?session="+session, "", cookie)
	var boot struct {
		WebClientID string `json:"web_client_id"`
	}
	json.NewDecoder(res.Body).Decode(&boot)
	res.Body.Close()
	if boot.WebClientID == "" {
		t.Fatalf("session boot: %d, no web_client_id", res.StatusCode)
	}

	ws := func(path, origin string) (*http.Response, net.Conn, *bufio.Reader) {
		t.Helper()
		conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
		if err != nil {
			t.Fatal(err)
		}
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		io.WriteString(conn, "GET "+path+" HTTP/1.1\r\nHost: "+host+"\r\nOrigin: "+origin+
			"\r\nCookie: "+cookie+"\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n"+
			"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n")
		br := bufio.NewReader(conn)
		res, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatal(err)
		}
		return res, conn, br
	}

	// Another tailnet site must not get a terminal, cookie or not.
	res, conn, _ := ws("/ws/control?web_client_id="+boot.WebClientID, "https://evil.example.ts.net")
	conn.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin WebSocket: %d, want 403", res.StatusCode)
	}

	res, conn, br := ws("/ws/terminal/"+session+"?web_client_id="+boot.WebClientID+"&rows=24&cols=80", self)
	defer conn.Close()
	if res.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("terminal WebSocket: %d, want 101", res.StatusCode)
	}
	// The first server frame: 0x81 text or 0x82 binary, unmasked.
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(br, hdr); err != nil {
		t.Fatalf("no terminal output: %v", err)
	}
	if op := hdr[0] & 0x0f; op != 1 && op != 2 {
		t.Fatalf("first frame opcode %d, want text or binary", op)
	}
	t.Logf("terminal WebSocket open through the proxy; first frame opcode %d, len byte %d", hdr[0]&0x0f, hdr[1]&0x7f)
}
