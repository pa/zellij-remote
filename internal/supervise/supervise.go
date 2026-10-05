// Package supervise keeps one child process running: zellij web, started
// and restarted by zellij-remote so that a single service unit covers both.
package supervise

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Child is a program to keep running until the context ends.
type Child struct {
	Args    []string // program path first
	Env     []string // the child's environment; nil inherits ours
	Stdout  io.Writer
	Stderr  io.Writer
	PIDFile string // records the running child, so a later run can clean up after a crash
	Logf    func(format string, args ...any)

	// Backoff between restarts doubles from MinBackoff up to MaxBackoff, and
	// resets once the child has stayed up for StableAfter.
	MinBackoff, MaxBackoff, StableAfter time.Duration
	// StopTimeout is how long the child gets after SIGTERM before SIGKILL.
	StopTimeout time.Duration
}

func (c *Child) defaults() {
	if c.MinBackoff == 0 {
		c.MinBackoff = time.Second
	}
	if c.MaxBackoff == 0 {
		c.MaxBackoff = 30 * time.Second
	}
	if c.StableAfter == 0 {
		c.StableAfter = time.Minute
	}
	if c.StopTimeout == 0 {
		c.StopTimeout = 5 * time.Second
	}
	if c.Logf == nil {
		c.Logf = func(string, ...any) {}
	}
}

// Run starts the child and restarts it whenever it exits, until ctx ends.
// Then it stops the child (SIGTERM to its process group, SIGKILL after
// StopTimeout) and returns.
func (c *Child) Run(ctx context.Context) {
	c.defaults()
	backoff := c.MinBackoff
	for ctx.Err() == nil {
		started := time.Now()
		err := c.once(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) >= c.StableAfter {
			backoff = c.MinBackoff
		}
		c.Logf("%s exited (%v); restarting in %s", c.Args[0], err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > c.MaxBackoff {
			backoff = c.MaxBackoff
		}
	}
}

func (c *Child) once(ctx context.Context) error {
	cmd := exec.Command(c.Args[0], c.Args[1:]...)
	cmd.Env = c.Env
	cmd.Stdout, cmd.Stderr = c.Stdout, c.Stderr
	// Its own process group: a Ctrl-C at the terminal reaches only us, and
	// we stop the child (and anything it started) ourselves.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	if c.PIDFile != "" {
		os.WriteFile(c.PIDFile, []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o600)
		defer os.Remove(c.PIDFile)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			err = errors.New("exit status 0")
		}
		return err
	case <-ctx.Done():
		pgid := -cmd.Process.Pid
		syscall.Kill(pgid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(c.StopTimeout):
			syscall.Kill(pgid, syscall.SIGKILL)
			<-done
		}
		return ctx.Err()
	}
}

// StopStale ends a child a previous run left behind. That happens when the
// supervisor itself was killed (SIGKILL, a crash) and its child kept the
// port. It only signals a process whose command name contains want, so a
// recycled PID that now belongs to something else is left alone.
func StopStale(pidFile, want string, timeout time.Duration) (stopped bool) {
	b, err := os.ReadFile(pidFile)
	if err != nil {
		return false
	}
	defer os.Remove(pidFile)
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 1 {
		return false
	}
	out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	if err != nil || !strings.Contains(string(out), want) {
		return false
	}
	syscall.Kill(-pid, syscall.SIGTERM)
	syscall.Kill(pid, syscall.SIGTERM)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	syscall.Kill(-pid, syscall.SIGKILL)
	syscall.Kill(pid, syscall.SIGKILL)
	return true
}
