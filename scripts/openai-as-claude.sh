#!/usr/bin/env bash
# Переводит ответ OpenAI chat/completions в Claude stream-json; промпт читает из stdin.

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
OPENAI_REASONING_EFFORT="${OPENAI_REASONING_EFFORT:-}"

if [[ -z "${OPENAI_API_KEY:-}" ]]; then
    echo "error: OPENAI_API_KEY is not set" >&2
    exit 1
fi

# extract_image_blocks <text> -> JSON array of image_url content blocks for every readable [Screenshot:
# <path>] reference in text.
extract_image_blocks() {
    local text="$1"
    local blocks='[]'
    local marker path
    while IFS= read -r marker; do
        [[ -z "$marker" ]] && continue
        # marker уже включает скобки — обрезаем "[Screenshot: " и "]" в bash, чтобы обойтись POSIX-совместимым
        # -E (без \K, который есть только в GNU/PCRE grep и отсутствует в BSD grep из macOS).
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
        # base64 -w0 — GNU-only флаг. Портируемый вариант — читать файл через stdin (у обеих реализаций
        # один и тот же вид вывода по умолчанию) и убрать переводы строк сами.
        b64=$(base64 <"$path" | tr -d '\n')
        blocks=$(printf '%s' "$b64" | jq -Rsc --slurpfile blocks <(printf '%s' "$blocks") --arg mime "$mime" \
            '$blocks[0] + [{type:"image_url", image_url:{url: ("data:" + $mime + ";base64," + .)}}]')
    done < <(printf '%s' "$text" | grep -oE '\[Screenshot: [^]]+\]' || true)
    printf '%s' "$blocks"
}

# build_user_content <text> -> plain JSON string if no image was embedded, else a [{type:"text",...},
# image_url...] array with the [Screenshot: <path>] marker stripped from the text portion.
build_user_content() {
    local text="$1"
    local blocks
    blocks=$(extract_image_blocks "$text")
    if [[ "$blocks" == "[]" ]]; then
        jq -nc --rawfile text <(printf '%s' "$text") '$text'
        return
    fi
    local cleaned
    cleaned=$(printf '%s' "$text" | sed -E 's/\[Screenshot: [^]]+\]//g')
    jq -nc --rawfile text <(printf '%s' "$cleaned") --slurpfile imgs <(printf '%s' "$blocks") '[{type:"text", text:$text}] + $imgs[0]'
}

# формируем тело запроса. stream_options.include_usage:true просит финальный
# SSE-чанк с .usage (см. заголовок файла — нужен для accounting).
content=$(build_user_content "$prompt")
# Keep large payloads in files/stdin to avoid argv limits; --rawfile also avoids jq 1.7 raw-stdin UTF-8
# corruption.
request_file=$(mktemp)
trap 'rm -f "$request_file"' EXIT
printf '%s' "$content" | jq -c --arg model "$OPENAI_MODEL" --arg re "$OPENAI_REASONING_EFFORT" \
    '{model: $model, stream: true, stream_options: {include_usage: true}, messages: [{role: "user", content: .}]}
     + (if $re != "" then {reasoning_effort: $re} else {} end)' > "$request_file"

# Один SSE-ответ читаем для текста и usage/model; после сбоя curl всё равно эмитим частичный или пустой
# конверт.
response=$(curl -sS --no-buffer \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $OPENAI_API_KEY" \
    --data-binary "@$request_file" \
    "${OPENAI_BASE_URL}/chat/completions" 2>/dev/null || true)

# Собираем SSE-текст в один assistant-конверт: executor игнорирует content_block_delta.
text=$(jq -njr --rawfile sse <(printf '%s' "$response") '
        $sse | split("\n")[] |
        sub("^data: ";"")                  # убрать префикс "data: " (если есть)
        | select(test("^\\{"))             # оставить только строки, начинающиеся с "{" (JSON; [DONE]/пустые отбрасываются)
        | fromjson?                        # парсим JSON; некорректные → null и skip
        | (.choices[0].delta.content // "")
    ' || true)
# подстраховка: если что-то пошло не так и text не задан — пустая строка
text="${text:-}"

# Берём последний ненулевой usage; cached_tokens переводим в prompt_cached_tokens для accounting.
usage_chunk=$(jq -nrc --rawfile sse <(printf '%s' "$response") '
        $sse | split("\n")[] |
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
# jq читает текст из файла, сохраняя JSON-экранирование без лимита argv.
jq -nc --rawfile text <(printf '%s' "$text") '{type:"assistant", message:{content:[{type:"text", text:$text}]}}'

# Эмитим terminal result; без usage оставляем accounting unmetered.
if [[ -z "$usage_json" ]]; then
    echo '{"type":"result","subtype":"success"}'
else
    jq -nc --arg model "$resolved_model" --argjson usage "$usage_json" \
        '{type:"result", subtype:"success", usage_contract_version:1, usage_schema:"openai_chat", channel:"openai-api", usage:$usage}
         + (if $model != "" then {model:$model} else {} end)'
fi
