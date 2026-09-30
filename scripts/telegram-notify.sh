#!/usr/bin/env bash
# afm lifecycle-hook notifier: превращает событие жизненного цикла флоу/стадии в
# дружелюбное Telegram-сообщение (parse_mode=HTML). Устанавливается в образ как
# /usr/local/bin/afm-telegram-notify (см. Dockerfile.runtime) и симлинком на
# хостовый PATH — поэтому hook-команда одинакова в Docker и на хосте.
#
# Секреты берутся из окружения (TELEGRAM_BOT_TOKEN / TELEGRAM_CHAT_ID — afm
# резолвит их из hook `env: file:` и передаёт транспортом), либо, как фолбэк,
# читаются напрямую из read-only файлов ~/.afm/secrets/* (см. read_secret_file).
#
# Подключается ТОЛЬКО во flow, объявившем hook (dev-pipeline.yaml → hooks:),
# поэтому уведомления приходят лишь для этого флоу.

set -Eeuo pipefail

readonly TELEGRAM_API_BASE="https://api.telegram.org"

die() { printf 'afm-telegram-notify: %s\n' "$*" >&2; exit 1; }
command -v curl >/dev/null 2>&1 || die "curl is required"

# read_secret_file — фолбэк, когда токен не пришёл через hook env (например при
# внешнем Docker-боундери без afm-транспорта): читаем те же read-only файлы.
read_secret_file() {
  local path=$1
  [[ -r "$path" ]] || return 0
  tr -d '\r\n' < "$path"
}

TELEGRAM_BOT_TOKEN="${TELEGRAM_BOT_TOKEN:-$(read_secret_file "$HOME/.afm/secrets/telegram-bot-token")}"
TELEGRAM_CHAT_ID="${TELEGRAM_CHAT_ID:-$(read_secret_file "$HOME/.afm/secrets/telegram-chat-id")}"

: "${TELEGRAM_BOT_TOKEN:?Set TELEGRAM_BOT_TOKEN (via hook env:) or mount ~/.afm/secrets/telegram-bot-token}"
: "${TELEGRAM_CHAT_ID:?Set TELEGRAM_CHAT_ID (via hook env:) or mount ~/.afm/secrets/telegram-chat-id}"

# esc — экранирование под Telegram HTML.
esc() { local s=${1//&/&amp;}; s=${s//</&lt;}; s=${s//>/&gt;}; printf '%s' "$s"; }

event="${AFM_HOOK_EVENT:-?}"

# Отправляем только события, требующие внимания, и итог флоу — остальное шумит.
case "$event" in
  stage_question_asked|stage_plan_ready|stage_paused|stage_failed|\
  stage_script_before_failed|stage_script_after_failed|flow_finished|flow_failed)
    ;;
  *)
    exit 0
    ;;
esac

flow="$(esc "${AFM_FLOW_NAME:-flow}")"
stage="$(esc "${AFM_STAGE_NAME:-${AFM_STAGE_ID:-stage}}")"

# reason доступен только в JSON-payload (stdin), не в env — берём его для падений.
reason=""
if [[ "$event" == *_failed ]] && command -v jq >/dev/null 2>&1 && [[ ! -t 0 ]]; then
  reason="$(jq -r '.reason // ""' 2>/dev/null || true)"
fi

# Дружелюбный заголовок по событию.
case "$event" in
  flow_started)     title="▶️ Flow <b>${flow}</b> started" ;;
  flow_resumed)     title="⏯️ Flow <b>${flow}</b> resumed" ;;
  flow_finished)    title="✅ Flow <b>${flow}</b> finished" ;;
  flow_failed)      title="❌ Flow <b>${flow}</b> failed" ;;
  flow_interrupted) title="⏹️ Flow <b>${flow}</b> interrupted" ;;

  stage_planning_started)  title="🧠 <b>${stage}</b> — planning started" ;;
  stage_plan_ready)        title="📋 <b>${stage}</b> — plan ready for review" ;;
  stage_approved)          title="👍 <b>${stage}</b> — plan approved" ;;
  stage_revision_started)  title="✏️ <b>${stage}</b> — revising" ;;
  stage_execution_started) title="🛠️ <b>${stage}</b> — started" ;;
  stage_question_asked)    title="❓ <b>${stage}</b> — waiting for your answer" ;;
  stage_question_answered) title="💬 <b>${stage}</b> — answer received" ;;
  stage_retry_scheduled)   title="🔁 <b>${stage}</b> — retry scheduled" ;;
  stage_retry_started)     title="🔁 <b>${stage}</b> — retrying" ;;
  stage_paused)            title="⏸️ <b>${stage}</b> — paused" ;;
  stage_resumed)           title="▶️ <b>${stage}</b> — resumed" ;;
  stage_finished)          title="✅ <b>${stage}</b> — done" ;;
  stage_failed)            title="❌ <b>${stage}</b> — failed" ;;

  stage_script_started)         title="📜 <b>${stage}</b> — script started" ;;
  stage_script_finished)        title="✅ <b>${stage}</b> — script done" ;;
  stage_script_failed)          title="❌ <b>${stage}</b> — script failed" ;;
  stage_script_before_started)  title="📜 <b>${stage}</b> — before-hook started" ;;
  stage_script_before_finished) title="✅ <b>${stage}</b> — before-hook done" ;;
  stage_script_before_failed)   title="❌ <b>${stage}</b> — before-hook failed" ;;
  stage_script_after_started)   title="📜 <b>${stage}</b> — after-hook started" ;;
  stage_script_after_finished)  title="✅ <b>${stage}</b> — after-hook done" ;;
  stage_script_after_failed)    title="❌ <b>${stage}</b> — after-hook failed" ;;

  *)                title="🔔 <b>$(esc "$event")</b> — <b>${flow}</b>" ;;
esac

message="$title"
# Для событий стадии добавляем строку с флоу — контекст, но ненавязчиво.
if [[ -n "${AFM_STAGE_ID:-}" && "$event" != flow_* ]]; then
  message+=$'\n'"<i>flow: ${flow}</i>"
fi
# Причина падения — отдельной строкой.
[[ -n "$reason" ]] && message+=$'\n'"<i>$(esc "$reason")</i>"

(( ${#message} <= 4096 )) || message="${message:0:4093}..."

curl --fail-with-body --silent --show-error \
  --connect-timeout 10 --max-time 30 \
  --request POST \
  --data-urlencode "chat_id=${TELEGRAM_CHAT_ID}" \
  --data-urlencode "parse_mode=HTML" \
  --data-urlencode "text=${message}" \
  "${TELEGRAM_API_BASE}/bot${TELEGRAM_BOT_TOKEN}/sendMessage" \
  >/dev/null

printf 'afm-telegram-notify: sent %s (stage=%s)\n' "$event" "${AFM_STAGE_ID:--}"
