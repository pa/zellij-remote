package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestConfigRoundTrip(t *testing.T) {
	t.Setenv("ZELLIJ_REMOTE_HOME", t.TempDir())
	if _, err := loadConfig(); err == nil {
		t.Fatal("loadConfig before setup should fail")
	}
	want := config{Name: "mac", Hostname: "zellij-mac", URL: "https://zellij-mac.x.ts.net", Port: 9000, Allow: []string{"me@example.com"}}
	if err := writeConfig(want); err != nil {
		t.Fatal(err)
	}
	got, err := loadConfig()
	if err != nil || !reflect.DeepEqual(got, want) {
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

func TestSplitLogins(t *testing.T) {
	got := splitLogins(" Me@Example.com, you@example.com\n")
	if !reflect.DeepEqual(got, []string{"me@example.com", "you@example.com"}) {
		t.Fatalf("got %q", got)
	}
	if splitLogins(" , ") != nil {
		t.Fatal("blank input should give no logins")
	}
}

func TestParseToken(t *testing.T) {
	for out, want := range map[string][2]string{
		"Created token successfully\n\ntoken_1: 00000000-0000-4000-8000-000000000000\n":             {"token_1", "00000000-0000-4000-8000-000000000000"},
		"Created token successfully\n\ntoken_7: 00000000-0000-4000-8000-000000000000 (read-only)\n": {"token_7", "00000000-0000-4000-8000-000000000000"},
	} {
		n, tok, ok := parseToken(out)
		if !ok || n != want[0] || tok != want[1] {
			t.Errorf("parseToken(%q) = %q, %q, %v", out, n, tok, ok)
		}
	}
	if _, _, ok := parseToken("error: something went wrong"); ok {
		t.Error("parsed a token out of an error")
	}
}

func TestOutsideZellijEnv(t *testing.T) {
	got := outsideZellijEnv([]string{"PATH=/bin", "ZELLIJ=0", "ZELLIJ_SESSION_NAME=main", "ZELLIJ_PANE_ID=3", "ZELLIJ_CONFIG_DIR=/c", "HOME=/h"})
	want := []string{"PATH=/bin", "ZELLIJ_CONFIG_DIR=/c", "HOME=/h"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestEnsureZellijConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ZELLIJ_CONFIG_DIR", "")
	t.Setenv("ZELLIJ_CONFIG_FILE", "")
	dir, created, err := ensureZellijConfigDir()
	want := filepath.Join(home, ".config", "zellij")
	if err != nil || !created || dir != want {
		t.Fatalf("first call: %q %v %v", dir, created, err)
	}
	if fi, err := os.Stat(want); err != nil || !fi.IsDir() {
		t.Fatalf("not created: %v", err)
	}
	if _, created, _ := ensureZellijConfigDir(); created {
		t.Fatal("second call created it again")
	}

	// A folder or file the user chose is theirs to manage.
	t.Setenv("ZELLIJ_CONFIG_DIR", filepath.Join(home, "elsewhere"))
	if _, created, _ := ensureZellijConfigDir(); created {
		t.Fatal("created a folder despite ZELLIJ_CONFIG_DIR")
	}
	if _, err := os.Stat(filepath.Join(home, "elsewhere")); !os.IsNotExist(err) {
		t.Fatal("touched the ZELLIJ_CONFIG_DIR folder")
	}
}
