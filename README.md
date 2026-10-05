<p align="center">
  <img src="docs/logo.png" alt="zellij-remote" width="160">
</p>

<h1 align="center">zellij-remote</h1>

<p align="center">
  Your <a href="https://zellij.dev">zellij</a> sessions in a browser, on any device in your tailnet.
  <br>Nothing else on the machine becomes reachable.
</p>

---

`zellij-remote` is one Go binary. It puts
[zellij's web client](https://zellij.dev/documentation/web-client.html) on
your [Tailscale](https://tailscale.com) tailnet at
`https://zellij-<name>.<tailnet>.ts.net`. Open that address from your phone
or another laptop, log in with a zellij token, and you're in your sessions.

```
 phone / laptop ──Tailscale──▶ zellij-remote ──http──▶ zellij web
 (Tailscale app)               its own tailnet device,   127.0.0.1:8082
                               HTTPS on :443 only
```

The machine running zellij doesn't need the Tailscale app. `zellij-remote`
embeds Tailscale's Go library, [tsnet](https://tailscale.com/kb/1244/tsnet),
and joins the tailnet as a device of its own.

- **One port.** The device answers on 443 and nothing else. SSH, file
  sharing and dev servers on the machine stay unreachable from the tailnet.
  There's no VPN interface and no routes.
- **Real HTTPS.** TLS uses the device's `*.ts.net` certificate and ends
  inside `zellij-remote`. zellij itself stays on `127.0.0.1` over plain
  HTTP, where it needs no certificate.
- **Runs in the background.** `zellij-remote start` runs zellij web and the
  proxy at login and restarts them if they crash. On macOS that's launchd,
  and on Linux it's systemd `--user`.
- **Runs as you**, not root.

Supported on macOS and Linux. Needs zellij 0.43 or newer (tested with
0.45.1).

## Install

From source, with Go 1.27.1 or newer:

```bash
go install github.com/pa/zellij-remote/cmd/zellij-remote@latest
```

Or download a binary for your platform from
[Releases](https://github.com/pa/zellij-remote/releases), unpack it, and put
`zellij-remote` on your `PATH`.

## 1. Prepare the tailnet (once)

`zellij-remote setup` walks you through these steps in the terminal, so you
can follow along there instead.

All three are in the [Tailscale admin console](https://console.tailscale.com/admin)
and need an Owner or Admin of the tailnet. Do them in this order, because
the auth key's tag has to exist before you can create the key.

### 1a. Turn on MagicDNS and HTTPS certificates

1. Open **DNS** ([console.tailscale.com/admin/dns](https://console.tailscale.com/admin/dns)).
2. If MagicDNS is off, select **Enable MagicDNS**.
3. Under **HTTPS Certificates**, select **Enable HTTPS** and accept the notice.

**Every HTTPS certificate is recorded in public Certificate Transparency
logs.** So the device name `zellij-<name>` and your tailnet's name become
public. Pick a `--name` in step 2 that says nothing sensitive.

### 1b. Add the tag, and who can reach it

Open **Access controls**
([console.tailscale.com/admin/acls](https://console.tailscale.com/admin/acls)),
switch to the JSON editor, and add these next to your existing entries:

```json
{
  "tagOwners": {
    "tag:zellij": ["autogroup:admin"]
  },
  "grants": [
    {
      "src": ["you@example.com"],
      "dst": ["tag:zellij"],
      "ip":  ["tcp:443"]
    }
  ]
}
```

- Put your own Tailscale login in `src`. A zellij login is a shell on the
  machine, so grant it to yourself only, not to `autogroup:member`.
- A new tailnet's default policy lets every device reach every other on
  every port. While that allow-all rule is there, the grant above changes
  nothing. To actually restrict access, replace the allow-all rule with
  rules for what you use.
- The tagged device can't reach any of your other devices unless a rule
  says so. Nothing above does.

### 1c. Create the auth key

Open **Settings › Keys**
([console.tailscale.com/admin/settings/keys](https://console.tailscale.com/admin/settings/keys)),
select **Generate auth key**, and set:

| Setting      | Value                                                         |
|--------------|---------------------------------------------------------------|
| Reusable     | off: one key, one device                                      |
| Expiration   | 1 day: it's only needed for setup                             |
| Ephemeral    | off: an ephemeral device vanishes whenever the machine sleeps |
| Tags         | on, `tag:zellij` (missing from the list? 1b wasn't saved)     |
| Pre-approved | on, if it's shown                                             |

Copy the key (`tskey-auth-…`). It's shown only once.

Tagged devices belong to the tailnet rather than to a person, so their
node key doesn't expire.

## 2. Set up the machine

```bash
zellij-remote setup --name "my laptop"
```

Paste the auth key when asked (input is hidden), or set `TS_AUTHKEY`, or
pipe the key in. Setup joins the tailnet, fetches the HTTPS certificate, and
prints the URL. The key isn't saved. After the first join, the device's
identity lives in `~/.zellij-remote/tailscale`, which is created with mode
0700.

The name is part of the URL, so running setup again keeps the name the
device already has. `--port` changes zellij web's local port from the
default 8082.

Create a login token for the web client:

```bash
zellij web --create-token             # full access, shown once
zellij web --create-read-only-token   # can only watch existing sessions
```

Then start both programs in the background:

```bash
zellij-remote start     # zellij web + the proxy, at login, restarted on crash
zellij-remote status    # are they running, the URL, the last log lines
zellij-remote stop      # stop both and remove them from login
```

If you already run `zellij web` yourself, stop it first (`zellij web
--stop`). `start` runs its own copy in the foreground under launchd or
systemd, so a crash gets restarted.

**Linux:** systemd stops user services when your last session ends, SSH
sessions included. To keep zellij-remote running after you log out, turn on
lingering once:

```bash
loginctl enable-linger $USER
```

To run it under a supervisor of your own instead, use
`zellij-remote run`, which serves in the foreground. It expects zellij web
to already be listening on `127.0.0.1:<port>`.

## 3. Open it

On your phone or another computer:

1. Install the Tailscale app and sign in to the same tailnet **as
   yourself**. Don't use the auth key from 1c, and don't tag the device:
   a tagged device no longer counts as you, and the grant won't let it in.
2. Open the URL that `setup` printed, and paste a login token.

`/` shows your sessions. `/<name>` attaches to a session, or creates it if
it doesn't exist yet.

## Security notes

- **A zellij login token is a shell on this machine.** Anyone holding a
  regular token can run commands as you, and can open new sessions by
  visiting `/<any-name>`. Keep tokens like passwords, give watch-only
  devices a `--create-read-only-token` token instead, and revoke tokens you
  no longer use:
  ```bash
  zellij web --list-tokens
  zellij web --revoke-token <name>      # or --revoke-all-tokens
  ```
- **Two locks on the door.** To reach the login page, a device must be on
  your tailnet and allowed by your ACL grant. To get a shell, it then needs
  a zellij token.
- **Cross-origin requests are refused.** zellij 0.45 doesn't check the
  `Origin` header on its WebSockets. Its session cookie is
  `SameSite=Strict`, but every `*.<tailnet>.ts.net` host counts as the
  same site. Without a check, a web page served by any other device on your
  tailnet could open a terminal using your logged-in browser. So
  `zellij-remote` rejects (403) any request whose `Origin` isn't its own
  URL.
- **Not on the public internet.** Don't put this behind
  [Funnel](https://tailscale.com/kb/1223/funnel). zellij's login has no
  rate limiting.
- **Cutting it off.** To remove the device right away, delete it under
  **Machines** in the admin console. To start over, run
  `zellij-remote stop`, delete `~/.zellij-remote/tailscale`, generate a new
  key, and run setup again.
- [Tailnet Lock](https://tailscale.com/kb/1226/tailnet-lock) requires every
  new device to be signed by one of your trusted devices before it can
  join. That stops Tailscale's coordination server from adding a device on
  its own.

## Files

| Path | What |
|---|---|
| `~/.zellij-remote/config.json` | name, device name, URL, zellij web's port |
| `~/.zellij-remote/tailscale/` | the device's tailnet identity (0700) |
| `~/.zellij-remote/web.log`, `proxy.log` | logs of the two background programs |
| `~/Library/LaunchAgents/com.github.pa.zellij-remote.{web,proxy}.plist` | macOS |
| `~/.config/systemd/user/zellij-remote-{web,proxy}.service` | Linux |

`ZELLIJ_REMOTE_HOME` moves `~/.zellij-remote` elsewhere.

## Development

```bash
go test ./...
```

One test drives a real zellij web through the proxy: it logs in, opens a
session, checks that a cross-origin WebSocket is refused, and reads
terminal output over the WebSocket. It runs only when you point it at a
zellij web and give it a token:

```bash
zellij web --port 18082 &
zellij web --create-token
ZELLIJ_IT_URL=http://127.0.0.1:18082 ZELLIJ_IT_TOKEN=<token> \
  go test ./internal/proxy -run RealZellij -v
```

## License

MIT
