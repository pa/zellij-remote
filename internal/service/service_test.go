package service

import (
	"strings"
	"testing"
)

var unit = Unit{
	Name: "zellij-remote", Desc: "zellij-remote",
	Args: []string{"/opt/my tools/zellij", "web", "--port", "8082"},
	Env:  [][2]string{{"PATH", "/usr/bin:/a&b"}, {"SHELL", "/bin/zsh"}, {"WEIRD", `50% "$HOME" \x`}},
	Log:  "/home/me/.zellij-remote/web.log",
}

func TestPlist(t *testing.T) {
	b, err := Plist(unit)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		"<string>com.github.pa.zellij-remote</string>",
		"<string>/opt/my tools/zellij</string><string>web</string><string>--port</string><string>8082</string>",
		"<key>PATH</key><string>/usr/bin:/a&amp;b</string>",
		"<key>WEIRD</key><string>50% &#34;$HOME&#34; \\x</string>",
		"<key>StandardOutPath</key><string>/home/me/.zellij-remote/web.log</string>",
		"<key>SuccessfulExit</key><false/>",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("plist missing %q\n%s", want, s)
		}
	}
}

func TestSystemdUnit(t *testing.T) {
	s := string(SystemdUnit(unit))
	for _, want := range []string{
		"[Unit]\nDescription=zellij-remote\n",
		`ExecStart="/opt/my tools/zellij" "web" "--port" "8082"` + "\n",
		`Environment="PATH=/usr/bin:/a&b"` + "\n",
		`Environment="WEIRD=50%% \"$$HOME\" \\x"` + "\n",
		"Restart=on-failure\n",
		"StandardOutput=append:/home/me/.zellij-remote/web.log\n",
		"WantedBy=default.target\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("unit missing %q\n%s", want, s)
		}
	}
}

func TestParseLaunchctlPrint(t *testing.T) {
	st := parseLaunchctlPrint("gui/501/x = {\n\tactive count = 1\n\tstate = running\n\tpid = 4242\n}")
	if !st.Running || st.PID != "4242" || st.Detail != "running" {
		t.Errorf("got %+v", st)
	}
	st = parseLaunchctlPrint("gui/501/x = {\n\tstate = not running\n}")
	if st.Running || st.PID != "-" {
		t.Errorf("got %+v", st)
	}
}

func TestParseSystemctlShow(t *testing.T) {
	st := parseSystemctlShow("ActiveState=active\nSubState=running\nMainPID=99\n")
	if !st.Running || st.PID != "99" || st.Detail != "active (running)" {
		t.Errorf("got %+v", st)
	}
	st = parseSystemctlShow("ActiveState=failed\nSubState=failed\nMainPID=0\n")
	if st.Running || st.PID != "-" || st.Detail != "failed (failed)" {
		t.Errorf("got %+v", st)
	}
}
