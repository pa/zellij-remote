package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigRoundTrip(t *testing.T) {
	t.Setenv("ZELLIJ_REMOTE_HOME", t.TempDir())
	if _, err := loadConfig(); err == nil {
		t.Fatal("loadConfig before setup should fail")
	}
	want := config{Name: "mac", Hostname: "zellij-mac", URL: "https://zellij-mac.x.ts.net", Port: 9000}
	if err := writeConfig(want); err != nil {
		t.Fatal(err)
	}
	got, err := loadConfig()
	if err != nil || got != want {
		t.Fatalf("got %+v, %v", got, err)
	}
	fi, _ := os.Stat(configPath())
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("config mode %v, want 0600", fi.Mode().Perm())
	}
}

func TestConfigDefaultPort(t *testing.T) {
	t.Setenv("ZELLIJ_REMOTE_HOME", t.TempDir())
	os.WriteFile(configPath(), []byte(`{"hostname":"zellij-mac"}`), 0o600)
	c, err := loadConfig()
	if err != nil || c.Port != defaultPort {
		t.Fatalf("got %+v, %v", c, err)
	}
}

// A machine that already joined keeps its device name even if setup runs
// again with another --name: the name is part of the URL.
func TestTailnetNameKeepsJoinedHostname(t *testing.T) {
	t.Setenv("ZELLIJ_REMOTE_HOME", t.TempDir())
	if _, host := tailnetName("Laptop"); host != "zellij-laptop" {
		t.Fatalf("fresh host = %q", host)
	}
	writeConfig(config{Hostname: "zellij-old"})
	if _, host := tailnetName("Laptop"); host != "zellij-laptop" {
		t.Fatalf("config without a joined device should not pin the name, got %q", host)
	}
	os.MkdirAll(tailscaleDir(), 0o700)
	os.WriteFile(filepath.Join(tailscaleDir(), "tailscaled.state"), []byte("{}"), 0o600)
	if display, host := tailnetName("Laptop"); host != "zellij-old" || display != "Laptop" {
		t.Fatalf("got %q/%q, want Laptop/zellij-old", display, host)
	}
}

func TestTail(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.log")
	os.WriteFile(p, []byte("1\n2\n3\n4\n5\n6\n7\n"), 0o600)
	got := tail(p, 3)
	if len(got) != 3 || got[0] != "5" || got[2] != "7" {
		t.Fatalf("tail = %q", got)
	}
	if tail(filepath.Join(t.TempDir(), "missing"), 3) != nil {
		t.Fatal("missing file should give nil")
	}
}
