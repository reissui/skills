#!/bin/sh
set -eu

repo="${CLEX_INSTALL_REPO:-reissui/clex}"
version="${CLEX_INSTALL_VERSION:-latest}"
install_dir="${CLEX_INSTALL_DIR:-}"

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "install.sh: missing required command: $1" >&2
    exit 1
  }
}

need curl
need tar

case "$(uname -s)" in
  Darwin) os="darwin" ;;
  Linux) os="linux" ;;
  *) echo "install.sh: unsupported OS $(uname -s)" >&2; exit 1 ;;
esac

case "$(uname -m)" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *) echo "install.sh: unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac

if [ "$version" = "latest" ]; then
  latest_json="$(curl -fsSL "https://api.github.com/repos/$repo/releases/latest")"
  version="$(printf '%s\n' "$latest_json" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"
  if [ -z "$version" ]; then
    echo "install.sh: could not resolve latest release for $repo" >&2
    exit 1
  fi
fi

asset_version="${version#v}"
archive="clex_${asset_version}_${os}_${arch}.tar.gz"
base_url="${CLEX_INSTALL_BASE_URL:-https://github.com/$repo/releases/download/$version}"

if [ -z "$install_dir" ]; then
  if [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
    install_dir="/usr/local/bin"
  else
    install_dir="$HOME/.local/bin"
  fi
fi

tmp="$(mktemp -d)"
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT INT TERM

curl -fsSL "$base_url/$archive" -o "$tmp/$archive"
curl -fsSL "$base_url/checksums.txt" -o "$tmp/checksums.txt"

if command -v sha256sum >/dev/null 2>&1; then
  checker="sha256sum -c"
elif command -v shasum >/dev/null 2>&1; then
  checker="shasum -a 256 -c"
else
  echo "install.sh: missing sha256sum or shasum" >&2
  exit 1
fi

grep "  $archive\$" "$tmp/checksums.txt" > "$tmp/checksums.one" || {
  echo "install.sh: checksum for $archive not found in checksums.txt" >&2
  exit 1
}
(cd "$tmp" && $checker checksums.one >/dev/null)

tar -xzf "$tmp/$archive" -C "$tmp"
mkdir -p "$install_dir"
install -m 0755 "$tmp/clex" "$install_dir/clex"
install -m 0755 "$tmp/clexd" "$install_dir/clexd"

echo "installed clex $version to $install_dir"
