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
case $OPENAI_API_KEY in
    *$'\n'* | *$'\r'*)
        echo "error: OPENAI_API_KEY contains a line break" >&2
        exit 1
        ;;
esac

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

# A helper run asks for live output: the text as it grows and the answer named by afm_final, which is given only
# for a complete stream. Without the flag the output is the plain protocol.
stream_ext=0
[[ "${AFM_STREAM_EXTENSION:-}" == "1" ]] && stream_ext=1

# One long-lived jq turns the streamed text into partial message snapshots. A snapshot goes out once the text grew
# by max(256, half of what was last sent), so a long answer costs a linear amount of output however finely the
# provider chunks it.
LIVE_TEXT='
    foreach inputs as $line ({text: "", sent: 0, out: null};
        (($line | select(startswith("data: ")) | ltrimstr("data: ") | select(test("^\\{")) | fromjson?
          | .choices[0]?.delta?.content? | strings) // "") as $d
        | .text += $d
        | if (.text | length) >= (.sent + (if .sent == 0 then 80 else ([256, (.sent / 2)] | max) end))
          then .out = .text | .sent = (.text | length)
          else .out = null end;
        .out | select(. != null)
        | {type: "assistant", message: {content: [{type: "text", text: .}]}, afm: {item_id: "msg-1", partial: true}})
'

# формируем тело запроса. stream_options.include_usage:true просит финальный
# SSE-чанк с .usage (см. заголовок файла — нужен для accounting).
if [[ "$stream_ext" -eq 1 ]]; then
    printf '%s\n' '{"type":"system","subtype":"afm_stream","version":1}'
fi
content=$(build_user_content "$prompt")
# Keep large payloads in files/stdin to avoid argv limits; --rawfile also avoids jq 1.7 raw-stdin UTF-8
# corruption.
request_file=$(mktemp)
response_file=$(mktemp)
trap 'rm -f "$request_file" "$response_file"' EXIT
# The key reaches curl on its stdin: argv is readable by every local user, and a file would outlive a killed run.
auth_key=${OPENAI_API_KEY//\\/\\\\}
auth_key=${auth_key//\"/\\\"}
openai_curl() { printf 'header = "Authorization: Bearer %s"\n' "$auth_key" | curl -K - "$@"; }
printf '%s' "$content" | jq -c --arg model "$OPENAI_MODEL" --arg re "$OPENAI_REASONING_EFFORT" \
    '{model: $model, stream: true, stream_options: {include_usage: true}, messages: [{role: "user", content: .}]}
     + (if $re != "" then {reasoning_effort: $re} else {} end)' > "$request_file"

# Один SSE-ответ читаем для текста и usage/model. Сетевой сбой curl или не-2xx статус
# не должны тихо выглядеть как успех — проверяем код выхода curl и HTTP-статус ниже
# (зеркалим openai-agent-as-claude.sh:165-191).
set +e
if [[ "$stream_ext" -eq 1 ]]; then
    # The body is kept whole in a file while one jq shows the text as it arrives; curl's status is PIPESTATUS[0].
    openai_curl -sS --no-buffer -w '\n%{http_code}' \
        -H "Content-Type: application/json" \
        --data-binary "@$request_file" \
        "${OPENAI_BASE_URL}/chat/completions" | tee "$response_file" | jq -nRc --unbuffered "$LIVE_TEXT"
    curl_exit=${PIPESTATUS[0]}
    response=$(cat "$response_file")
else
    response=$(openai_curl -sS --no-buffer -w '\n%{http_code}' \
        -H "Content-Type: application/json" \
        --data-binary "@$request_file" \
        "${OPENAI_BASE_URL}/chat/completions")
    curl_exit=$?
fi
set -e

http_code=$(printf '%s' "$response" | tail -n1)
response=$(printf '%s' "$response" | sed '$d')

if [[ "$curl_exit" -ne 0 || "$http_code" -lt 200 || "$http_code" -ge 300 ]]; then
    failure="request to $OPENAI_BASE_URL failed (curl exit $curl_exit, http $http_code): "
    # A 2xx streamed body is raw SSE deltas: a secret split across them evades redaction, so it is not echoed.
    if [[ "$stream_ext" -eq 1 && "$http_code" == 2?? ]]; then
        response="(streamed body omitted)"
    fi
    # Full body only on stderr (echo is a builtin, no exec argument limit); the result line gets a capped
    # copy because jq receives it as an exec argument.
    echo "error: $failure$response" >&2
    # The body keeps error.code (insufficient_quota vs rate_limit_exceeded); is_error+result is the shape
    # afm's usage-limit detection reads.
    jq -nc --arg err "$failure${response:0:4096}" '{type:"result", subtype:"error_during_execution", is_error:true, result:$err}'
    exit 1
fi

# A stream is complete once the provider said it finished ([DONE] or a finish_reason). A body that merely stops
# is a cut stream: an error, never a successful partial answer.
if [[ "$stream_ext" -eq 1 ]]; then
    stream_done=$(jq -nr --rawfile sse <(printf '%s' "$response") '
        [$sse | split("\n")[] | sub("^data: ";"")
         | select(. == "[DONE]" or (test("^\\{") and ((fromjson? | .choices[0]?.finish_reason?) // "") != ""))]
        | length > 0')
    if [[ "$stream_done" != "true" ]]; then
        failure="response stream ended before completion (curl exit $curl_exit, http $http_code)"
        echo "error: $failure" >&2
        jq -nc --arg err "$failure" '{type:"result", subtype:"error_during_execution", is_error:true, result:$err}'
        exit 1
    fi
fi

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
if [[ "$stream_ext" -eq 1 ]]; then
    jq -nc --rawfile text <(printf '%s' "$text") \
        'select($text | test("\\S")) | {type:"assistant", message:{content:[{type:"text", text:$text}]}, afm:{item_id:"msg-1", partial:false}}'
    jq -nc --rawfile text <(printf '%s' "$text") \
        '{type:"afm_final", version:1, text:$text} + (if ($text | test("\\S")) then {item_id:"msg-1"} else {} end)'
else
    jq -nc --rawfile text <(printf '%s' "$text") '{type:"assistant", message:{content:[{type:"text", text:$text}]}}'
fi

# Эмитим terminal result; без usage оставляем accounting unmetered.
if [[ -z "$usage_json" ]]; then
    echo '{"type":"result","subtype":"success"}'
else
    jq -nc --arg model "$resolved_model" --argjson usage "$usage_json" \
        '{type:"result", subtype:"success", usage_contract_version:1, usage_schema:"openai_chat", channel:"openai-api", usage:$usage}
         + (if $model != "" then {model:$model} else {} end)'
fi
