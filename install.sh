#!/bin/sh
# roost installer: detects OS/arch, downloads the latest release, and
# installs the binary into /usr/local/bin.
set -eu

REPO="cdrrazan/roost"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  darwin|linux) ;;
  *) echo "unsupported OS: $os" >&2; exit 1 ;;
esac

arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "unsupported architecture: $arch" >&2; exit 1 ;;
esac

tag=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
  | grep '"tag_name"' | head -1 | cut -d'"' -f4)
if [ -z "$tag" ]; then
  echo "could not determine the latest release of $REPO" >&2
  exit 1
fi

url="https://github.com/$REPO/releases/download/$tag/roost_${os}_${arch}.tar.gz"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "downloading roost $tag ($os/$arch)..."
curl -fsSL "$url" | tar -xz -C "$tmp"

if [ -w "$INSTALL_DIR" ]; then
  install -m 0755 "$tmp/roost" "$INSTALL_DIR/roost"
else
  echo "installing to $INSTALL_DIR (needs sudo)"
  sudo install -m 0755 "$tmp/roost" "$INSTALL_DIR/roost"
fi

echo "installed: $("$INSTALL_DIR/roost" version)"
echo "note: roost needs Docker running; cloudflared runs as a container."
echo "next: roost init"
