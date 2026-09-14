#!/usr/bin/env bash
# openai-agent-as-claude.sh — real tool-loop translator for OpenAI-compatible
# providers with function calling (IdeaLab, and any other /chat/completions
# gateway that supports `tools`). Unlike openai-as-claude.sh (one-shot text),
# this gives the model exactly one tool — `bash` — and loops: call the API,
# execute any tool_calls for real, feed results back, repeat until the model
# answers with plain text or OPENAI_AGENT_MAX_TURNS is reached.
#
# environment variables:
#   OPENAI_API_KEY         — токен авторизации (обязателен)
#   OPENAI_BASE_URL        — базовый URL API (дефолт: https://api.openai.com/v1)
#   OPENAI_MODEL           — модель (дефолт: gpt-4o)
#   OPENAI_AGENT_MAX_TURNS — макс. число tool-вызовов за стадию (дефолт: 40)
#
# usage accounting: каждый внутренний turn запрашивается со
# stream_options:{include_usage:true}. Числа токенов каждого turn'а (никогда
# не текст промпта/ответа) собираются БЕЗ суммирования в массив
# upstream_usages — суммирование делает исключительно pkg/accounting, не эта
# оболочка (никакого `jq add` здесь). Форма synthetic terminal result — та же,
# что понимает Collector: usage_contract_version:1, usage_schema:"openai_chat",
# channel:"openai-api".

set -euo pipefail

command -v curl >/dev/null 2>&1 || { echo "error: curl is required but not found" >&2; exit 1; }
command -v jq   >/dev/null 2>&1 || { echo "error: jq is required but not found" >&2; exit 1; }

# игнорируем все claude CLI флаги (--model, --effort, --dangerously-skip-permissions и т.д.)
while [[ $# -gt 0 ]]; do
    shift
done

if [[ -t 0 ]]; then
    echo "error: no prompt on stdin (openai-agent-as-claude requires prompt via stdin pipe)" >&2
    exit 1
fi
prompt=$(cat)

if [[ -z "$prompt" ]]; then
    echo "error: empty prompt" >&2
    exit 1
fi

OPENAI_BASE_URL="${OPENAI_BASE_URL:-https://api.openai.com/v1}"
OPENAI_MODEL="${OPENAI_MODEL:-gpt-4o}"
OPENAI_AGENT_MAX_TURNS="${OPENAI_AGENT_MAX_TURNS:-40}"

if [[ -z "${OPENAI_API_KEY:-}" ]]; then
    echo "error: OPENAI_API_KEY is not set" >&2
    exit 1
fi

system_prompt='You have exactly one tool, "bash", which runs a shell command in the current working directory and returns its combined stdout+stderr and exit code. Use it to read and write files, run scripts, and wait for external input (a blocking command is fine to run -- it will return once ready). If the task mentions a skill by name, first read its instructions with bash (e.g. `cat .claude/skills/<name>/SKILL.md` or `~/.claude/skills/<name>/SKILL.md`) before proceeding. If a message mentions [Screenshot: <path>], an image of it is attached directly to that message -- you do not need to read the file yourself. When the task is fully complete, respond with your final answer as plain text and do not call any tool.'

tools_json='[{"type":"function","function":{"name":"bash","description":"Execute a shell command in the current working directory and return its combined stdout+stderr and exit code.","parameters":{"type":"object","properties":{"command":{"type":"string","description":"The shell command to run"}},"required":["command"]}}}]'

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
        # чтобы обойтись POSIX-совместимым -E (без \K, который есть только в GNU/PCRE
        # grep и отсутствует в BSD grep из macOS — go test запускает этот скрипт
        # напрямую на хосте раннера, не внутри Docker-образа).
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
        # base64 -w0 — GNU-only флаг (BSD base64 из macOS падает "invalid argument").
        # Портируемый вариант — читать файл через stdin (одинаковый вывод у обеих
        # реализаций) и убрать переводы строк вручную.
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

# extract_turn_usage <sse-body> -> compact flattened usage JSON object for the
# LAST chunk in this turn carrying a non-null .usage, or empty if none. Same
# flattening as openai-as-claude.sh: prompt_tokens_details.cached_tokens ->
# flat prompt_cached_tokens (the field name pkg/accounting/normalize.go reads).
# Numbers only, never prompt/response text.
extract_turn_usage() {
    local sse="$1"
    printf '%s' "$sse" | jq -Rrc '
        sub("^data: ";"")
        | select(test("^\\{"))
        | fromjson?
        | select(.usage != null)
        | {prompt_tokens: (.usage.prompt_tokens // 0), completion_tokens: (.usage.completion_tokens // 0)}
            + (if .usage.prompt_tokens_details.cached_tokens != null
               then {prompt_cached_tokens: .usage.prompt_tokens_details.cached_tokens}
               else {} end)
    ' 2>/dev/null | tail -n1 || true
}

# extract_turn_model <sse-body> -> the LAST non-empty .model seen across this
# turn's chunks (independent of whether that particular chunk also had usage —
# most providers echo .model on every chunk).
extract_turn_model() {
    local sse="$1"
    printf '%s' "$sse" | jq -Rr '
        sub("^data: ";"")
        | select(test("^\\{"))
        | fromjson?
        | (.model // empty)
    ' 2>/dev/null | grep -v '^$' | tail -n1 || true
}

# build_result_line <subtype> — emits the terminal result line. upstream_usages
# is a plain JSON array of raw per-turn usage objects — accounting normalizes
# and sums each one itself (no `jq add` here, per the module-level comment).
# With no usage gathered at all, omits the envelope so Collector.Finish reports
# an honest "unmetered" observation instead of a fabricated $0.
build_result_line() {
    local subtype="$1"
    if [[ "$upstream_usages" == "[]" ]]; then
        jq -nc --arg st "$subtype" '{type:"result", subtype:$st}'
        return
    fi
    jq -nc --arg st "$subtype" --arg model "$resolved_model" --argjson usages "$upstream_usages" \
        '{type:"result", subtype:$st, usage_contract_version:1, usage_schema:"openai_chat", channel:"openai-api", upstream_usages:$usages}
         + (if $model != "" then {model:$model} else {} end)'
}

messages_file=$(mktemp)
trap 'rm -f "$messages_file" "${messages_file}.tmp"' EXIT

user_content=$(build_user_content "$prompt")
jq -nc --arg sys "$system_prompt" --argjson user "$user_content" \
    '[{role:"system", content:$sys}, {role:"user", content:$user}]' > "$messages_file"

final_text=""
turn=0
max_turns_reached=0
upstream_usages='[]'
resolved_model="$OPENAI_MODEL"

while :; do
    turn=$((turn + 1))
    if [[ "$turn" -gt "$OPENAI_AGENT_MAX_TURNS" ]]; then
        max_turns_reached=1
        break
    fi

    request_body=$(jq -nc --slurpfile msgs "$messages_file" --arg model "$OPENAI_MODEL" --argjson tools "$tools_json" \
        '{model: $model, stream: true, stream_options: {include_usage: true}, tool_choice: "auto", tools: $tools, messages: $msgs[0]}')

    set +e
    response=$(curl -sS -w '\n%{http_code}' \
        -H "Content-Type: application/json" \
        -H "Authorization: Bearer $OPENAI_API_KEY" \
        -d "$request_body" \
        "${OPENAI_BASE_URL}/chat/completions")
    curl_exit=$?
    set -e

    http_code=$(printf '%s' "$response" | tail -n1)
    body=$(printf '%s' "$response" | sed '$d')

    turn_model=$(extract_turn_model "$body")
    [[ -n "$turn_model" ]] && resolved_model="$turn_model"
    turn_usage=$(extract_turn_usage "$body")
    if [[ -n "$turn_usage" ]]; then
        upstream_usages=$(jq -nc --argjson arr "$upstream_usages" --argjson item "$turn_usage" '$arr + [$item]')
    fi

    if [[ "$curl_exit" -ne 0 || "$http_code" -lt 200 || "$http_code" -ge 300 ]]; then
        echo "error: request to $OPENAI_BASE_URL failed (curl exit $curl_exit, http $http_code): $body" >&2
        # still emit whatever text/usage were gathered across earlier turns before
        # this failure — afm fails the stage via the non-zero exit below regardless
        # of what's printed here (pkg/executor doesn't look at subtype for that).
        jq -nc --arg t "$final_text" '{type:"assistant", message:{content:[{type:"text", text:$t}]}}'
        build_result_line "error_during_execution"
        exit 1
    fi

    reassembled=$(printf '%s' "$body" | jq -R -s '
        split("\n")
        | map(select(startswith("data: ")) | sub("^data: ";""))
        | map(select(test("^\\{")))
        | map(fromjson? // empty)
        | map(select(.choices != null and (.choices | length) > 0))
        | map(.choices[0]) as $choices
        | {
            content: ($choices | map(.delta.content // "") | join("")),
            tool_calls: (
                $choices
                | map(.delta.tool_calls // [])
                | flatten
                | group_by(.index)
                | map({
                    index: .[0].index,
                    id: ([.[] | .id // "" | select(. != "")] | first // ""),
                    name: ([.[] | .function.name // "" | select(. != "")] | first // ""),
                    arguments: ([.[] | .function.arguments // ""] | join(""))
                  })
            ),
            finish_reason: ([$choices[] | .finish_reason // "" | select(. != "")] | last // "")
          }
    ')

    tool_call_count=$(printf '%s' "$reassembled" | jq '.tool_calls | length')

    if [[ "$tool_call_count" -eq 0 ]]; then
        final_text=$(printf '%s' "$reassembled" | jq -r '.content')
        break
    fi

    assistant_msg=$(printf '%s' "$reassembled" | jq -c \
        '{role:"assistant", content: (.content // ""), tool_calls: [.tool_calls[] | {id: .id, type: "function", function: {name: .name, arguments: .arguments}}]}')
    jq -c --argjson m "$assistant_msg" '. + [$m]' "$messages_file" > "${messages_file}.tmp" && mv "${messages_file}.tmp" "$messages_file"

    for i in $(seq 0 $((tool_call_count - 1))); do
        call=$(printf '%s' "$reassembled" | jq -c ".tool_calls[$i]")
        call_id=$(printf '%s' "$call" | jq -r '.id')
        command=$(printf '%s' "$call" | jq -r '.arguments | fromjson? | .command // empty')

        # живой tool_use конверт — сразу в stdout: сбрасывает idle-timer и рисуется
        # в event feed дашборда (та же форма, что и реальный Bash tool_use у claude).
        jq -nc --arg cmd "$command" '{type:"assistant", message:{content:[{type:"tool_use", name:"Bash", input:{command:$cmd}}]}}'

        set +e
        tool_output=$(bash -c "$command" 2>&1)
        tool_exit=$?
        set -e
        if [[ ${#tool_output} -gt 15000 ]]; then
            tool_output="${tool_output:0:15000}
[...truncated]"
        fi
        tool_output="${tool_output}
[exit code: ${tool_exit}]"

        tool_msg=$(jq -nc --arg id "$call_id" --arg out "$tool_output" '{role:"tool", tool_call_id:$id, content:$out}')
        jq -c --argjson m "$tool_msg" '. + [$m]' "$messages_file" > "${messages_file}.tmp" && mv "${messages_file}.tmp" "$messages_file"

        # если вывод команды содержит [Screenshot: ...] (например, cat ответа на
        # диалог со вставленным скриншотом) — картинка идёт отдельным user-сообщением
        # сразу за tool-результатом: tool-роль в OpenAI-протоколе не гарантированно
        # поддерживает мультимодальный content, а user-роль — везде.
        img_blocks=$(extract_image_blocks "$tool_output")
        if [[ "$img_blocks" != "[]" ]]; then
            followup_msg=$(jq -nc --argjson imgs "$img_blocks" \
                '{role:"user", content: ([{type:"text", text:"Screenshot referenced in the tool result above:"}] + $imgs)}')
            jq -c --argjson m "$followup_msg" '. + [$m]' "$messages_file" > "${messages_file}.tmp" && mv "${messages_file}.tmp" "$messages_file"
        fi
    done
done

if [[ "$max_turns_reached" -eq 1 ]]; then
    final_text="${final_text}
[openai-agent: max turns reached, stopping]"
fi

jq -nc --arg t "$final_text" '{type:"assistant", message:{content:[{type:"text", text:$t}]}}'
build_result_line "success"
