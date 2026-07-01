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

# --- Claude Skills ---
echo ""
read -p "Установить Claude-скиллы (/afm, /afm-check и др.) в ~/.claude/skills/? [Y/n] " answer || answer=""
case "$answer" in
  [nN]*)
    echo "Пропущено. Установи позже: afm install-skills"
    ;;
  *)
    "$INSTALL_DIR/$BIN_NAME" install-skills
    echo ""
    echo "Готово! Доступные команды:"
    echo "  /afm        — запустить flow"
    echo "  /afm-check  — статус текущего flow"
    echo "  /afm-init   — создать flow.yaml"
    echo "  /afm-retry  — повторить упавшую стадию"
    echo "  /afm-review — ревью плана стадии"
    ;;
esac
