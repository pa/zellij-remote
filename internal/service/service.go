// Package service runs zellij-remote in the background under the
// platform's per-user service manager: a launchd agent on macOS, a systemd
// --user unit on Linux. It starts at login and restarts on a crash.
package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Unit is one background program.
type Unit struct {
	Name string   // "zellij-remote"; the launchd label and systemd unit derive from it
	Desc string   // one line, for systemd's Description
	Args []string // absolute program path first
	Env  [][2]string
	Log  string // stdout and stderr both go here
}

// State is what the service manager says about a unit.
type State struct {
	Installed bool
	Running   bool
	PID       string // "-" when not running
	Detail    string // the manager's own word for it, like "running" or "failed"
}

// Manager installs, removes and inspects units.
type Manager interface {
	Kind() string // "launchd" or "systemd"
	Install(units []Unit) error
	Remove(names []string) error
	// Restart stops and starts an installed unit, so it runs a new binary.
	Restart(name string) error
	// Runs reports whether the installed unit starts the program at path.
	Runs(name, path string) bool
	State(name string) State
	// Path is where the unit's definition file lives.
	Path(name string) string
}

// ForOS returns the manager for this platform.
func ForOS() (Manager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	switch runtime.GOOS {
	case "darwin":
		return &Launchd{Dir: filepath.Join(home, "Library", "LaunchAgents")}, nil
	case "linux":
		dir := os.Getenv("XDG_CONFIG_HOME")
		if dir == "" {
			dir = filepath.Join(home, ".config")
		}
		return &Systemd{Dir: filepath.Join(dir, "systemd", "user")}, nil
	}
	return nil, fmt.Errorf("background mode isn't supported on %s; use `zellij-remote run` under your own supervisor", runtime.GOOS)
}

// Executable returns this program's resolved path, refusing a `go run`
// build: its binary is deleted when go run exits, and the service would
// point at nothing.
func Executable() (string, error) {
	bin, err := os.Executable()
	if err != nil {
		return "", err
	}
	if bin, err = filepath.EvalSymlinks(bin); err != nil {
		return "", err
	}
	if strings.Contains(bin, "/go-build") {
		return "", errors.New("run this from an installed binary (go install ./cmd/zellij-remote), not `go run`")
	}
	return bin, nil
}
