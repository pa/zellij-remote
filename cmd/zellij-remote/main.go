// Command zellij-remote serves zellij's web client to your tailnet, and
// nothing else on this machine.
//
//	zellij-remote setup [--name <n>] [--port <p>]   join the tailnet once
//	zellij-remote run                               serve in the foreground
//	zellij-remote start | stop | status             run zellij web and the proxy
//	                                                in the background (launchd on
//	                                                macOS, systemd --user on Linux)
//
// It embeds tsnet, joins the tailnet as its own device (zellij-<name>,
// tagged tag:zellij), listens on :443 with the device's *.ts.net
// certificate, and reverse-proxies to zellij web on 127.0.0.1.
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
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/pa/zellij-remote/internal/proxy"
	"github.com/pa/zellij-remote/internal/service"
	"github.com/pa/zellij-remote/internal/tunnel"
)

// version is set at release time with -ldflags "-X main.version=...".
var version = "dev"

const defaultPort = 8082

const usage = `zellij-remote serves zellij's web client to your tailnet, and nothing else.

usage:
  zellij-remote setup [--name <n>] [--port <p>]  join the tailnet (once) and print the URL
  zellij-remote run                              serve in the foreground
  zellij-remote start | stop | status            run zellij web and the proxy in the
                                                 background (at login, restarted on crash)
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	if _, err := exec.LookPath("zellij"); err != nil {
		return errors.New("zellij isn't on PATH; install zellij 0.43 or newer first (https://zellij.dev)")
	}
	display, host := tailnetName(*name)
	ts := &tunnel.Tailscale{Dir: tailscaleDir(), Hostname: host, Logf: func(f string, a ...any) { fmt.Printf(f+"\n", a...) }}
	if !tunnel.Joined(ts.Dir) {
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
	if err := writeConfig(config{Name: display, Hostname: host, URL: u, Port: *port}); err != nil {
		return err
	}
	fmt.Printf(`
joined. Other devices on your tailnet will reach zellij at
    %s

Next:
  1. zellij web --create-token       a login token for the web client (shown once)
     zellij web --create-read-only-token   for a device that should only watch
  2. zellij-remote start             run zellij web and the proxy in the background
`, u)
	return nil
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

Put your own Tailscale login in "src". A zellij login is a shell on this
machine, so keep the grant to yourself. If the file already has a "grants"
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

func cmdRun() error {
	c, err := loadConfig()
	if err != nil {
		return err
	}
	release, err := lockRun()
	if err != nil {
		return err
	}
	defer release()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ts := &tunnel.Tailscale{Dir: tailscaleDir(), Hostname: c.Hostname, Logf: log.Printf}
	ln, u, err := ts.Listen(ctx)
	if err != nil {
		return err
	}
	defer ts.Close()
	if u != c.URL {
		// The tailnet was renamed, or the device was; keep the config honest.
		c.URL = u
		writeConfig(c)
	}

	target := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(c.Port))}
	if !zellijUp(c.Port) {
		log.Printf("zellij web isn't answering on %s yet; requests get a 502 until it is", target.Host)
	}
	srv := &http.Server{
		Handler:           proxy.New(target, u, log.Default()),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	log.Printf("serving %s -> %s", u, target)
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	log.Print("stopped")
	return nil
}

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

// lockRun makes sure only one proxy runs at a time; two would fight over
// the same tailnet identity.
func lockRun() (release func(), err error) {
	if err := os.MkdirAll(home(), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(home(), "proxy.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another zellij-remote proxy is already running (check `zellij-remote status`)")
	}
	f.Truncate(0)
	fmt.Fprintf(f, "%d\n", os.Getpid())
	return func() { f.Close() }, nil
}

// ---- start / stop / status ----

func logPath(name string) string { return filepath.Join(home(), name+".log") }

// units describes the two background programs: zellij web in the
// foreground (so the service manager supervises it, rather than
// --daemonize), and this binary's `run`.
func units(c config) ([]service.Unit, error) {
	bin, err := service.Executable()
	if err != nil {
		return nil, err
	}
	zj, err := exec.LookPath("zellij")
	if err != nil {
		return nil, errors.New("zellij isn't on PATH")
	}
	if zj, err = filepath.Abs(zj); err != nil {
		return nil, err
	}
	// Service managers start programs with a bare environment. Keep what
	// zellij needs to find its config, start the right shell, and speak
	// UTF-8, plus this shell's PATH so the shells it starts find things.
	var env [][2]string
	for _, k := range []string{"PATH", "SHELL", "LANG", "LC_ALL", "LC_CTYPE", "ZELLIJ_CONFIG_DIR", "ZELLIJ_CONFIG_FILE", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
		if v, ok := os.LookupEnv(k); ok && v != "" {
			env = append(env, [2]string{k, v})
		}
	}
	return []service.Unit{
		{
			Name: "web", Desc: "zellij web, for zellij-remote",
			Args: []string{zj, "web", "--ip", "127.0.0.1", "--port", strconv.Itoa(c.Port)},
			Env:  env, Log: logPath("web"),
		},
		{
			Name: "proxy", Desc: "zellij-remote: zellij web over Tailscale",
			Args:  []string{bin, "run"},
			Env:   [][2]string{{"PATH", os.Getenv("PATH")}, {"ZELLIJ_REMOTE_HOME", home()}},
			Log:   logPath("proxy"),
			After: "web",
		},
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
	// A zellij web that isn't ours (say, `zellij web --daemonize`) would keep
	// ours from binding the port, and it would restart forever.
	if zellijUp(c.Port) && !m.State("web").Running {
		return fmt.Errorf("something already answers on 127.0.0.1:%d, probably a zellij web you started; stop it with `zellij web --stop` (or set another --port in setup) and try again", c.Port)
	}
	us, err := units(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(home(), 0o700); err != nil {
		return err
	}
	if err := m.Install(us); err != nil {
		return err
	}
	fmt.Printf("started zellij web and the proxy (%s). They run at login and restart if they crash.\n", m.Kind())
	fmt.Printf("  url:  %s\n  logs: %s\n        %s\n", c.URL, logPath("web"), logPath("proxy"))
	lingerHint(m)
	return nil
}

func cmdStop() error {
	m, err := service.ForOS()
	if err != nil {
		return err
	}
	if err := m.Remove([]string{"proxy", "web"}); err != nil {
		return err
	}
	fmt.Println("stopped zellij web and the proxy, and removed them from login. `zellij-remote start` brings them back.")
	return nil
}

func cmdStatus() error {
	m, err := service.ForOS()
	if err != nil {
		return err
	}
	c, cerr := loadConfig()
	for _, n := range []string{"web", "proxy"} {
		st := m.State(n)
		label := map[string]string{"web": "zellij web", "proxy": "proxy"}[n]
		if !st.Installed {
			fmt.Printf("%-11s not installed (`zellij-remote start` installs it)\n", label+":")
			continue
		}
		fmt.Printf("%-11s %s (pid %s)\n", label+":", st.Detail, st.PID)
	}
	if cerr != nil {
		fmt.Println("url:       ", cerr)
	} else {
		fmt.Println("url:       ", c.URL)
		reach := "answering"
		if !zellijUp(c.Port) {
			reach = "not answering"
		}
		fmt.Printf("local:      http://127.0.0.1:%d (%s)\n", c.Port, reach)
	}
	lingerHint(m)
	for _, n := range []string{"web", "proxy"} {
		if lines := tail(logPath(n), 5); len(lines) > 0 {
			fmt.Printf("\nlast lines of %s:\n  %s\n", logPath(n), strings.Join(lines, "\n  "))
		}
	}
	return nil
}

func lingerHint(m service.Manager) {
	if runtime.GOOS == "linux" && m.Kind() == "systemd" && !service.Lingering() {
		fmt.Println("note: lingering is off, so these stop when you log out. To keep them running:")
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
