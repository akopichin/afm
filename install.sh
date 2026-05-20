#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BIN_NAME="flowmanager"
INSTALL_DIR="$HOME/homebrew/bin"
SKILLS_DIR="$HOME/.claude/skills"

# --- Binary ---
echo "==> Установка бинарника $BIN_NAME в $INSTALL_DIR/"

mkdir -p "$INSTALL_DIR"
cp "$SCRIPT_DIR/bin/$BIN_NAME" "$INSTALL_DIR/$BIN_NAME"

chmod +x "$INSTALL_DIR/$BIN_NAME"

if command -v "$BIN_NAME" >/dev/null 2>&1; then
    echo "    OK: $(command -v "$BIN_NAME")"
else
    echo "    ОШИБКА: $BIN_NAME не найден в PATH" >&2
    exit 1
fi

# --- Claude Skills ---
echo "==> Установка Claude-скиллов в $SKILLS_DIR/"

for skill in "$SCRIPT_DIR/assets/claude/skills"/*/; do
    name="$(basename "$skill")"
    mkdir -p "$SKILLS_DIR/$name"
    cp "$skill/SKILL.md" "$SKILLS_DIR/$name/SKILL.md"
    echo "    $name"
done

echo ""
echo "Готово! Доступные команды:"
echo "  /flowmanager         — запустить flow"
echo "  /flowmanager-check   — статус текущего flow"
echo "  /flowmanager-init    — создать flow.yaml"
echo "  /flowmanager-monitor — фоновый мониторинг (субагент)"
