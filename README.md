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
 phone / laptop ══WireGuard══▶ zellij-remote ──loopback──▶ zellij web
 (Tailscale app)   + HTTPS     its own tailnet device,      127.0.0.1:8082
                               :443 only, allowlisted       (started and kept
                               logins only                   running by zellij-remote)
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
- **Only you.** Every request is checked against the tailnet's own record
  of who sent it. Logins not on your allowlist, and tagged devices, get a
  403 before anything reaches zellij, even if your ACLs allow more.
- **One program to run.** zellij-remote starts `zellij web` itself and
  restarts it if it crashes. `zellij-remote start` runs the lot at login,
  under launchd on macOS or systemd `--user` on Linux.
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
zellij-remote setup --name "my laptop" --allow you@example.com
zellij-remote start
```

That's all. `setup`:

1. Asks for the auth key. Input is hidden; `TS_AUTHKEY` or a pipe works
   too. The key is never saved, never logged, and never passed on a command
   line.
2. Joins the tailnet, fetches the HTTPS certificate, and prints the URL.
3. Creates a zellij login token and prints it **once**. zellij keeps only a
   hash of it, and zellij-remote doesn't keep it at all. Put it in your
   password manager, then clear your terminal's scrollback.

`--allow` takes your Tailscale login (comma-separate several). Only those
people's own devices get through. Without it, setup asks.

`start` installs one background service that runs at login and restarts on
a crash. It starts `zellij web` on 127.0.0.1 itself. If you already run a
`zellij web` on that port, zellij-remote uses yours, but won't restart it if
it stops; `zellij web --stop` before `start` hands it over.

```bash
zellij-remote status              # running?, URL, allowlist, last log lines
zellij-remote stop                # stop, and remove from login
zellij-remote token               # another login token
zellij-remote token --read-only   # a token that can only watch sessions
```

The device name is part of the URL, so running setup again keeps the name
it already has. Running it again with `--allow` changes the allowlist
(restart with `zellij-remote start` to apply). `--port` moves zellij web
off the default 8082.

**Linux:** systemd stops user services when your last session ends, SSH
sessions included. To keep zellij-remote running after you log out, turn on
lingering once:

```bash
loginctl enable-linger $USER
```

To run it under a supervisor of your own instead, use `zellij-remote run`,
which does the same in the foreground.

## 3. Open it

On your phone or another computer:

1. Install the Tailscale app and sign in to the same tailnet **as
   yourself**. Don't use the auth key from 1c, and don't tag the device:
   a tagged device no longer counts as you, and the grant won't let it in.
2. Open the URL that `setup` printed, and paste the login token.

`/` shows your sessions. `/<name>` attaches to a session, or creates it if
it doesn't exist yet.

## Security

### What's encrypted

| Hop | Protection |
|---|---|
| your device → this machine | WireGuard (Tailscale), and inside it HTTPS with the device's `*.ts.net` certificate. TLS ends inside the zellij-remote process; Tailscale's servers never see plaintext. |
| zellij-remote → zellij web | plain HTTP over loopback (`127.0.0.1`). It never leaves the machine. |

The zellij token doesn't encrypt anything. It's a login credential:
zellij exchanges it for a session cookie, and the encryption above
protects both.

### Who gets in

A request has to pass all of these, in order:

1. **Tailscale ACLs:** your device must be on the tailnet and allowed to
   reach `tag:zellij` on 443.
2. **The allowlist:** zellij-remote asks the tailnet who is connecting
   (from its own coordination data, not anything the client sends). It
   refuses logins not on `--allow` and tagged devices. This holds even if
   your ACL is the default allow-all.
3. **The Origin check:** a request whose `Origin` isn't zellij-remote's own
   URL gets a 403. zellij 0.45 doesn't check `Origin` on its WebSockets,
   and its `SameSite=Strict` cookie counts every `*.<tailnet>.ts.net`
   host as the same site. Without this check, a page served by another
   device on your tailnet could open a terminal using your logged-in
   browser.
4. **A zellij login token.** Then zellij's session cookie, which
   zellij-remote marks `Secure`, with HSTS on every response.

Refusals, login attempts, and every terminal opened (who, from where) go to
`~/.zellij-remote/zellij-remote.log`, which is mode 0600. Headers, cookies,
tokens and query strings are never logged.

### Handling tokens

- **A full token is a shell on this machine.** Whoever holds one can run
  commands as you, and visiting `/<any-name>` creates a new session. Keep
  tokens in a password manager. Give a device that only needs to watch a
  `zellij-remote token --read-only` token.
- Tokens are printed once and stored nowhere by zellij-remote; zellij
  stores only a hash. Revoke ones you no longer need:
  ```bash
  zellij web --list-tokens
  zellij web --revoke-token <name>      # or --revoke-all-tokens
  ```
- On a shared machine, other local users can reach `127.0.0.1:8082`
  directly, skipping the allowlist (they'd still need a token). zellij web
  can't listen on a unix socket, so use zellij-remote on single-user
  machines.

### Keeping it private

- **Not on the public internet.** Don't put this behind
  [Funnel](https://tailscale.com/kb/1223/funnel). zellij's login has no
  rate limiting.
- **Cut it off** by removing the device under **Machines** in the admin
  console. To start over, run `zellij-remote stop`, delete
  `~/.zellij-remote/tailscale`, generate a new key, and run setup again.
- [Tailnet Lock](https://tailscale.com/kb/1226/tailnet-lock) requires every
  new device to be signed by one of your trusted devices before it can
  join. That stops Tailscale's coordination server from adding a device on
  its own.
- The device name and your tailnet's name are public through Certificate
  Transparency logs (see 1a).

## Files

| Path | What |
|---|---|
| `~/.zellij-remote/` | all state, mode 0700 |
| `~/.zellij-remote/config.json` | name, device name, URL, port, allowlist (0600) |
| `~/.zellij-remote/tailscale/` | the device's tailnet identity, its private key included |
| `~/.zellij-remote/zellij-remote.log` | the service's log, zellij web's output included (0600) |
| `~/Library/LaunchAgents/com.github.pa.zellij-remote.plist` | macOS |
| `~/.config/systemd/user/zellij-remote.service` | Linux |

No tokens or keys are stored anywhere in it.

`ZELLIJ_REMOTE_HOME` moves `~/.zellij-remote` elsewhere.

## Development

```bash
git config core.hooksPath .githooks   # once: scan every commit for secrets
go test ./...
```

`scripts/check-secrets.sh` refuses commits that contain anything shaped
like a Tailscale key, a zellij token or session cookie, or a private key.
CI runs it over the whole history. A test that needs a token-shaped string
uses `00000000-0000-4000-8000-000000000000`.

One test drives a real zellij web through the proxy: it logs in, opens a
session, checks that a cross-origin WebSocket is refused, and reads
terminal output over the WebSocket. It runs only when you point it at a
zellij web and give it a token:

```bash
zellij web --port 18082 &
zellij web --create-token        # note the token's name, revoke it after
ZELLIJ_IT_URL=http://127.0.0.1:18082 ZELLIJ_IT_TOKEN=<token> \
  go test ./internal/proxy -run RealZellij -v
zellij web --revoke-token <name>
```

## License

MIT
