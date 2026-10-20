#!/usr/bin/env bash
# Запуск Telegram-бота с теми же секретами, что у ~/.afm/t-notify.sh.
set -Eeuo pipefail

die() { printf 'runbot: %s\n' "$*" >&2; exit 1; }

read_secret_file() {
  local path=$1
  [[ -r "$path" ]] || return 0
  tr -d '\r\n' < "$path"
}

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
telegram_secrets_dir="${AFM_TELEGRAM_SECRETS_DIR:-$HOME/.afm/secrets}"

TELEGRAM_BOT_TOKEN="${TELEGRAM_BOT_TOKEN:-$(read_secret_file "$telegram_secrets_dir/telegram-bot-token")}"
[[ -n "$TELEGRAM_BOT_TOKEN" ]] || die "set TELEGRAM_BOT_TOKEN or fill $telegram_secrets_dir/telegram-bot-token"

if [[ -z "${TELEGRAM_ALLOWED_USER_IDS:-}" ]]; then
  # В личном чате chat_id совпадает с user ID; ID групп сюда не подходят.
  telegram_chat_id="${TELEGRAM_CHAT_ID:-$(read_secret_file "$telegram_secrets_dir/telegram-chat-id")}"
  [[ "$telegram_chat_id" =~ ^[1-9][0-9]*$ ]] || die "set TELEGRAM_ALLOWED_USER_IDS or put a private-chat user ID in $telegram_secrets_dir/telegram-chat-id"
  TELEGRAM_ALLOWED_USER_IDS="$telegram_chat_id"
fi

export TELEGRAM_BOT_TOKEN TELEGRAM_ALLOWED_USER_IDS
command -v go >/dev/null 2>&1 || die "go is required"
cd "$script_dir/telegram-bot"
exec go run . "$@"
