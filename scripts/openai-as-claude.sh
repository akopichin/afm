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
#
# usage accounting: запрос идёт со stream_options:{include_usage:true}, поэтому
# провайдер шлёт финальный SSE-чанк с .usage (и обычно .model). Числа токенов
# из этого чанка (никогда не текст промпта/ответа) пробрасываются в synthetic
# terminal result в форме, которую уже понимает Collector в pkg/accounting
# (usage_contract_version:1, usage_schema:"openai_chat", channel:"openai-api"),
# с вложенным prompt_tokens_details.cached_tokens, сплющенным в плоское поле
# prompt_cached_tokens.

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

# extract_image_blocks <text> -> JSON array of image_url content blocks for every
# readable [Screenshot: <path>] reference in text (empty "[]" if none, or if none
# were readable/recognized). Unreadable/unrecognized paths are skipped with a
# stderr warning, not a hard failure.
extract_image_blocks() {
    local text="$1"
    local blocks='[]'
    local marker path
    while IFS= read -r marker; do
        [[ -z "$marker" ]] && continue
        # marker уже включает скобки — обрезаем "[Screenshot: " и "]" в bash,
        # чтобы обойтись POSIX-совместимым -E (без \K, который есть только в GNU/PCRE grep
        # и отсутствует в BSD grep из macOS).
        path="${marker#\[Screenshot: }"
        path="${path%\]}"
        if [[ ! -r "$path" ]]; then
            echo "warning: [Screenshot: $path] not readable, skipping" >&2
            continue
        fi
        local mime=""
        case "$path" in
            *.png) mime="image/png" ;;
            *.jpg|*.jpeg) mime="image/jpeg" ;;
            *.webp) mime="image/webp" ;;
            *.gif) mime="image/gif" ;;
            *) echo "warning: [Screenshot: $path] unrecognized image extension, skipping" >&2; continue ;;
        esac
        local b64
        # base64 -w0 — GNU-only флаг (BSD base64 из macOS его не понимает и падает
        # "invalid argument"). Портируемый вариант — читать файл через stdin (у обеих
        # реализаций один и тот же вид вывода по умолчанию) и убрать переводы строк сами.
        b64=$(base64 <"$path" | tr -d '\n')
        blocks=$(jq -nc --argjson blocks "$blocks" --arg mime "$mime" --arg b64 "$b64" \
            '$blocks + [{type:"image_url", image_url:{url: ("data:" + $mime + ";base64," + $b64)}}]')
    done < <(printf '%s' "$text" | grep -oE '\[Screenshot: [^]]+\]' || true)
    printf '%s' "$blocks"
}

# build_user_content <text> -> plain JSON string if no image was embedded, else a
# [{type:"text",...}, image_url...] array with the [Screenshot: <path>] marker(s)
# stripped from the text portion.
build_user_content() {
    local text="$1"
    local blocks
    blocks=$(extract_image_blocks "$text")
    if [[ "$blocks" == "[]" ]]; then
        jq -nc --arg t "$text" '$t'
        return
    fi
    local cleaned
    cleaned=$(printf '%s' "$text" | sed -E 's/\[Screenshot: [^]]+\]//g')
    jq -nc --arg t "$cleaned" --argjson imgs "$blocks" '[{type:"text", text:$t}] + $imgs'
}

# формируем тело запроса. stream_options.include_usage:true просит финальный
# SSE-чанк с .usage (см. заголовок файла — нужен для accounting).
content=$(build_user_content "$prompt")
body=$(jq -nc --arg model "$OPENAI_MODEL" --argjson content "$content" \
    '{model: $model, stream: true, stream_options: {include_usage: true}, messages: [{role: "user", content: $content}]}')

# вызываем API один раз, оставляем полный SSE-ответ в переменной — нужен для ДВУХ
# независимых проходов: накопление текста (существующая логика ниже) и извлечение
# .usage/.model из финального чанка (accounting, см. заголовок файла).
#
# || true — не падать при ошибке curl (документированное ограничение); даже при
# сбое $response будет частичным/пустым, конверт всё равно эмитится.
response=$(curl -sS --no-buffer \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $OPENAI_API_KEY" \
    -d "$body" \
    "${OPENAI_BASE_URL}/chat/completions" 2>/dev/null || true)

# накапливаем SSE-чанки, затем эмитим ОДИН assistant-конверт.
# Важно: pkg/executor/parseStreamEvent парсит ТОЛЬКО {type:"assistant", message:{...}},
# стриминговые content_block_delta им игнорируются — поэтому накапливаем текст целиком
# и отдаём агрегированную форму, как делает claude в stream-json режиме.
#
# SSE формат ответа: "data: {...}" или "data: [DONE]".
# Накопление делаем одним jq-конвейером: читаем строки как raw, отбрасываем всё
# до "data: ", парсим JSON, конкатенируем delta.content. [DONE] не парсится (jq
# выдаст null → пропустим). Финальный usage-чанк (stream_options.include_usage)
# обычно приходит с пустым choices:[] — .choices[0] там null, delta.content
# корректно сворачивается в "" и ничего не портит.
text=$(printf '%s' "$response" | \
    jq -jRr '
        sub("^data: ";"")                  # убрать префикс "data: " (если есть)
        | select(test("^\\{"))             # оставить только строки, начинающиеся с "{" (JSON; [DONE]/пустые отбрасываются)
        | fromjson?                        # парсим JSON; некорректные → null и skip
        | (.choices[0].delta.content // "")
    ' | jq -sRr 'rtrimstr("\n")' || true)  # объединить в одну строку, обрезать хвостовой \n
# подстраховка: если что-то пошло не так и text не задан — пустая строка
text="${text:-}"

# usage/model: последний чанк с ненулевым .usage (stream_options.include_usage).
# prompt_tokens_details.cached_tokens сплющивается в плоское prompt_cached_tokens —
# это то имя поля, которое ждёт rawUsage в pkg/accounting/normalize.go.
usage_chunk=$(printf '%s' "$response" | jq -Rrc '
        sub("^data: ";"")
        | select(test("^\\{"))
        | fromjson?
        | select(.usage != null)
        | {
            model: (.model // null),
            usage: (
                {prompt_tokens: (.usage.prompt_tokens // 0), completion_tokens: (.usage.completion_tokens // 0)}
                + (if .usage.prompt_tokens_details.cached_tokens != null
                   then {prompt_cached_tokens: .usage.prompt_tokens_details.cached_tokens}
                   else {} end)
              )
          }
    ' 2>/dev/null | tail -n1 || true)

usage_json=""
resolved_model="$OPENAI_MODEL"
if [[ -n "$usage_chunk" ]]; then
    usage_json=$(printf '%s' "$usage_chunk" | jq -c '.usage')
    m=$(printf '%s' "$usage_chunk" | jq -r '.model // empty')
    [[ -n "$m" ]] && resolved_model="$m"
fi

# assistant-конверт: агрегированный текст всего ответа.
# jq -nc --arg t — корректно JSON-экранирует текст (кавычки/переводы строк).
jq -nc --arg t "$text" '{type:"assistant", message:{content:[{type:"text", text:$t}]}}'

# финальный result-ивент (claude executor ждёт его для завершения). Без
# перехваченного usage эмитим голую строку, как раньше — Collector.Finish тогда
# честно репортит "unmetered", а не выдуманный $0.
if [[ -z "$usage_json" ]]; then
    echo '{"type":"result","subtype":"success"}'
else
    jq -nc --arg model "$resolved_model" --argjson usage "$usage_json" \
        '{type:"result", subtype:"success", usage_contract_version:1, usage_schema:"openai_chat", channel:"openai-api", usage:$usage}
         + (if $model != "" then {model:$model} else {} end)'
fi
