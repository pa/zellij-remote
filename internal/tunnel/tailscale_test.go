package tunnel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostname(t *testing.T) {
	for in, want := range map[string]string{
		"My MacBook Pro":        "zellij-my-macbook-pro",
		"devbox":                "zellij-devbox",
		"  --Weird__Name!! ":    "zellij-weird-name",
		"":                      "zellij",
		"!!!":                   "zellij",
		strings.Repeat("a", 80): "zellij-" + strings.Repeat("a", 56),
	} {
		if got := Hostname(in); got != want {
			t.Errorf("Hostname(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJoined(t *testing.T) {
	dir := t.TempDir()
	if Joined(dir) {
		t.Fatal("empty dir counts as joined")
	}
	os.WriteFile(filepath.Join(dir, "tailscaled.state"), []byte("{}"), 0o600)
	if !Joined(dir) {
		t.Fatal("dir with state doesn't count as joined")
	}
}

func TestAllowlist(t *testing.T) {
	a := Allowlist{"Me@Example.com", " other@example.com "}
	for _, tc := range []struct {
		p  Peer
		ok bool
	}{
		{Peer{Login: "me@example.com"}, true},
		{Peer{Login: "other@example.com"}, true},
		{Peer{Login: "stranger@example.com"}, false},
		{Peer{Login: "me@example.com", Tagged: true}, false},
		{Peer{Login: ""}, false},
	} {
		if err := a.Check(tc.p); (err == nil) != tc.ok {
			t.Errorf("Check(%+v) = %v, want ok=%v", tc.p, err, tc.ok)
		}
	}
	if (Allowlist{}).Check(Peer{Login: "me@example.com"}) == nil {
		t.Error("an empty allowlist must refuse everyone")
	}
}
