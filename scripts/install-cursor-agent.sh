#!/usr/bin/env bash
# Устанавливает закреплённый cursor-agent в PREFIX (по умолчанию /usr/local): хеш архива сверяется с пином до
# распаковки, при несовпадении ничего не устанавливается.
# Использование: install-cursor-agent.sh [PREFIX]

set -euo pipefail

VERSION="2026.10.22-27dcf5f"
SHA256_LINUX_X64="85296944e4600a83c4b764f578d1502bf9c416db200d33a593e8f3ecabfdc294"
SHA256_LINUX_ARM64="22a36df9720830668ccd4d707e5190204ab9c8a0efb2f068cc914895f804d3ec"

PREFIX="${1:-/usr/local}"
# The bin/ link resolves relative to bin/, so a relative PREFIX must become absolute first.
[[ "$PREFIX" == /* ]] || PREFIX="$PWD/$PREFIX"

case "$(uname -s)-$(uname -m)" in
    Linux-x86_64)          platform="linux/x64";   expected_sha256="$SHA256_LINUX_X64" ;;
    Linux-aarch64|Linux-arm64) platform="linux/arm64"; expected_sha256="$SHA256_LINUX_ARM64" ;;
    *) echo "error: unsupported platform $(uname -s)-$(uname -m): cursor-agent is pinned for Linux x64 and arm64" >&2; exit 1 ;;
esac

command -v curl >/dev/null 2>&1 || { echo "error: curl is required but not found" >&2; exit 1; }
if command -v sha256sum >/dev/null 2>&1; then
    sha256_of() { sha256sum "$1" | cut -d' ' -f1; }
else
    sha256_of() { shasum -a 256 "$1" | cut -d' ' -f1; }
fi

url="https://downloads.cursor.com/lab/${VERSION}/${platform}/agent-cli-package.tar.gz"
work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT

curl -fsSL --retry 3 --retry-all-errors -o "$work_dir/package.tar.gz" "$url"

actual_sha256=$(sha256_of "$work_dir/package.tar.gz")
if [[ "$actual_sha256" != "$expected_sha256" ]]; then
    echo "error: cursor-agent ${VERSION} (${platform}) sha256 mismatch: expected $expected_sha256, got $actual_sha256" >&2
    exit 1
fi

# The archive holds one top-level directory with the cursor-agent launcher and its bundled node.
mkdir "$work_dir/extract"
tar -xzf "$work_dir/package.tar.gz" -C "$work_dir/extract" --strip-components=1
[[ -x "$work_dir/extract/cursor-agent" ]] || { echo "error: cursor-agent launcher is missing from the package" >&2; exit 1; }

install_dir="$PREFIX/share/cursor-agent/$VERSION"
mkdir -p "$PREFIX/bin" "$PREFIX/share/cursor-agent"
rm -rf "$install_dir"
mv "$work_dir/extract" "$install_dir"
ln -sfn "$install_dir/cursor-agent" "$PREFIX/bin/cursor-agent"
echo "cursor-agent ${VERSION} installed to $PREFIX/bin/cursor-agent"
