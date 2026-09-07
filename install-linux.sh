#!/usr/bin/env bash
#
# Installs the prebuilt afm binary on Linux without Homebrew.
# Downloads the archive from GitHub Releases, verifies the checksum, and puts
# the binary into /usr/local/bin (or ~/.local/bin if /usr/local is not writable).
#
# Usage:
#   ./install-linux.sh                       # latest version
#   AFM_VERSION=v0.5.70 ./install-linux.sh   # specific version
#
set -euo pipefail

REPO="akopichin/afm"
BIN_NAME="afm"

# --- Architecture ---
ARCH="$(uname -m)"
case "$ARCH" in
    x86_64)          ARCH="amd64" ;;
    aarch64 | arm64) ARCH="arm64" ;;
    *)
        echo "ERROR: unsupported architecture: $ARCH" >&2
        exit 1
        ;;
esac

# --- Version (env > latest) ---
VERSION="${AFM_VERSION:-}"
if [ -z "$VERSION" ]; then
    echo "==> Detecting latest version..."
    VERSION="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
        | sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)"
    if [ -z "$VERSION" ]; then
        echo "ERROR: could not detect the latest version." >&2
        echo "Set it explicitly:  AFM_VERSION=vX.Y.Z ./install-linux.sh" >&2
        exit 1
    fi
fi
echo "==> Version: $VERSION"

ASSET="${BIN_NAME}_linux_${ARCH}.tar.gz"
BASE_URL="https://github.com/$REPO/releases/download/$VERSION"

# --- Download into a temp dir ---
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

echo "==> Downloading $ASSET..."
curl -fsSL "$BASE_URL/$ASSET" -o "$TMP_DIR/$ASSET"

# --- Verify checksum (best-effort) ---
if curl -fsSL "$BASE_URL/checksums.txt" -o "$TMP_DIR/checksums.txt" 2>/dev/null; then
    echo "==> Verifying checksum..."
    EXPECTED="$(sed -n "s/^\([0-9a-f]*\)[[:space:]]*$ASSET$/\1/p" "$TMP_DIR/checksums.txt")"
    if [ -n "$EXPECTED" ]; then
        ACTUAL="$(sha256sum "$TMP_DIR/$ASSET" | awk '{print $1}')"
        if [ "$EXPECTED" != "$ACTUAL" ]; then
            echo "ERROR: checksum mismatch." >&2
            echo "  expected: $EXPECTED" >&2
            echo "  actual:   $ACTUAL" >&2
            exit 1
        fi
        echo "    OK"
    fi
fi

# --- Extract ---
tar -xzf "$TMP_DIR/$ASSET" -C "$TMP_DIR" "$BIN_NAME"
chmod +x "$TMP_DIR/$BIN_NAME"

# --- Install (try /usr/local/bin, fall back to ~/.local/bin) ---
INSTALL_DIR="/usr/local/bin"
if [ -w "$INSTALL_DIR" ]; then
    mv "$TMP_DIR/$BIN_NAME" "$INSTALL_DIR/$BIN_NAME"
elif command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null; then
    sudo mv "$TMP_DIR/$BIN_NAME" "$INSTALL_DIR/$BIN_NAME"
else
    INSTALL_DIR="$HOME/.local/bin"
    mkdir -p "$INSTALL_DIR"
    mv "$TMP_DIR/$BIN_NAME" "$INSTALL_DIR/$BIN_NAME"
    echo "    (no write access to /usr/local/bin — installed into $INSTALL_DIR)"
fi

echo "==> Installed: $INSTALL_DIR/$BIN_NAME"

# --- PATH check ---
if command -v "$BIN_NAME" >/dev/null 2>&1; then
    echo "    $(command -v "$BIN_NAME")"
    "$BIN_NAME" --version || true
else
    echo ""
    echo "WARNING: $INSTALL_DIR is not in PATH. Add to ~/.bashrc:" >&2
    echo "  export PATH=\"$INSTALL_DIR:\$PATH\"" >&2
fi

echo ""
echo "Done. Optionally install the Claude skills:  $BIN_NAME install-skills"
