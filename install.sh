#!/bin/sh
# Install Signet on a Mac or Linux:  curl -fsSL https://raw.githubusercontent.com/kahlkritzinger/signet/main/install.sh | sh
# Puts one file, `signet`, in ~/.local/bin (or $SIGNET_BIN). Nothing else is installed.
set -eu
repo=kahlkritzinger/signet
bin=${SIGNET_BIN:-$HOME/.local/bin}
os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "signet: no build for $(uname -m) yet; try: go install github.com/$repo@latest" >&2; exit 1 ;;
esac
case "$os" in darwin|linux) ;; *) echo "signet: on Windows use install.ps1" >&2; exit 1 ;; esac
url="https://github.com/$repo/releases/latest/download/signet-$os-$arch"
mkdir -p "$bin"
tmp=$(mktemp)
echo "Downloading $url"
curl -fsSL "$url" -o "$tmp"
sums=$(curl -fsSL "https://github.com/$repo/releases/latest/download/checksums.txt")
want=$(printf '%s\n' "$sums" | awk -v f="signet-$os-$arch" '$2 == f {print $1}')
have=$( (sha256sum "$tmp" 2>/dev/null || shasum -a 256 "$tmp") | cut -d' ' -f1)
[ -n "$want" ] && [ "$want" = "$have" ] || { rm -f "$tmp"; echo "signet: checksum mismatch, not installed" >&2; exit 1; }
chmod +x "$tmp" && mv "$tmp" "$bin/signet"
echo "Installed $bin/signet"
case ":$PATH:" in *":$bin:"*) ;; *) echo "Add $bin to your PATH, e.g.: echo 'export PATH=\"$bin:\$PATH\"' >> ~/.profile" ;; esac
echo "Next: signet set-default   (PDFs open in Signet)   or   signet some.pdf"
