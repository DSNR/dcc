#!/bin/sh
# Downloads the latest dcc-cli or dcc-gui release binary for this machine's
# OS/arch and installs it to ~/.local/bin.
#
# Usage: ./install.sh [cli|gui]   (default: cli)
set -eu

client=${1:-cli}
case "$client" in
	cli|gui) ;;
	*)
		echo "usage: $0 [cli|gui]" >&2
		exit 1
		;;
esac

os=$(uname -s)
case "$os" in
	Linux) platform=linux-amd64; ext="" ;;
	MINGW*|MSYS*|CYGWIN*) platform=windows-amd64; ext=".exe" ;;
	*)
		echo "unsupported OS: $os (dcc ships Linux and Windows builds only)" >&2
		exit 1
		;;
esac

repo="DSNR/dcc"
asset="dcc-$client-$platform$ext"
dest_dir="$HOME/.local/bin"
dest="$dest_dir/dcc-$client$ext"

mkdir -p "$dest_dir"

url="https://github.com/$repo/releases/latest/download/$asset"
echo "Downloading $asset..."
curl -fL -o "$dest" "$url"
chmod +x "$dest"

echo "Installed to $dest"
case ":$PATH:" in
	*":$dest_dir:"*) ;;
	*) echo "Add $dest_dir to your PATH to run it as dcc-$client." ;;
esac
