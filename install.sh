#!/bin/sh
# flashcart installer, for the Batocera box.
#   curl -sSL https://raw.githubusercontent.com/adamcarlile/flashcart/main/install.sh | sh
set -eu

REPO="adamcarlile/flashcart"
INSTALL_DIR="/userdata/system/flashcart"
BIN="$INSTALL_DIR/flashcart"
CFG="$INSTALL_DIR/flashcart.toml"

case "$(uname -m)" in
    x86_64)  ARCH=amd64 ;;
    aarch64) ARCH=arm64 ;;
    *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac
ASSET="flashcart_linux_${ARCH}"

echo "==> fetching the latest release"
TAG=$(wget -qO- "https://api.github.com/repos/$REPO/releases/latest" \
    | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1)
[ -n "$TAG" ] || { echo "could not determine the latest release" >&2; exit 1; }
echo "    $TAG"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

BASE="https://github.com/$REPO/releases/download/$TAG"
wget -qO "$TMP/$ASSET" "$BASE/$ASSET"
wget -qO "$TMP/checksums.txt" "$BASE/checksums.txt"

echo "==> verifying checksum"
WANT=$(grep " $ASSET\$" "$TMP/checksums.txt" | awk '{print $1}')
GOT=$(sha256sum "$TMP/$ASSET" | awk '{print $1}')
[ "$WANT" = "$GOT" ] || { echo "checksum mismatch: got $GOT, want $WANT" >&2; exit 1; }

echo "==> installing to $INSTALL_DIR"
mkdir -p "$INSTALL_DIR"
# Install to a temporary name in the SAME directory as $BIN, then rename
# into place. A rename within one directory is atomic, so a power loss
# mid-write (this box is switched off at the wall, not shut down) can never
# leave a truncated binary at $BIN. Installing straight to $BIN would not
# have that property. Staying in the same directory matters: a cross-
# filesystem "mv" is a copy, not a rename, and loses the atomicity.
NEW_BIN="$BIN.new"
install -m 0755 "$TMP/$ASSET" "$NEW_BIN"
mv "$NEW_BIN" "$BIN"

if [ ! -f "$CFG" ]; then
    echo "==> writing a default config to $CFG"
    cat > "$CFG" <<'TOML'
[nas]
host = "10.132.1.25"

[server]
listen = ":8474"

[trees.roms]
export = "/volume2/retrogaming/roms"
local = "/userdata/roms"

[trees.bios]
export = "/volume2/retrogaming/bios"
local = "/userdata/bios"

[trees.saves]
export = "/volume2/retrogaming/saves"
local = "/userdata/saves"
TOML
    echo "    review it before starting the service"
else
    echo "==> keeping the existing config at $CFG"
fi

echo "==> installing the service"
"$BIN" install-service

echo
echo "flashcart $TAG installed."
echo "UI: http://$(hostname):8474"
echo "Update later with: $BIN update"
