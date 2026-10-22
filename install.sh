#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BIN_NAME="afm"
INSTALL_DIR="$HOME/homebrew/bin"

# --- Binary ---
SRC_BIN="$SCRIPT_DIR/bin/$BIN_NAME"
if [ ! -f "$SRC_BIN" ]; then
    echo "ОШИБКА: бинарник не собран ($SRC_BIN не найден)." >&2
    echo "Сначала собери его:  make build" >&2
    echo "Затем запусти install.sh снова." >&2
    exit 1
fi

echo "==> Установка бинарника $BIN_NAME в $INSTALL_DIR/"

mkdir -p "$INSTALL_DIR"
cp "$SRC_BIN" "$INSTALL_DIR/$BIN_NAME"

chmod +x "$INSTALL_DIR/$BIN_NAME"

if command -v "$BIN_NAME" >/dev/null 2>&1; then
    echo "    OK: $(command -v "$BIN_NAME")"
else
    echo "    ОШИБКА: $BIN_NAME не найден в PATH" >&2
    exit 1
fi

# --- Skills (Claude Code, Codex) ---
echo ""
read -p "Установить скиллы afm для Claude Code и Codex? Папки встроенных скиллов afm (afm-run, afm-check, …), если это обычные каталоги, будут перенесены в backup. [Y/n] " answer || answer=""
case "$answer" in
  [nN]*)
    echo "Пропущено. Установи позже: afm install-skills"
    ;;
  *)
    "$INSTALL_DIR/$BIN_NAME" install-skills --force
    ;;
esac
