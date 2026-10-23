#!/usr/bin/env bash
# OpenAI-compatible tool loop with one bash tool; preserve raw per-turn usage for accounting to normalize
# and sum.

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
OPENAI_REASONING_EFFORT="${OPENAI_REASONING_EFFORT:-}"

if [[ -z "${OPENAI_API_KEY:-}" ]]; then
    echo "error: OPENAI_API_KEY is not set" >&2
    exit 1
fi

system_prompt='You have exactly one tool, "bash", which runs a shell command in the current working directory and returns its combined stdout+stderr and exit code. Use it to read and write files, run scripts, and wait for external input (a blocking command is fine to run -- it will return once ready). If the task mentions a skill by name, first read its instructions with bash (e.g. `cat .claude/skills/<name>/SKILL.md` or `~/.claude/skills/<name>/SKILL.md`) before proceeding. If a message mentions [Screenshot: <path>], an image of it is attached directly to that message -- you do not need to read the file yourself. When the task is fully complete, respond with your final answer as plain text and do not call any tool.'

tools_json='[{"type":"function","function":{"name":"bash","description":"Execute a shell command in the current working directory and return its combined stdout+stderr and exit code.","parameters":{"type":"object","properties":{"command":{"type":"string","description":"The shell command to run"}},"required":["command"]}}}]'

# extract_image_blocks <text> -> JSON array of image_url content blocks for every readable [Screenshot:
# <path>] reference in text.
extract_image_blocks() {
    local text="$1"
    local blocks='[]'
    local marker path
    while IFS= read -r marker; do
        [[ -z "$marker" ]] && continue
        # marker уже включает скобки — обрезаем "[Screenshot: " и "]" в bash, чтобы обойтись
        # POSIX-совместимым -E.
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
        # base64 -w0 — GNU-only флаг. Портируемый вариант — читать файл через stdin (одинаковый вывод у
        # обеих реализаций) и убрать переводы строк вручную.
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

# extract_turn_usage <sse-body> -> compact flattened usage JSON object for the LAST chunk in this turn
# carrying a non-null .usage, or empty if none. Numbers only, never prompt/response text.
extract_turn_usage() {
    local sse="$1"
    jq -nrc --rawfile sse <(printf '%s' "$sse") '
        $sse | split("\n")[] |
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

# extract_turn_model <sse-body> -> the LAST non-empty .model seen across this turn's chunks.
extract_turn_model() {
    local sse="$1"
    jq -nr --rawfile sse <(printf '%s' "$sse") '
        $sse | split("\n")[] |
        sub("^data: ";"")
        | select(test("^\\{"))
        | fromjson?
        | (.model // empty)
    ' 2>/dev/null | grep -v '^$' | tail -n1 || true
}

# build_result_line <subtype> [error] — emits the terminal result line. An error message adds is_error and
# result, the shape afm's usage-limit detection reads (the body keeps error.code).
build_result_line() {
    local subtype="$1" err="${2:-}"
    if [[ "$upstream_usages" == "[]" ]]; then
        jq -nc --arg st "$subtype" --arg err "$err" \
            '{type:"result", subtype:$st} + (if $err != "" then {is_error:true, result:$err} else {} end)'
        return
    fi
    printf '%s' "$upstream_usages" | jq -c --arg st "$subtype" --arg model "$resolved_model" --arg err "$err" \
        '{type:"result", subtype:$st, usage_contract_version:1, usage_schema:"openai_chat", channel:"openai-api", upstream_usages:.}
         + (if $model != "" then {model:$model} else {} end)
         + (if $err != "" then {is_error:true, result:$err} else {} end)'
}

# Linux limits each exec argument to 128 KiB on 4 KiB pages. Keep arbitrary payloads in stdin/files,
# including JSON history, images and generated commands.
work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT
messages_file="$work_dir/messages.json"
request_file="$work_dir/request.json"
command_file="$work_dir/tool.sh"

# Read one message from stdin and append it without putting its JSON in argv.
append_message() {
    jq -c --slurpfile msgs "$messages_file" '$msgs[0] + [.]' > "${messages_file}.tmp"
    mv "${messages_file}.tmp" "$messages_file"
}

user_content=$(build_user_content "$prompt")
printf '%s' "$user_content" | jq -c --arg sys "$system_prompt" \
    '[{role:"system", content:$sys}, {role:"user", content:.}]' > "$messages_file"

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

    jq -nc --slurpfile msgs "$messages_file" --arg model "$OPENAI_MODEL" --arg re "$OPENAI_REASONING_EFFORT" --argjson tools "$tools_json" \
        '{model: $model, stream: true, stream_options: {include_usage: true}, tool_choice: "auto", tools: $tools, messages: $msgs[0]}
         + (if $re != "" then {reasoning_effort: $re} else {} end)' > "$request_file"

    set +e
    response=$(curl -sS -w '\n%{http_code}' \
        -H "Content-Type: application/json" \
        -H "Authorization: Bearer $OPENAI_API_KEY" \
        --data-binary "@$request_file" \
        "${OPENAI_BASE_URL}/chat/completions")
    curl_exit=$?
    set -e

    http_code=$(printf '%s' "$response" | tail -n1)
    body=$(printf '%s' "$response" | sed '$d')

    turn_model=$(extract_turn_model "$body")
    [[ -n "$turn_model" ]] && resolved_model="$turn_model"
    turn_usage=$(extract_turn_usage "$body")
    if [[ -n "$turn_usage" ]]; then
        upstream_usages=$(printf '%s' "$upstream_usages" | jq -c --argjson item "$turn_usage" '. + [$item]')
    fi

    if [[ "$curl_exit" -ne 0 || "$http_code" -lt 200 || "$http_code" -ge 300 ]]; then
        failure="request to $OPENAI_BASE_URL failed (curl exit $curl_exit, http $http_code): "
        # Full body only on stderr (echo is a builtin, no exec argument limit); the result line gets a capped
        # copy because jq receives it as an exec argument.
        echo "error: $failure$body" >&2
        # still emit whatever text/usage were gathered across earlier turns before this failure — afm
        # fails the stage via the non-zero exit below regardless of what's printed here.
        jq -nc --rawfile text <(printf '%s' "$final_text") '{type:"assistant", message:{content:[{type:"text", text:$text}]}}'
        build_result_line "error_during_execution" "$failure${body:0:4096}"
        exit 1
    fi

    reassembled=$(jq -n --rawfile sse <(printf '%s' "$body") '
        $sse | split("\n")
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
    printf '%s' "$assistant_msg" | append_message

    for i in $(seq 0 $((tool_call_count - 1))); do
        call=$(printf '%s' "$reassembled" | jq -c ".tool_calls[$i]")
        call_id=$(printf '%s' "$call" | jq -r '.id')
        command=$(printf '%s' "$call" | jq -r '.arguments | fromjson? | .command // empty')

        # живой tool_use конверт — сразу в stdout: сбрасывает idle-timer и рисуется
        # в event feed дашборда (та же форма, что и реальный Bash tool_use у claude).
        jq -nc --rawfile cmd <(printf '%s' "$command") '{type:"assistant", message:{content:[{type:"tool_use", name:"Bash", input:{command:$cmd}}]}}'

        printf '%s\n' "$command" > "$command_file"
        set +e
        tool_output=$(bash "$command_file" 2>&1)
        tool_exit=$?
        set -e
        if [[ ${#tool_output} -gt 15000 ]]; then
            tool_output="${tool_output:0:15000}
[...truncated]"
        fi
        tool_output="${tool_output}
[exit code: ${tool_exit}]"

        jq -nc --rawfile out <(printf '%s' "$tool_output") --arg id "$call_id" '{role:"tool", tool_call_id:$id, content:$out}' | append_message

        # Картинку из tool-output отправляем отдельным user-сообщением: мультимодальность tool-роли
        # поддерживается не везде.
        img_blocks=$(extract_image_blocks "$tool_output")
        if [[ "$img_blocks" != "[]" ]]; then
            printf '%s' "$img_blocks" | jq -c \
                '{role:"user", content: ([{type:"text", text:"Screenshot referenced in the tool result above:"}] + .)}' | append_message
        fi
    done
done

if [[ "$max_turns_reached" -eq 1 ]]; then
    final_text="${final_text}
[openai-agent: max turns reached, stopping]"
fi

jq -nc --rawfile text <(printf '%s' "$final_text") '{type:"assistant", message:{content:[{type:"text", text:$text}]}}'
build_result_line "success"
