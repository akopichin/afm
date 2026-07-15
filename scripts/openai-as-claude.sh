#!/usr/bin/env bash
# openai-as-claude.sh — транслятор для OpenAI-совместимых провайдеров (cursor, deepseek, и т.д.)
#
# читает промпт из stdin, вызывает ${OPENAI_BASE_URL}/chat/completions (stream=true),
# транслирует SSE-чанки в claude stream-json формат.
#
# переменные окружения (устанавливаются сгенерированным враппером autoShim):
#   OPENAI_API_KEY   — токен авторизации (обязателен)
#   OPENAI_BASE_URL  — базовый URL API (дефолт: https://api.openai.com/v1)
#   OPENAI_MODEL     — модель (дефолт: gpt-4o)

set -euo pipefail

command -v curl >/dev/null 2>&1 || { echo "error: curl is required but not found" >&2; exit 1; }
command -v jq   >/dev/null 2>&1 || { echo "error: jq is required but not found" >&2; exit 1; }

# игнорируем все claude CLI флаги (--model, --effort, --dangerously-skip-permissions и т.д.)
while [[ $# -gt 0 ]]; do
    shift
done

# читаем промпт только из stdin (как делает claude, когда prompt передаётся pipe)
if [[ -t 0 ]]; then
    echo "error: no prompt on stdin (openai-as-claude requires prompt via stdin pipe)" >&2
    exit 1
fi
prompt=$(cat)

if [[ -z "$prompt" ]]; then
    echo "error: empty prompt" >&2
    exit 1
fi

OPENAI_BASE_URL="${OPENAI_BASE_URL:-https://api.openai.com/v1}"
OPENAI_MODEL="${OPENAI_MODEL:-gpt-4o}"

if [[ -z "${OPENAI_API_KEY:-}" ]]; then
    echo "error: OPENAI_API_KEY is not set" >&2
    exit 1
fi

# формируем тело запроса
body=$(jq -nc --arg model "$OPENAI_MODEL" --arg content "$prompt" \
    '{model: $model, stream: true, messages: [{role: "user", content: $content}]}')

# вызываем API и накапливаем SSE-чанки, затем эмитим ОДИН assistant-конверт.
# Важно: pkg/executor/parseStreamEvent парсит ТОЛЬКО {type:"assistant", message:{...}},
# стриминговые content_block_delta им игнорируются — поэтому накапливаем текст целиком
# и отдаём агрегированную форму, как делает claude в stream-json режиме.
#
# SSE формат ответа: "data: {...}" или "data: [DONE]".
# Накопление делаем одним jq-конвейером: читаем строки как raw, отбрасываем всё
# до "data: ", парсим JSON, конкатенируем delta.content. [DONE] не парсится (jq
# выдаст null → пропустим). Весь accumulate — одна команда, без подоболочек bash.
#
# || true — не падать при ошибке curl (документированное ограничение);
# даже при сбое $text будет пустым (jq вернёт пустую строку), конверт всё равно эмитится.
text=$(curl -sS --no-buffer \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $OPENAI_API_KEY" \
    -d "$body" \
    "${OPENAI_BASE_URL}/chat/completions" 2>/dev/null | \
    jq -jRr '
        sub("^data: ";"")                  # убрать префикс "data: " (если есть)
        | select(test("^\\{"))             # оставить только строки, начинающиеся с "{" (JSON; [DONE]/пустые отбрасываются)
        | fromjson?                        # парсим JSON; некорректные → null и skip
        | (.choices[0].delta.content // "")
    ' | jq -sRr 'rtrimstr("\n")' || true)  # объединить в одну строку, обрезать хвостовой \n
# подстраховка: если что-то пошло не так и text не задан — пустая строка
text="${text:-}"

# assistant-конверт: агрегированный текст всего ответа.
# jq -nc --arg t — корректно JSON-экранирует текст (кавычки/переводы строк).
jq -nc --arg t "$text" '{type:"assistant", message:{content:[{type:"text", text:$t}]}}'

# финальный result-ивент (claude executor ждёт его для завершения).
echo '{"type":"result","subtype":"success"}'
