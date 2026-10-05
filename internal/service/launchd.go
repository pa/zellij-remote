package service

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"time"
)

// LabelPrefix starts every launchd label; the unit name follows it.
const LabelPrefix = "com.github.pa."

// Launchd manages per-user launchd agents in Dir (~/Library/LaunchAgents).
type Launchd struct{ Dir string }

func (l *Launchd) Kind() string { return "launchd" }

func (l *Launchd) Path(name string) string {
	return filepath.Join(l.Dir, LabelPrefix+name+".plist")
}

func launchdDomain() string { return fmt.Sprintf("gui/%d", os.Getuid()) }

func xmlEscape(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

var plistTmpl = template.Must(template.New("plist").Funcs(template.FuncMap{"x": xmlEscape}).Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>{{x .Label}}</string>
  <key>ProgramArguments</key>
  <array>{{range .Args}}<string>{{x .}}</string>{{end}}</array>
  <key>EnvironmentVariables</key>
  <dict>{{range .Env}}
    <key>{{x (index . 0)}}</key><string>{{x (index . 1)}}</string>{{end}}
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
  <key>ThrottleInterval</key><integer>10</integer>
  <key>StandardErrorPath</key><string>{{x .Log}}</string>
  <key>StandardOutPath</key><string>{{x .Log}}</string>
  <key>ProcessType</key><string>Interactive</string>
</dict>
</plist>
`))

// Plist renders u as a launchd agent definition.
func Plist(u Unit) ([]byte, error) {
	var buf bytes.Buffer
	err := plistTmpl.Execute(&buf, map[string]any{
		"Label": LabelPrefix + u.Name, "Args": u.Args, "Env": u.Env, "Log": u.Log,
	})
	return buf.Bytes(), err
}

func (l *Launchd) Install(units []Unit) error {
	if err := os.MkdirAll(l.Dir, 0o755); err != nil {
		return err
	}
	for _, u := range units {
		b, err := Plist(u)
		if err != nil {
			return err
		}
		target := launchdDomain() + "/" + LabelPrefix + u.Name
		// Replace a loaded agent so a new binary or PATH takes effect.
		// bootout returns before the old one has finished unloading, and
		// bootstrapping over it fails with "Input/output error", so wait.
		exec.Command("launchctl", "bootout", target).Run()
		for i := 0; i < 50 && exec.Command("launchctl", "print", target).Run() == nil; i++ {
			time.Sleep(100 * time.Millisecond)
		}
		if err := os.WriteFile(l.Path(u.Name), b, 0o644); err != nil {
			return err
		}
		var out []byte
		for attempt := 0; attempt < 3; attempt++ {
			if out, err = exec.Command("launchctl", "bootstrap", launchdDomain(), l.Path(u.Name)).CombinedOutput(); err == nil {
				break
			}
			time.Sleep(time.Second)
		}
		if err != nil {
			return fmt.Errorf("launchctl bootstrap %s: %v: %s", u.Name, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

func (l *Launchd) Remove(names []string) error {
	for _, n := range names {
		out, err := exec.Command("launchctl", "bootout", launchdDomain()+"/"+LabelPrefix+n).CombinedOutput()
		if err != nil && !strings.Contains(string(out), "No such process") && !strings.Contains(string(out), "Could not find") {
			return fmt.Errorf("launchctl bootout %s: %v: %s", n, err, strings.TrimSpace(string(out)))
		}
		if err := os.Remove(l.Path(n)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (l *Launchd) Runs(name, path string) bool {
	b, err := os.ReadFile(l.Path(name))
	if err != nil {
		return false
	}
	// The program is ProgramArguments' first string, right after <array>.
	return bytes.Contains(b, []byte("<array><string>"+xmlEscape(path)+"</string>"))
}

func (l *Launchd) Restart(name string) error {
	out, err := exec.Command("launchctl", "kickstart", "-k", launchdDomain()+"/"+LabelPrefix+name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl kickstart: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (l *Launchd) State(name string) State {
	out, err := exec.Command("launchctl", "print", launchdDomain()+"/"+LabelPrefix+name).CombinedOutput()
	if err != nil {
		return State{PID: "-", Detail: "not installed"}
	}
	return parseLaunchctlPrint(string(out))
}

// parseLaunchctlPrint reads the job's own state and pid. Only the first
// of each counts: nested sections (endpoints, event triggers) have "state"
// lines of their own, like "state = active", that say nothing about the job.
func parseLaunchctlPrint(out string) State {
	s := State{Installed: true, PID: "-", Detail: "unknown"}
	var gotState, gotPID bool
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "state = "); ok && !gotState {
			s.Detail, gotState = v, true
			s.Running = v == "running"
		}
		if v, ok := strings.CutPrefix(line, "pid = "); ok && !gotPID {
			s.PID, gotPID = v, true
		}
	}
	return s
}
