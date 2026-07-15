#!/usr/bin/env bash
# cursor-as-claude.sh — адаптер Cursor Cloud Agents API → claude stream-json.
#
# Cursor Cloud API (api.cursor.com) НЕ имеет синхронного /chat/completions — это
# асинхронный run-based Cloud Agents API. Поэтому адаптер работает иначе, чем
# openai-as-claude: он не делает одного запроса «промпт → ответ», а запускает
# Cloud Agent и дожидается его результата:
#
#   1. создаёт no-repo Cloud Agent (POST /v1/agents) с промптом из stdin;
#   2. опрашивает run до терминального статуса (FINISHED/ERROR/CANCELLED/EXPIRED);
#   3. эмитит claude stream-json: assistant-конверт с итоговым текстом + result event;
#   4. архивирует агента (best-effort), чтобы не плодить мусор в аккаунте.
#
# Переменные (ставятся сгенерированным враппером autoShim при type: cursor):
#   CURSOR_API_KEY   — токен авторизации (обязателен)
#   CURSOR_BASE_URL  — базовый URL API (дефолт: https://api.cursor.com/v1)
#   CURSOR_MODEL     — model.id (пусто / "auto" → поле model опускается → Cursor default)
#
# Особенности:
#   - Создание агента включает старт cloud-VM (enqueuing) и может занимать до
#     минуты и более — отсюда длинный timeout на create (300с).
#   - Результат эмитится одним агрегированным конвертом в конце (как openai-as-claude):
#     pkg/executor/parseStreamEvent парсит {type:"assistant", message:{...}}, а
#     стриминговые дельты игнорирует, поэтому копим текст целиком.

set -euo pipefail

command -v curl >/dev/null 2>&1 || { echo "error: curl is required but not found" >&2; exit 1; }
command -v jq   >/dev/null 2>&1 || { echo "error: jq is required but not found" >&2; exit 1; }

# Игнорируем все claude CLI флаги (--model, --effort, --dangerously-skip-permissions и т.д.).
while [[ $# -gt 0 ]]; do
    shift
done

# Промпт только из stdin (как делает claude, когда prompt передаётся через pipe).
if [[ -t 0 ]]; then
    echo "error: no prompt on stdin (cursor-as-claude requires prompt via stdin pipe)" >&2
    exit 1
fi
prompt=$(cat)
if [[ -z "$prompt" ]]; then
    echo "error: empty prompt" >&2
    exit 1
fi

CURSOR_BASE_URL="${CURSOR_BASE_URL:-https://api.cursor.com/v1}"
CURSOR_MODEL="${CURSOR_MODEL:-}"

if [[ -z "${CURSOR_API_KEY:-}" ]]; then
    echo "error: CURSOR_API_KEY is not set" >&2
    exit 1
fi

AUTH="Authorization: Bearer $CURSOR_API_KEY"
CT="Content-Type: application/json"

# Тело create: prompt + mode "agent". no-repo (без repos/env) — Cloud Agent без репозитория.
body=$(jq -nc --arg text "$prompt" '{prompt:{text:$text}, mode:"agent"}')
# model добавляем только если задан и не "auto" — иначе Cursor использует свой default.
if [[ -n "$CURSOR_MODEL" && "$CURSOR_MODEL" != "auto" ]]; then
    body=$(printf '%s' "$body" | jq -c --arg m "$CURSOR_MODEL" '.model={id:$m}')
fi

# 1. Создаём no-repo Cloud Agent. Старт VM может занимать минуты — timeout 300с.
create_resp=$(curl -sS -m 300 -X POST "${CURSOR_BASE_URL}/agents" -H "$CT" -H "$AUTH" -d "$body") || {
    echo "error: cursor create agent failed (curl exit $?)" >&2
    exit 1
}
agent_id=$(printf '%s' "$create_resp" | jq -r '.agent.id // empty')
run_id=$(printf '%s' "$create_resp" | jq -r '.run.id // empty')
if [[ -z "$agent_id" || -z "$run_id" ]]; then
    echo "error: cursor create returned no agent/run id: $create_resp" >&2
    exit 1
fi

# 2. Опрашиваем run до терминального статуса. Кап ~25 мин (< executor idle 30 мин).
status=""
result_text=""
deadline=$(( SECONDS + 1500 ))   # 25 минут — backstop на зависшие run'ы
while (( SECONDS < deadline )); do
    # run мог уже завершиться в момент create — поэтому poll'им сразу.
    if ! run_resp=$(curl -sS -m 60 "${CURSOR_BASE_URL}/agents/${agent_id}/runs/${run_id}" -H "$AUTH" 2>/dev/null); then
        sleep 3
        continue
    fi
    status=$(printf '%s' "$run_resp" | jq -r '.status // empty')
    case "$status" in
        FINISHED|ERROR|CANCELLED|EXPIRED)
            result_text=$(printf '%s' "$run_resp" | jq -r '.result // empty')
            break
            ;;
    esac
    sleep 3
done

# 3. Эмитим claude stream-json. assistant-конверт: агрегированный текст всего ответа.
#    jq -nc --arg корректно JSON-экранирует текст (кавычки/переводы строк).
if [[ "$status" == "FINISHED" ]]; then
    text="${result_text:-}"
    jq -nc --arg t "$text" '{type:"assistant",message:{content:[{type:"text",text:$t}]}}'
    echo '{"type":"result","subtype":"success"}'
else
    # Не FINISHED (ERROR/CANCELLED/EXPIRED/timeout) — отдаём как assistant-текст + success,
    # чтобы executor корректно завершился, а описание проблемы было видно пользователю.
    msg="Cursor run ${status:-TIMEOUT}: ${result_text:-(no result)}"
    jq -nc --arg t "$msg" '{type:"assistant",message:{content:[{type:"text",text:$t}]}}'
    echo '{"type":"result","subtype":"success"}'
fi

# 4. Архивируем агента (best-effort). no-repo агент одноразовый — не плодим мусор в аккаунте.
curl -sS -m 20 -o /dev/null -X POST "${CURSOR_BASE_URL}/agents/${agent_id}/archive" -H "$AUTH" 2>/dev/null || true
