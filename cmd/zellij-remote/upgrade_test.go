package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeGitHub serves one release, v9.9.9, the way GitHub's API does, and
// counts requests.
func fakeGitHub(t *testing.T, bin []byte, tamper bool) *atomic.Int32 {
	t.Helper()
	tag := "v9.9.9"
	name := fmt.Sprintf("zellij-remote_%s_%s_%s.tar.gz", tag, runtime.GOOS, runtime.GOARCH)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	dir := strings.TrimSuffix(name, ".tar.gz")
	tw.WriteHeader(&tar.Header{Name: dir + "/README.md", Mode: 0o644, Size: 2, Typeflag: tar.TypeReg})
	tw.Write([]byte("hi"))
	tw.WriteHeader(&tar.Header{Name: dir + "/zellij-remote", Mode: 0o755, Size: int64(len(bin)), Typeflag: tar.TypeReg})
	tw.Write(bin)
	tw.Close()
	zw.Close()
	tgz := buf.Bytes()
	sum := sha256.Sum256(tgz)
	if tamper {
		sum[0] ^= 1
	}
	sums := hex.EncodeToString(sum[:]) + "  " + name + "\n"

	var hits atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Errorf("%s sent credentials; downloads must be anonymous", r.URL.Path)
		}
		switch r.URL.Path {
		case "/latest":
			json.NewEncoder(w).Encode(release{Tag: tag, Assets: []asset{
				{Name: name, URL: srv.URL + "/asset/tgz"}, {Name: "checksums.txt", URL: srv.URL + "/asset/sums"},
			}})
		case "/asset/tgz":
			w.Write(tgz)
		case "/asset/sums":
			io.WriteString(w, sums)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := releasesAPI
	releasesAPI = srv.URL
	t.Cleanup(func() { releasesAPI = old })
	t.Setenv("GH_TOKEN", "must-not-be-sent")
	return &hits
}

func setVersion(t *testing.T, v string) {
	t.Helper()
	old := version
	version = v
	t.Cleanup(func() { version = old })
}

func TestUpgradeReplacesTheBinary(t *testing.T) {
	t.Setenv("ZELLIJ_REMOTE_HOME", t.TempDir())
	setVersion(t, "v1.0.0")
	fakeGitHub(t, []byte("new binary"), false)
	exe := filepath.Join(t.TempDir(), "zellij-remote")
	os.WriteFile(exe, []byte("old binary"), 0o755)
	var out bytes.Buffer
	ok, err := upgradeTo(context.Background(), exe, false, false, &out)
	if err != nil || !ok {
		t.Fatalf("upgrade: %v %v", ok, err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new binary" {
		t.Fatalf("binary is %q", b)
	}
	if fi, _ := os.Stat(exe); fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v", fi.Mode())
	}
}

func TestUpgradeRefusesABadChecksum(t *testing.T) {
	t.Setenv("ZELLIJ_REMOTE_HOME", t.TempDir())
	setVersion(t, "v1.0.0")
	fakeGitHub(t, []byte("new binary"), true)
	exe := filepath.Join(t.TempDir(), "zellij-remote")
	os.WriteFile(exe, []byte("old binary"), 0o755)
	if _, err := upgradeTo(context.Background(), exe, false, false, io.Discard); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old binary" {
		t.Fatal("a bad download replaced the binary")
	}
}

func TestUpgradeCheckAndUpToDate(t *testing.T) {
	t.Setenv("ZELLIJ_REMOTE_HOME", t.TempDir())
	setVersion(t, "v1.0.0")
	fakeGitHub(t, []byte("new binary"), false)
	exe := filepath.Join(t.TempDir(), "zellij-remote")
	os.WriteFile(exe, []byte("old binary"), 0o755)
	var out bytes.Buffer
	if ok, err := upgradeTo(context.Background(), exe, true, false, &out); ok || err != nil || !strings.Contains(out.String(), "v9.9.9 is available") {
		t.Fatalf("--check: %v %v %q", ok, err, out.String())
	}
	if b, _ := os.ReadFile(exe); string(b) != "old binary" {
		t.Fatal("--check replaced the binary")
	}
	setVersion(t, "v9.9.9")
	out.Reset()
	if ok, _ := upgradeTo(context.Background(), exe, false, false, &out); ok || !strings.Contains(out.String(), "latest release") {
		t.Fatalf("up to date: %v %q", ok, out.String())
	}
	setVersion(t, "v10.0.0")
	out.Reset()
	if ok, _ := upgradeTo(context.Background(), exe, false, false, &out); ok || !strings.Contains(out.String(), "newer than the latest") {
		t.Fatalf("ahead of the release: %v %q", ok, out.String())
	}
}

func TestUpgradeDevBuildNeedsForce(t *testing.T) {
	t.Setenv("ZELLIJ_REMOTE_HOME", t.TempDir())
	setVersion(t, "dev")
	fakeGitHub(t, []byte("new binary"), false)
	exe := filepath.Join(t.TempDir(), "zellij-remote")
	os.WriteFile(exe, []byte("my build"), 0o755)
	if _, err := upgradeTo(context.Background(), exe, false, false, io.Discard); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "my build" {
		t.Fatal("a dev build was replaced without --force")
	}
	if ok, err := upgradeTo(context.Background(), exe, false, true, io.Discard); !ok || err != nil {
		t.Fatalf("--force: %v %v", ok, err)
	}
}

func TestUpdateNotice(t *testing.T) {
	t.Setenv("ZELLIJ_REMOTE_HOME", t.TempDir())
	setVersion(t, "v1.0.0")
	if n := updateNotice(); n != "" {
		t.Fatalf("notice without a check: %q", n)
	}
	recordLatest("v1.2.0")
	if n := updateNotice(); !strings.Contains(n, "v1.2.0 is available") || !strings.Contains(n, "zellij-remote upgrade") {
		t.Fatalf("notice = %q", n)
	}
	recordLatest("v1.0.0")
	if n := updateNotice(); n != "" {
		t.Fatalf("notice when up to date: %q", n)
	}
	setVersion(t, "dev")
	recordLatest("v1.2.0")
	if n := updateNotice(); n != "" {
		t.Fatalf("a dev build got a notice: %q", n)
	}
}

// A command asks GitHub only when the last answer is over a day old.
func TestNoticeChecksAtMostDaily(t *testing.T) {
	t.Setenv("ZELLIJ_REMOTE_HOME", t.TempDir())
	setVersion(t, "v1.0.0")
	hits := fakeGitHub(t, []byte("x"), false)
	stderr := os.Stderr
	devnull, _ := os.Open(os.DevNull)
	os.Stderr = devnull
	defer func() { os.Stderr = stderr }()

	noticeAfterCommand()
	if hits.Load() != 1 {
		t.Fatalf("first command: %d requests, want 1", hits.Load())
	}
	noticeAfterCommand()
	if hits.Load() != 1 {
		t.Fatalf("second command the same day: %d requests, want still 1", hits.Load())
	}
	b, _ := json.Marshal(latestSeen{Tag: "v9.9.9", Checked: time.Now().Add(-25 * time.Hour)})
	os.WriteFile(latestPath(), b, 0o600)
	noticeAfterCommand()
	if hits.Load() != 2 {
		t.Fatalf("after a day: %d requests, want 2", hits.Load())
	}

	t.Setenv("ZELLIJ_REMOTE_NO_UPDATE_CHECK", "1")
	os.WriteFile(latestPath(), b, 0o600)
	noticeAfterCommand()
	if hits.Load() != 2 {
		t.Fatal("checked GitHub with ZELLIJ_REMOTE_NO_UPDATE_CHECK=1")
	}
}

func TestNewer(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"v1.2.0", "v1.1.9", true},
		{"v1.10.0", "v1.9.0", true},
		{"v2.0.0", "v1.99.99", true},
		{"v1.0.0", "v1.0.0", false},
		{"v1.0.0", "v1.0.1", false},
		{"v1.0.0", "dev", false},
		{"dev", "v1.0.0", false},
		{"1.2.3", "v1.0.0", false},
		{"v1.2", "v1.0.0", false},
	} {
		if got := newer(tc.a, tc.b); got != tc.want {
			t.Errorf("newer(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// With no release published (or GitHub unreachable), a command still
// waits a day before asking again.
func TestFailedCheckBacksOff(t *testing.T) {
	t.Setenv("ZELLIJ_REMOTE_HOME", t.TempDir())
	setVersion(t, "v1.0.0")
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	defer srv.Close()
	old := releasesAPI
	releasesAPI = srv.URL
	defer func() { releasesAPI = old }()
	stderr := os.Stderr
	devnull, _ := os.Open(os.DevNull)
	os.Stderr = devnull
	defer func() { os.Stderr = stderr }()

	noticeAfterCommand()
	noticeAfterCommand()
	noticeAfterCommand()
	if hits.Load() != 1 {
		t.Fatalf("%d requests for three commands, want 1", hits.Load())
	}
	if n := updateNotice(); n != "" {
		t.Fatalf("notice after a failed check: %q", n)
	}
}
