#!/bin/sh
set -eu

root="$(mktemp -d)"
cleanup() { rm -rf "$root"; }
trap cleanup EXIT INT TERM

version="v0.0.1"
asset_version="0.0.1"
case "$(uname -s)" in
  Darwin) os="darwin" ;;
  Linux) os="linux" ;;
  *) echo "unsupported OS for fixture test" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *) echo "unsupported arch for fixture test" >&2; exit 1 ;;
esac

release="$root/release/$version"
payload="$root/payload"
bin="$root/bin"
archive="clex_${asset_version}_${os}_${arch}.tar.gz"

mkdir -p "$release" "$payload" "$bin"
printf '#!/bin/sh\necho clex fixture\n' > "$payload/clex"
printf '#!/bin/sh\necho clexd fixture\n' > "$payload/clexd"
chmod +x "$payload/clex" "$payload/clexd"
tar -C "$payload" -czf "$release/$archive" clex clexd

if command -v sha256sum >/dev/null 2>&1; then
  (cd "$release" && sha256sum "$archive" > checksums.txt)
else
  (cd "$release" && shasum -a 256 "$archive" > checksums.txt)
fi

CLEX_INSTALL_VERSION="$version" \
CLEX_INSTALL_BASE_URL="file://$release" \
CLEX_INSTALL_DIR="$bin" \
  sh "$(dirname "$0")/../install.sh" >/dev/null

"$bin/clex" | grep -q "clex fixture"
"$bin/clexd" | grep -q "clexd fixture"
echo "install fixture passed"
