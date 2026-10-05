package supervise

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type logs struct {
	mu    sync.Mutex
	lines []string
}

func (l *logs) logf(f string, a ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, f)
}

func (l *logs) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.lines)
}

func TestRestartsAChildThatExits(t *testing.T) {
	var l logs
	c := &Child{Args: []string{"/bin/sh", "-c", "exit 3"}, Logf: l.logf, MinBackoff: 10 * time.Millisecond, MaxBackoff: 20 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	c.Run(ctx)
	if n := l.count(); n < 3 {
		t.Fatalf("restarted %d times in 300ms, want several", n)
	}
}

func TestStopsTheChildOnCancel(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	c := &Child{Args: []string{"/bin/sleep", "60"}, PIDFile: pidFile}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()

	var pid int
	for i := 0; i < 100 && pid == 0; i++ {
		b, _ := os.ReadFile(pidFile)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("child never wrote its pid")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run didn't return after cancel")
	}
	if syscall.Kill(pid, 0) == nil {
		t.Fatalf("child %d still running", pid)
	}
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Fatal("pid file left behind")
	}
}

func TestStopStale(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")

	cmd := exec.Command("/bin/sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go cmd.Wait()
	os.WriteFile(pidFile, []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)

	// A name that doesn't match must be left alone.
	if StopStale(pidFile, "zellij", time.Second) {
		t.Fatal("stopped a process that isn't zellij")
	}
	if syscall.Kill(cmd.Process.Pid, 0) != nil {
		t.Fatal("non-matching process was killed")
	}

	os.WriteFile(pidFile, []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
	if !StopStale(pidFile, "sleep", 2*time.Second) {
		t.Fatal("didn't stop the stale child")
	}
	time.Sleep(50 * time.Millisecond)
	if syscall.Kill(cmd.Process.Pid, 0) == nil {
		t.Fatal("stale child still running")
	}
}
