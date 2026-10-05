#!/bin/sh
# Install the zellij-remote release binary for this OS and CPU, checked
# against the release's checksums.txt.
#
#   curl -fsSL https://raw.githubusercontent.com/pa/zellij-remote/main/scripts/install.sh | sh
#
# ZELLIJ_REMOTE_VERSION=v0.1.0   a specific release (default: the latest)
# ZELLIJ_REMOTE_INSTALL_DIR=...  where to put it (default: ~/.local/bin)
#
# Later versions install with `zellij-remote upgrade`.
set -eu

repo=https://github.com/pa/zellij-remote
dir=${ZELLIJ_REMOTE_INSTALL_DIR:-$HOME/.local/bin}

fail() { echo "install: $*" >&2; exit 1; }

case $(uname -s) in
Darwin) os=darwin ;;
Linux) os=linux ;;
*) fail "unsupported OS $(uname -s); build from source instead" ;;
esac
case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) fail "unsupported CPU $(uname -m); build from source instead" ;;
esac

v=${ZELLIJ_REMOTE_VERSION:-}
if [ -z "$v" ]; then
	v=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$repo/releases/latest") || fail "can't reach GitHub"
	v=${v##*/}
	case $v in v*) ;; *) fail "no release found" ;; esac
fi

f=zellij-remote_${v}_${os}_${arch}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "Downloading zellij-remote $v for $os/$arch"
curl -fsSL -o "$tmp/$f.tar.gz" "$repo/releases/download/$v/$f.tar.gz" || fail "download failed: $f.tar.gz"
curl -fsSL -o "$tmp/checksums.txt" "$repo/releases/download/$v/checksums.txt" || fail "download failed: checksums.txt"

want=$(awk -v f="$f.tar.gz" '$2 == f { print $1 }' "$tmp/checksums.txt")
[ -n "$want" ] || fail "$f.tar.gz isn't in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
	got=$(sha256sum "$tmp/$f.tar.gz" | awk '{ print $1 }')
else
	got=$(shasum -a 256 "$tmp/$f.tar.gz" | awk '{ print $1 }')
fi
[ "$got" = "$want" ] || fail "checksum mismatch for $f.tar.gz"

tar -xzf "$tmp/$f.tar.gz" -C "$tmp"
mkdir -p "$dir"
mv "$tmp/$f/zellij-remote" "$dir/zellij-remote"
echo "Installed $dir/zellij-remote"

case ":$PATH:" in
*":$dir:"*) ;;
*) echo "Add $dir to your PATH:  export PATH=\"$dir:\$PATH\"" ;;
esac
