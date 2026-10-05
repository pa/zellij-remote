// Command zellij-remote serves zellij's web client to your tailnet, and
// nothing else on this machine.
//
//	zellij-remote setup [--name <n>] [--port <p>]   join the tailnet once, make a login token
//	zellij-remote run                               serve in the foreground
//	zellij-remote start | stop | status             run in the background (launchd on
//	                                                macOS, systemd --user on Linux)
//	zellij-remote token [--read-only]               make another login token
//
// It embeds tsnet, joins the tailnet as its own device (zellij-<name>,
// tagged tag:zellij), listens on :443 with the device's *.ts.net
// certificate, and reverse-proxies to zellij web on 127.0.0.1, which it
// starts and keeps running itself.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/pa/zellij-remote/internal/proxy"
	"github.com/pa/zellij-remote/internal/service"
	"github.com/pa/zellij-remote/internal/supervise"
	"github.com/pa/zellij-remote/internal/tunnel"
)

// version is set at release time with -ldflags "-X main.version=...".
var version = "dev"

const defaultPort = 8082

const usage = `zellij-remote serves zellij's web client to your tailnet, and nothing else.

usage:
  zellij-remote setup [--name <n>] [--port <p>]  join the tailnet (once), print the URL
                                                 and a login token
  zellij-remote run                              serve in the foreground (starts zellij web)
  zellij-remote start | stop | status            run in the background (at login,
                                                 restarted on crash)
  zellij-remote token [--read-only]              make another login token
  zellij-remote version

Setup reads the Tailscale auth key from TS_AUTHKEY, or asks for it.
State lives in ~/.zellij-remote (override with ZELLIJ_REMOTE_HOME).
`

func main() {
	log.SetFlags(log.LstdFlags)
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch cmd, args := os.Args[1], os.Args[2:]; cmd {
	case "setup":
		err = cmdSetup(args)
	case "run":
		err = cmdRun()
	case "start":
		err = cmdStart()
	case "stop":
		err = cmdStop()
	case "status":
		err = cmdStatus()
	case "token":
		err = cmdToken(args)
	case "version", "--version", "-v":
		fmt.Println("zellij-remote", version)
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "zellij-remote:", err)
		os.Exit(1)
	}
}

// ---- state ----

func home() string {
	if h := os.Getenv("ZELLIJ_REMOTE_HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".zellij-remote")
}

func tailscaleDir() string { return filepath.Join(home(), "tailscale") }

type config struct {
	Name     string `json:"name"`     // what the user called this machine
	Hostname string `json:"hostname"` // its tailnet device name
	URL      string `json:"url"`      // https://<hostname>.<tailnet>.ts.net
	Port     int    `json:"port"`     // zellij web's local port
	// Allow lists the Tailscale logins that may connect. Empty refuses
	// everyone: run won't start without at least one.
	Allow []string `json:"allow"`
}

func configPath() string { return filepath.Join(home(), "config.json") }

func loadConfig() (config, error) {
	var c config
	b, err := os.ReadFile(configPath())
	if errors.Is(err, os.ErrNotExist) {
		return c, errors.New("not set up yet; run `zellij-remote setup` first")
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", configPath(), err)
	}
	if c.Port == 0 {
		c.Port = defaultPort
	}
	return c, nil
}

func writeConfig(c config) error {
	if err := os.MkdirAll(home(), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(configPath(), append(b, '\n'), 0o600)
}

// ---- setup ----

// cmdSetup joins the tailnet once. The auth key comes from TS_AUTHKEY or a
// hidden prompt, never a flag, so it stays out of shell history and the
// process list, and it isn't saved: after the first join the device's
// identity is in ~/.zellij-remote/tailscale.
func cmdSetup(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	name := fs.String("name", "", "this machine's name on the tailnet (default: its host name)")
	port := fs.Int("port", defaultPort, "the local port zellij web listens on")
	allowFlag := fs.String("allow", "", "Tailscale logins that may connect, comma-separated (you@example.com)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	allow := splitLogins(*allowFlag)
	if len(allow) == 0 {
		if prev, err := loadConfig(); err == nil {
			allow = prev.Allow
		}
	}
	if len(allow) == 0 {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return errors.New("say who may connect: --allow you@example.com (your Tailscale login)")
		}
		fmt.Print("Your Tailscale login, as the admin console's Users page shows it\n(you@yourdomain.com, <github-user>@github; comma-separate several): ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if allow = splitLogins(line); len(allow) == 0 {
			return errors.New("at least one login is needed")
		}
	}
	if _, err := exec.LookPath("zellij"); err != nil {
		return errors.New("zellij isn't on PATH; install zellij 0.43 or newer first (https://zellij.dev)")
	}
	display, host := tailnetName(*name)
	ts := &tunnel.Tailscale{Dir: tailscaleDir(), Hostname: host, Logf: func(f string, a ...any) { fmt.Printf(f+"\n", a...) }}
	firstJoin := !tunnel.Joined(ts.Dir)
	if firstJoin {
		key, err := authKey(host)
		if err != nil {
			return err
		}
		ts.AuthKey = key
	}
	fmt.Printf("joining the tailnet as %s...\n", host)
	u, err := ts.Join(context.Background())
	if err != nil {
		return err
	}
	if err := writeConfig(config{Name: display, Hostname: host, URL: u, Port: *port, Allow: allow}); err != nil {
		return err
	}
	fmt.Printf("\njoined. Other devices on your tailnet will reach zellij at\n    %s\n", u)
	fmt.Printf("Only these Tailscale logins get through: %s\n", strings.Join(allow, ", "))
	// One login token on the first join, so the URL is usable right away.
	// Running setup again doesn't pile up more; `zellij-remote token` does.
	if firstJoin {
		fmt.Println()
		if err := printToken(false); err != nil {
			fmt.Printf("couldn't create a login token (%v); run `zellij-remote token` to try again\n", err)
		}
	}
	fmt.Println("\nNext: zellij-remote start     (runs at login, restarts if it crashes)")
	return nil
}

// ---- token ----

func cmdToken(args []string) error {
	fs := flag.NewFlagSet("token", flag.ContinueOnError)
	ro := fs.Bool("read-only", false, "the token can only watch existing sessions")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return printToken(*ro)
}

func printToken(readOnly bool) error {
	name, tok, err := createToken(readOnly)
	if err != nil {
		return err
	}
	kind := "login token"
	if readOnly {
		kind = "read-only login token (can watch sessions, not type in them)"
	}
	fmt.Printf(`%s %q, shown only this once:
    %s
Anyone with it gets a shell on this machine. List or revoke tokens with
    zellij web --list-tokens
    zellij web --revoke-token %s
`, kind, name, tok, name)
	return nil
}

// createToken asks zellij for a new web login token. zellij prints it as
// "<name>: <token>" (plus " (read-only)"), once; it keeps only a hash.
func createToken(readOnly bool) (name, token string, err error) {
	arg := "--create-token"
	if readOnly {
		arg = "--create-read-only-token"
	}
	out, err := exec.Command("zellij", "web", arg).CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf("zellij web %s: %v: %s", arg, err, strings.TrimSpace(string(out)))
	}
	if name, token, ok := parseToken(string(out)); ok {
		return name, token, nil
	}
	return "", "", fmt.Errorf("unexpected output from zellij web %s", arg)
}

var tokenLine = regexp.MustCompile(`^(\S+): ([0-9a-fA-F]{8}-[0-9a-fA-F-]{27,})( \(read-only\))?$`)

func parseToken(out string) (name, token string, ok bool) {
	for _, line := range strings.Split(out, "\n") {
		if m := tokenLine.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			return m[1], m[2], true
		}
	}
	return "", "", false
}

// splitLogins parses a comma- or space-separated list of logins.
func splitLogins(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' }) {
		out = append(out, strings.ToLower(f))
	}
	return out
}

// tailnetName returns the machine's display name (the host name when name
// is empty) and its device name on the tailnet. A machine that already
// joined keeps its device name, since it's part of the URL.
func tailnetName(name string) (display, host string) {
	if name == "" {
		name, _ = os.Hostname()
		name = strings.TrimSuffix(name, ".local")
	}
	if prev, err := loadConfig(); err == nil && prev.Hostname != "" && tunnel.Joined(tailscaleDir()) {
		return name, prev.Hostname
	}
	return name, tunnel.Hostname(name)
}

// authKey returns the Tailscale auth key from TS_AUTHKEY, or else asks for
// it, after walking through the admin console when stdin is a terminal.
func authKey(host string) (string, error) {
	key := os.Getenv("TS_AUTHKEY")
	if key == "" {
		interactive := term.IsTerminal(int(os.Stdin.Fd()))
		if interactive {
			if err := tailnetGuide(host); err != nil {
				return "", err
			}
		}
		fmt.Print("Paste the auth key (input hidden): ")
		b, err := readSecret(interactive)
		fmt.Println()
		if err != nil {
			return "", fmt.Errorf("reading the key: %w", err)
		}
		key = strings.TrimSpace(string(b))
	}
	if !strings.HasPrefix(key, "tskey-") {
		return "", errors.New("that doesn't look like a Tailscale auth key (tskey-...)")
	}
	return key, nil
}

// readSecret reads one line from stdin without echoing it at a terminal.
// Piped in (from a password manager's CLI, say), it reads the first line.
func readSecret(interactive bool) ([]byte, error) {
	if interactive {
		return term.ReadPassword(int(os.Stdin.Fd()))
	}
	b, err := bufio.NewReader(os.Stdin).ReadBytes('\n')
	if errors.Is(err, io.EOF) {
		err = nil
	}
	return b, err
}

// tailnetGuide walks through the admin console before asking for the key,
// one step at a time. It only runs at a terminal: with TS_AUTHKEY set or a
// key piped in, setup goes straight on.
func tailnetGuide(host string) error {
	in := bufio.NewReader(os.Stdin)
	steps := []struct{ title, body string }{
		{"Turn on MagicDNS and HTTPS certificates", `Open https://console.tailscale.com/admin/dns
  - If MagicDNS is off, select Enable MagicDNS.
  - Under HTTPS Certificates, select Enable HTTPS and accept the notice.

HTTPS certificates are published in public Certificate Transparency logs,
so this machine's address will be public:
    ` + host + `.<your-tailnet>.ts.net
If that name says too much, press Ctrl-C and run setup again with --name.`},
		{"Add the tag, and who can reach it", `Open https://console.tailscale.com/admin/acls and switch to the JSON editor.
Add these next to what's already there, then select Save:

    "tagOwners": {
      "` + tunnel.Tag + `": ["autogroup:admin"]
    },
    "grants": [
      { "src": ["you@example.com"], "dst": ["` + tunnel.Tag + `"], "ip": ["tcp:443"] }
    ]

Put your own Tailscale login in "src": not a URL, but the account you sign
in with, exactly as https://console.tailscale.com/admin/users shows it
(you@yourdomain.com, <github-user>@github, <name>@passkey). A zellij login
is a shell on this machine, so keep the grant to yourself. If the file already has a "grants"
or "tagOwners" section, add the entries inside it rather than a second one.`},
		{"Create the auth key", `Open https://console.tailscale.com/admin/settings/keys and select
Generate auth key, with:
  - Reusable:      off   (one key, one device)
  - Expiration:    1 day (it's only needed now)
  - Ephemeral:     off   (it would vanish whenever the machine sleeps)
  - Tags:          on, ` + tunnel.Tag + `   (missing? step 2 wasn't saved)
  - Pre-approved:  on, if it's shown
Copy the key (tskey-auth-...). It's shown once.`},
	}
	fmt.Println("Before joining, three settings in the Tailscale admin console. (Done already? Press Enter through them.)")
	for i, s := range steps {
		fmt.Printf("\n[%d/%d] %s\n\n%s\n\nPress Enter when done. ", i+1, len(steps), s.title, s.body)
		if _, err := in.ReadString('\n'); err != nil {
			return err
		}
	}
	fmt.Println()
	return nil
}

// ---- run ----

// cmdRun serves in the foreground. It starts zellij web on 127.0.0.1 and
// restarts it if it exits, unless a zellij web is already listening on the
// port, which it then uses as is. On SIGINT/SIGTERM it stops both.
func cmdRun() error {
	c, err := loadConfig()
	if err != nil {
		return err
	}
	if len(c.Allow) == 0 {
		return errors.New("nobody is allowed to connect yet; run `zellij-remote setup --allow you@example.com`")
	}
	release, err := lockRun()
	if err != nil {
		return err
	}
	defer release()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	target := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(c.Port))}
	webDone := make(chan struct{})
	// A zellij web left over from a run that was killed outright still
	// holds the port; stop it so this run supervises a fresh one.
	if supervise.StopStale(webPIDPath(), "zellij", 5*time.Second) {
		log.Print("stopped a zellij web left over from an earlier run")
	}
	if zellijUp(c.Port) {
		log.Printf("zellij web is already running on %s; using it as is", target.Host)
		close(webDone)
	} else {
		zj, err := exec.LookPath("zellij")
		if err != nil {
			return errors.New("zellij isn't on PATH")
		}
		child := &supervise.Child{
			Args:    []string{zj, "web", "--ip", "127.0.0.1", "--port", strconv.Itoa(c.Port)},
			Env:     outsideZellijEnv(os.Environ()),
			Stdout:  os.Stdout,
			Stderr:  os.Stderr,
			PIDFile: webPIDPath(),
			Logf:    log.Printf,
		}
		log.Printf("starting zellij web on %s", target.Host)
		go func() { child.Run(ctx); close(webDone) }()
	}

	ts := &tunnel.Tailscale{Dir: tailscaleDir(), Hostname: c.Hostname, Logf: log.Printf}
	ln, u, err := ts.Listen(ctx)
	if err != nil {
		stop()
		<-webDone
		return err
	}
	defer ts.Close()
	if u != c.URL {
		// The tailnet was renamed, or the device was; keep the config honest.
		c.URL = u
		writeConfig(c)
	}

	srv := &http.Server{
		Handler: proxy.New(proxy.Config{
			Target: target, Origin: u, Logger: log.Default(),
			Authorize: func(r *http.Request) (string, error) {
				p, err := ts.WhoIs(r.Context(), r.RemoteAddr)
				if err != nil {
					return "", fmt.Errorf("who is this? %w", err)
				}
				if err := tunnel.Allowlist(c.Allow).Check(p); err != nil {
					return p.Login, err
				}
				return p.Login, nil
			},
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	log.Printf("serving %s -> %s, for %s", u, target, strings.Join(c.Allow, ", "))
	err = srv.Serve(ln)
	stop()
	<-webDone
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	log.Print("stopped")
	return nil
}

// outsideZellijEnv drops the variables zellij sets inside its own panes.
// Started from a pane (say, `zellij-remote run` typed in one), zellij web
// would inherit ZELLIJ_SESSION_NAME, take that session for its "current"
// one, and leave it out of the web client's session list.
func outsideZellijEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if k == "ZELLIJ" || k == "ZELLIJ_SESSION_NAME" || k == "ZELLIJ_PANE_ID" {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func webPIDPath() string { return filepath.Join(home(), "zellij-web.pid") }

// zellijUp reports whether something answers HTTP on zellij web's port.
func zellijUp(port int) bool {
	cl := &http.Client{Timeout: 2 * time.Second}
	res, err := cl.Get(fmt.Sprintf("http://127.0.0.1:%d/info/version", port))
	if err != nil {
		return false
	}
	res.Body.Close()
	return true
}

// lockRun makes sure only one zellij-remote serves at a time; two would
// fight over the same tailnet identity and zellij web's port.
func lockRun() (release func(), err error) {
	if err := os.MkdirAll(home(), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(home(), "run.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("zellij-remote is already running (check `zellij-remote status`)")
	}
	f.Truncate(0)
	fmt.Fprintf(f, "%d\n", os.Getpid())
	return func() { f.Close() }, nil
}

// ---- start / stop / status ----

const unitName = "zellij-remote"

func logPath() string { return filepath.Join(home(), "zellij-remote.log") }

// unit describes zellij-remote as a background service: `zellij-remote run`,
// which starts zellij web itself.
func unit() (service.Unit, error) {
	bin, err := service.Executable()
	if err != nil {
		return service.Unit{}, err
	}
	if _, err := exec.LookPath("zellij"); err != nil {
		return service.Unit{}, errors.New("zellij isn't on PATH")
	}
	// Service managers start programs with a bare environment. Keep what
	// zellij needs to find its config, start the right shell, and speak
	// UTF-8, plus this shell's PATH so zellij and the shells it starts
	// find things.
	env := [][2]string{{"ZELLIJ_REMOTE_HOME", home()}}
	for _, k := range []string{"PATH", "SHELL", "LANG", "LC_ALL", "LC_CTYPE", "ZELLIJ_CONFIG_DIR", "ZELLIJ_CONFIG_FILE", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
		if v, ok := os.LookupEnv(k); ok && v != "" {
			env = append(env, [2]string{k, v})
		}
	}
	return service.Unit{
		Name: unitName, Desc: "zellij-remote: zellij web over Tailscale",
		Args: []string{bin, "run"}, Env: env, Log: logPath(),
	}, nil
}

func cmdStart() error {
	c, err := loadConfig()
	if err != nil {
		return err
	}
	if !tunnel.Joined(tailscaleDir()) {
		return errors.New("this machine hasn't joined the tailnet yet; run `zellij-remote setup`")
	}
	m, err := service.ForOS()
	if err != nil {
		return err
	}
	external := zellijUp(c.Port) && !m.State(unitName).Running
	u, err := unit()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(home(), 0o700); err != nil {
		return err
	}
	// Create the log ourselves so it's 0600; launchd and systemd would
	// create it world-readable. It holds no secrets, but who connected when
	// is nobody else's business.
	if f, err := os.OpenFile(logPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		f.Close()
		os.Chmod(logPath(), 0o600)
	}
	if err := m.Install([]service.Unit{u}); err != nil {
		return err
	}
	fmt.Printf("started (%s). It runs at login and restarts if it crashes.\n", m.Kind())
	fmt.Printf("  url: %s\n  log: %s\n", c.URL, logPath())
	if external {
		fmt.Printf("note: a zellij web you started is already on 127.0.0.1:%d, so zellij-remote uses it\n"+
			"      and won't restart it if it stops. To hand it over: zellij web --stop, then zellij-remote start.\n", c.Port)
	}
	lingerHint(m)
	return nil
}

func cmdStop() error {
	m, err := service.ForOS()
	if err != nil {
		return err
	}
	if err := m.Remove([]string{unitName}); err != nil {
		return err
	}
	fmt.Println("stopped, and removed from login. `zellij-remote start` brings it back.")
	return nil
}

func cmdStatus() error {
	m, err := service.ForOS()
	if err != nil {
		return err
	}
	st := m.State(unitName)
	if st.Installed {
		fmt.Printf("service:    %s (pid %s)\n", st.Detail, st.PID)
	} else {
		fmt.Println("service:    not installed (`zellij-remote start` installs it)")
	}
	c, cerr := loadConfig()
	if cerr != nil {
		fmt.Println("url:       ", cerr)
	} else {
		fmt.Println("url:       ", c.URL)
		fmt.Println("allowed:   ", strings.Join(c.Allow, ", "))
		web := "not answering"
		if zellijUp(c.Port) {
			web = "answering"
			if _, err := os.Stat(webPIDPath()); err == nil {
				web += ", started by zellij-remote"
			} else {
				web += ", started outside zellij-remote"
			}
		}
		fmt.Printf("zellij web: http://127.0.0.1:%d (%s)\n", c.Port, web)
	}
	lingerHint(m)
	if lines := tail(logPath(), 8); len(lines) > 0 {
		fmt.Printf("\nlast lines of %s:\n  %s\n", logPath(), strings.Join(lines, "\n  "))
	}
	return nil
}

func lingerHint(m service.Manager) {
	if runtime.GOOS == "linux" && m.Kind() == "systemd" && !service.Lingering() {
		fmt.Println("note: lingering is off, so zellij-remote stops when you log out. To keep it running:")
		fmt.Println("        loginctl enable-linger $USER")
	}
}

func tail(path string, n int) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}
