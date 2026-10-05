#!/bin/sh
# Refuse to let secrets into the repo: Tailscale auth/API keys, zellij login
# tokens and session cookies (UUIDs), private keys, tsnet state.
#
#   scripts/check-secrets.sh            check what's staged (the pre-commit hook)
#   scripts/check-secrets.sh --history  check every commit (CI)
#
# Tests that need a token-shaped string use 00000000-0000-4000-8000-000000000000.
set -eu

pattern='tskey-[a-z]+-[A-Za-z0-9]{6,}|-----BEGIN [A-Z ]*PRIVATE KEY-----|[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}|"PrivateNode''Key"|privkey:[0-9a-f]{16,}'
allowed='00000000-0000-4000-8000-000000000000'

case "${1:-}" in
--history) content=$(git log -p --all --no-color --no-ext-diff) ;;
*) content=$(git diff --cached --no-color --no-ext-diff -U0 | grep '^+' || true) ;;
esac

hits=$(printf '%s\n' "$content" | grep -En "$pattern" | grep -v "$allowed" || true)
if [ -n "$hits" ]; then
	echo "check-secrets: possible secret found; nothing was committed." >&2
	echo "$hits" | sed -E 's/([0-9a-fA-F]{8})-[0-9a-fA-F-]{27}/\1-…(redacted)/g; s/(tskey-[a-z]+-)[A-Za-z0-9]+/\1…(redacted)/g' | head -20 >&2
	exit 1
fi
