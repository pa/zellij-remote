package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// How often zellij-remote asks GitHub for the latest release: the running
// service once a day, and any command when the last answer is older than
// that. ZELLIJ_REMOTE_NO_UPDATE_CHECK=1 turns the check off.
const updateEvery = 24 * time.Hour

// latestSeen is what the last update check found, in ~/.zellij-remote/latest.json.
type latestSeen struct {
	Tag     string    `json:"tag"`
	Checked time.Time `json:"checked"`
}

func latestPath() string { return filepath.Join(home(), "latest.json") }

func updateCheckOff() bool { return os.Getenv("ZELLIJ_REMOTE_NO_UPDATE_CHECK") == "1" }

// readLatest returns the last check's result. Tag is empty when that
// check failed (offline, no releases yet, rate-limited); Checked is still
// set, so a failure also waits a day before the next try.
func readLatest() (latestSeen, bool) {
	var l latestSeen
	b, err := os.ReadFile(latestPath())
	if err != nil || json.Unmarshal(b, &l) != nil || l.Checked.IsZero() {
		return l, false
	}
	return l, true
}

// recordLatest saves the latest release's tag, if the state directory
// exists (it doesn't before setup, and a check shouldn't create it).
func recordLatest(tag string) {
	if _, err := os.Stat(home()); err != nil {
		return
	}
	b, _ := json.Marshal(latestSeen{Tag: tag, Checked: time.Now().UTC()})
	os.WriteFile(latestPath(), b, 0o600)
}

// checkOnce asks GitHub for the latest release and records it. A failed
// check keeps the last known tag but records the time, so GitHub isn't
// asked again on every command.
func checkOnce(ctx context.Context, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	rel, err := latestRelease(ctx)
	if err != nil {
		prev, _ := readLatest()
		recordLatest(prev.Tag)
		return err
	}
	recordLatest(rel.Tag)
	return nil
}

// updateNotice is the line that says to upgrade, or "" when this build is
// the latest (or a development build, which is never told to upgrade).
func updateNotice() string {
	l, ok := readLatest()
	if !ok || l.Tag == "" || !newer(l.Tag, version) {
		return ""
	}
	return fmt.Sprintf("zellij-remote %s is available (this is %s). Run `zellij-remote upgrade`.", l.Tag, version)
}

// noticeAfterCommand prints the upgrade notice to stderr after a command,
// so output meant for scripts stays clean. When the last check is over a
// day old it asks GitHub first, giving up after two seconds rather than
// holding up the command.
func noticeAfterCommand() {
	if updateCheckOff() {
		return
	}
	if _, isRelease := parseVersion(version); !isRelease {
		return
	}
	if l, ok := readLatest(); !ok || time.Since(l.Checked) > updateEvery {
		checkOnce(context.Background(), 2*time.Second)
	}
	if n := updateNotice(); n != "" {
		fmt.Fprintln(os.Stderr, "\n"+n)
	}
}

// checkForUpdates runs inside the service: a minute after it starts and
// then once a day, it looks up the latest release and logs the notice.
// It only ever tells; installing is `zellij-remote upgrade`, run by you.
func checkForUpdates(ctx context.Context) {
	if updateCheckOff() {
		return
	}
	wait := time.Minute
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = updateEvery
		if err := checkOnce(ctx, time.Minute); err != nil {
			log.Printf("update check: %v", err)
		} else if n := updateNotice(); n != "" {
			log.Print(n)
		}
	}
}

// newer reports whether version a is later than b. Both look like v1.2.3;
// anything else, such as a "dev" build, is never newer or older.
func newer(a, b string) bool {
	va, ok1 := parseVersion(a)
	vb, ok2 := parseVersion(b)
	if !ok1 || !ok2 {
		return false
	}
	for i := range va {
		if va[i] != vb[i] {
			return va[i] > vb[i]
		}
	}
	return false
}

func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) != 3 || !strings.HasPrefix(v, "v") {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
