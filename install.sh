#!/bin/sh
# Installs the latest minitest-connector release (or $MINITEST_CONNECTOR_VERSION, e.g. v0.1.0).
#   curl -fsSL https://raw.githubusercontent.com/minitap-ai/minitest-connector/main/install.sh | sh
# Installs to /usr/local/bin when writable (or run with sudo), else ~/.local/bin.
# Override with INSTALL_DIR=/some/dir.
set -eu

repo="minitap-ai/minitest-connector"
name="minitest-connector"

fail() { echo "error: $*" >&2; exit 1; }

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) fail "unsupported OS: $(uname -s) (macOS and Linux only; use Docker elsewhere)" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) fail "unsupported architecture: $(uname -m)" ;;
esac

version="${MINITEST_CONNECTOR_VERSION:-latest}"
if [ "$version" = latest ]; then
  base="https://github.com/$repo/releases/latest/download"
else
  base="https://github.com/$repo/releases/download/$version"
fi
archive="${name}_${os}_${arch}.tar.gz"

if [ -n "${INSTALL_DIR:-}" ]; then
  dir="$INSTALL_DIR"
elif [ -w /usr/local/bin ]; then
  dir=/usr/local/bin
else
  dir="$HOME/.local/bin"
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "Downloading $archive ($version)…"
curl -fsSL "$base/$archive" -o "$tmp/$archive" || fail "download failed: $base/$archive"
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt" || fail "download failed: $base/checksums.txt"

expected="$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/checksums.txt")"
[ -n "$expected" ] || fail "$archive not listed in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$tmp/$archive" | awk '{ print $1 }')"
else
  actual="$(shasum -a 256 "$tmp/$archive" | awk '{ print $1 }')"
fi
[ "$expected" = "$actual" ] || fail "checksum mismatch for $archive"

tar -xzf "$tmp/$archive" -C "$tmp" "$name"
mkdir -p "$dir"
install -m 0755 "$tmp/$name" "$dir/$name"

echo "Installed $("$dir/$name" version) to $dir/$name"
case ":$PATH:" in
  *":$dir:"*) ;;
  *) echo "Add $dir to your PATH to run $name." ;;
esac
echo "Next: sudo $dir/$name service install --token <token> --allow '<domains>'"
