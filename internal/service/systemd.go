package service

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Systemd manages systemd --user units in Dir (~/.config/systemd/user).
type Systemd struct{ Dir string }

func (s *Systemd) Kind() string { return "systemd" }

func unitName(name string) string { return name + ".service" }

func (s *Systemd) Path(name string) string { return filepath.Join(s.Dir, unitName(name)) }

// systemdQuote quotes one word for an Exec= or Environment= line. Inside
// double quotes systemd treats \ as an escape; % starts a specifier and $
// a variable anywhere, so both are doubled.
func systemdQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`, `$`, `$$`)
	return `"` + r.Replace(s) + `"`
}

// pathValue escapes a file path for a directive that takes a bare path,
// where only % is special.
func pathValue(s string) string { return strings.ReplaceAll(s, "%", "%%") }

// SystemdUnit renders u as a systemd user service.
func SystemdUnit(u Unit) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "[Unit]\nDescription=%s\n", u.Desc)
	b.WriteString("\n[Service]\nType=simple\n")
	args := make([]string, len(u.Args))
	for i, a := range u.Args {
		args[i] = systemdQuote(a)
	}
	fmt.Fprintf(&b, "ExecStart=%s\n", strings.Join(args, " "))
	for _, kv := range u.Env {
		fmt.Fprintf(&b, "Environment=%s\n", systemdQuote(kv[0]+"="+kv[1]))
	}
	b.WriteString("Restart=on-failure\nRestartSec=10\n")
	fmt.Fprintf(&b, "StandardOutput=append:%s\nStandardError=append:%s\n", pathValue(u.Log), pathValue(u.Log))
	b.WriteString("\n[Install]\nWantedBy=default.target\n")
	return []byte(b.String())
}

func systemctl(args ...string) error {
	out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl --user %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (s *Systemd) Install(units []Unit) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	names := make([]string, len(units))
	for i, u := range units {
		if err := os.WriteFile(s.Path(u.Name), SystemdUnit(u), 0o644); err != nil {
			return err
		}
		names[i] = unitName(u.Name)
	}
	if err := systemctl("daemon-reload"); err != nil {
		return err
	}
	if err := systemctl(append([]string{"enable"}, names...)...); err != nil {
		return err
	}
	// restart, not start, so a new binary or PATH takes effect.
	return systemctl(append([]string{"restart"}, names...)...)
}

func (s *Systemd) Remove(names []string) error {
	for _, n := range names {
		if _, err := os.Stat(s.Path(n)); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := systemctl("disable", "--now", unitName(n)); err != nil {
			return err
		}
		if err := os.Remove(s.Path(n)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return systemctl("daemon-reload")
}

func (s *Systemd) State(name string) State {
	if _, err := os.Stat(s.Path(name)); err != nil {
		return State{PID: "-", Detail: "not installed"}
	}
	out, err := exec.Command("systemctl", "--user", "show", "-p", "ActiveState,SubState,MainPID", unitName(name)).Output()
	if err != nil {
		return State{Installed: true, PID: "-", Detail: "unknown"}
	}
	return parseSystemctlShow(string(out))
}

func parseSystemctlShow(out string) State {
	st := State{Installed: true, PID: "-", Detail: "unknown"}
	var active, sub string
	for _, line := range strings.Split(out, "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch k {
		case "ActiveState":
			active = v
		case "SubState":
			sub = v
		case "MainPID":
			if v != "" && v != "0" {
				st.PID = v
			}
		}
	}
	st.Running = active == "active" && sub == "running"
	if active != "" {
		st.Detail = active
		if sub != "" {
			st.Detail += " (" + sub + ")"
		}
	}
	return st
}

// Lingering reports whether the user's systemd instance keeps running
// without a login session. Without it, the units stop when the last
// session (including an SSH one) ends, and start again at the next login.
func Lingering() bool {
	u := os.Getenv("USER")
	if u == "" {
		return false
	}
	_, err := os.Stat(filepath.Join("/var/lib/systemd/linger", u))
	return err == nil
}
