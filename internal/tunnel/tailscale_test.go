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
