## What and why

<!-- What does this change, and why? Link an issue if there is one. -->

## Kind

- [ ] Bug fix
- [ ] Feature
- [ ] Security hardening
- [ ] Docs
- [ ] CI / build / dependencies

## Testing

CI runs gofmt, vet, `go test -race` on Linux and macOS, a real zellij web
through the proxy, cross-builds, govulncheck, and a secret scan of the
whole history. Tick what you ran beyond that:

- [ ] `go test -race ./...` locally
- [ ] Against a real zellij web (README: Local testing, level 2)
- [ ] End to end on a tailnet (level 3). Needed when this touches
      `internal/tunnel`, `internal/proxy`, setup or run.
- [ ] The background service on macOS (level 4). Needed when this touches
      `internal/service`, `internal/supervise`, start, stop or status.
- [ ] The background service on Linux / systemd (level 4)
- [ ] Not needed; say why:

## Security

zellij-remote stands between the tailnet and a shell, so every PR answers
these:

- [ ] No secrets in the diff or in any commit: no auth keys, tokens,
      cookies, `tailscaled.state`. The pre-commit hook is on
      (`git config core.hooksPath .githooks`).
- [ ] Nothing new logs headers, cookies, tokens or query strings.
- [ ] Every request still goes through the allowlist, then the Origin
      check, before reaching zellij. Nothing new is reachable on the
      tailnet beyond :443.
- [ ] Files under `~/.zellij-remote` keep their modes (dir 0700, files
      0600).
- [ ] New dependencies are needed, from known sources, and `govulncheck`
      is clean.
- [ ] Not applicable (docs or CI only).

## Docs

- [ ] README updated (setup, security, troubleshooting, local testing),
      or nothing user-facing changed.
