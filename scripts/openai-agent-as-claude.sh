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

# The bash tool has no timeout of its own, so a stage agent can wait for the user's answer much longer than
# the default `afm stage wait-answer` slice.
export AFM_WAIT_ANSWER_TIMEOUT="${AFM_WAIT_ANSWER_TIMEOUT:-600}"

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
MAX_SCHEMA_CORRECTIONS=2

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

# A helper run asks for live output: messages as they grow, tool starts and finishes, and the answer named by
# afm_final. Without the flag the output is the plain protocol.
stream_ext=0
[[ "${AFM_STREAM_EXTENSION:-}" == "1" ]] && stream_ext=1
if [[ "$stream_ext" -eq 1 ]]; then
    printf '%s\n' '{"type":"system","subtype":"afm_stream","version":1}'
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

# parse_tool_arguments <arguments-json> -> {"command":...} for a usable call, else {"error":<reason>}. A model that
# streams broken arguments must hear about it instead of getting an empty command run.
parse_tool_arguments() {
    jq -nc --rawfile args <(printf '%s' "$1") '
        try ($args | fromjson
             | if type != "object" then error("arguments must be a JSON object")
               elif (.command | type) != "string" or .command == "" then error("\"command\" must be a non-empty string")
               else {command: .command} end)
        catch {error: .}'
}

# schema_violation <text> -> the validator's message when the text breaks AFM_OUTPUT_SCHEMA_FILE, else nothing.
# Only an explicit output_invalid asks for a correction; a missing or broken validator accepts the answer.
schema_violation() {
    [[ -n "${AFM_OUTPUT_SCHEMA_FILE:-}" ]] && command -v afm >/dev/null 2>&1 || return 0
    local verdict_err
    if verdict_err=$(printf '%s' "$1" | afm schema validate --schema "$AFM_OUTPUT_SCHEMA_FILE" 2>&1 >/dev/null); then
        return 0
    fi
    printf '%s' "$verdict_err" | jq -r 'select(.error == "output_invalid") | .message[0:2000]' 2>/dev/null || true
}

# One long-lived jq per HTTP round turns the streamed text into partial message snapshots. A snapshot goes out
# once the text grew by max(256, half of what was last sent), so a long answer costs a linear amount of output
# however finely the provider chunks it. The complete message follows after the round.
LIVE_TEXT='
    foreach inputs as $line ({text: "", sent: 0, out: null};
        (($line | select(startswith("data: ")) | ltrimstr("data: ") | select(test("^\\{")) | fromjson?
          | .choices[0]?.delta?.content? | strings) // "") as $d
        | .text += $d
        | if (.text | length) >= (.sent + (if .sent == 0 then 80 else ([256, (.sent / 2)] | max) end))
          then .out = .text | .sent = (.text | length)
          else .out = null end;
        .out | select(. != null)
        | {type: "assistant", message: {content: [{type: "text", text: .}]}, afm: {item_id: $item, partial: true}})
'

# Linux limits each exec argument to 128 KiB on 4 KiB pages. Keep arbitrary payloads in stdin/files,
# including JSON history, images and generated commands.
work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT
messages_file="$work_dir/messages.json"
request_file="$work_dir/request.json"
command_file="$work_dir/tool.sh"
response_file="$work_dir/response.sse"
# The key reaches curl on its stdin: argv is readable by every local user, and a file would outlive a killed run.
auth_key=${OPENAI_API_KEY//\\/\\\\}
auth_key=${auth_key//\"/\\\"}
openai_curl() { printf 'header = "Authorization: Bearer %s"\n' "$auth_key" | curl -K - "$@"; }

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
schema_corrections=0
answer_item=""
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
    if [[ "$stream_ext" -eq 1 ]]; then
        # The body is kept whole in a file (usage and tool calls are read from it as before) while one jq shows
        # the text as it arrives; curl's own status is PIPESTATUS[0].
        openai_curl -sS -N -w '\n%{http_code}' \
            -H "Content-Type: application/json" \
            --data-binary "@$request_file" \
            "${OPENAI_BASE_URL}/chat/completions" | tee "$response_file" | jq -nRc --unbuffered --arg item "msg-$turn" "$LIVE_TEXT"
        curl_exit=${PIPESTATUS[0]}
        response=$(cat "$response_file")
    else
        response=$(openai_curl -sS -w '\n%{http_code}' \
            -H "Content-Type: application/json" \
            --data-binary "@$request_file" \
            "${OPENAI_BASE_URL}/chat/completions")
        curl_exit=$?
    fi
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
        # A 2xx streamed body is raw SSE deltas: a secret split across them evades redaction, so it is not echoed.
        if [[ "$stream_ext" -eq 1 && "$http_code" == 2?? ]]; then
            body="(streamed body omitted)"
        fi
        # Full body only on stderr (echo is a builtin, no exec argument limit); the result line gets a capped
        # copy because jq receives it as an exec argument.
        echo "error: $failure$body" >&2
        # still emit whatever text/usage were gathered across earlier turns before this failure — afm
        # fails the stage via the non-zero exit below regardless of what's printed here.
        if [[ "$stream_ext" -eq 0 ]]; then
            jq -nc --rawfile text <(printf '%s' "$final_text") '{type:"assistant", message:{content:[{type:"text", text:$text}]}}'
        fi
        build_result_line "error_during_execution" "$failure${body:0:4096}"
        exit 1
    fi

    # A stream is complete once the provider said it finished ([DONE] or a finish_reason). A body that merely
    # stops is a cut stream: an error, so no partial message is published and no tool of the round runs.
    if [[ "$stream_ext" -eq 1 ]]; then
        stream_done=$(jq -nr --rawfile sse <(printf '%s' "$body") '
            [$sse | split("\n")[] | sub("^data: ";"")
             | select(. == "[DONE]" or (test("^\\{") and ((fromjson? | .choices[0]?.finish_reason?) // "") != ""))]
            | length > 0')
        if [[ "$stream_done" != "true" ]]; then
            failure="response stream ended before completion (curl exit $curl_exit, http $http_code)"
            echo "error: $failure" >&2
            build_result_line "error_during_execution" "$failure"
            exit 1
        fi
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

    if [[ "$stream_ext" -eq 1 ]]; then
        # The round's complete message replaces its partial snapshots; the id is unique per round.
        printf '%s' "$reassembled" | jq -c --arg item "msg-$turn" \
            'select((.content // "") | test("\\S"))
             | {type:"assistant", message:{content:[{type:"text", text:.content}]}, afm:{item_id:$item, partial:false}}'
        answer_item="msg-$turn"
    fi

    if [[ "$tool_call_count" -eq 0 ]]; then
        final_text=$(printf '%s' "$reassembled" | jq -r '.content')
        violation=""
        if [[ "$schema_corrections" -lt "$MAX_SCHEMA_CORRECTIONS" ]]; then
            violation=$(schema_violation "$final_text")
        fi
        [[ -z "$violation" ]] && break
        schema_corrections=$((schema_corrections + 1))
        jq -nc --rawfile text <(printf '%s' "$final_text") '{role:"assistant", content:$text}' | append_message
        jq -nc --rawfile err <(printf 'Your final answer does not match the required JSON schema: %s\nReply again with only the corrected JSON document, without markdown fences or commentary.' "$violation") \
            '{role:"user", content:$err}' | append_message
        continue
    fi

    assistant_msg=$(printf '%s' "$reassembled" | jq -c \
        '{role:"assistant", content: (.content // ""), tool_calls: [.tool_calls[] | {id: .id, type: "function", function: {name: .name, arguments: .arguments}}]}')
    printf '%s' "$assistant_msg" | append_message

    for i in $(seq 0 $((tool_call_count - 1))); do
        call=$(printf '%s' "$reassembled" | jq -c ".tool_calls[$i]")
        call_id=$(printf '%s' "$call" | jq -r '.id')
        parsed=$(parse_tool_arguments "$(printf '%s' "$call" | jq -r '.arguments')")
        arguments_error=$(printf '%s' "$parsed" | jq -r '.error // empty')
        if [[ -n "$arguments_error" ]]; then
            jq -nc --arg err "error: invalid arguments JSON: ${arguments_error}; expected {\"command\": string}" --arg id "$call_id" \
                '{role:"tool", tool_call_id:$id, content:$err}' | append_message
            continue
        fi
        command=$(printf '%s' "$parsed" | jq -r '.command')

        # живой tool_use конверт — сразу в stdout: сбрасывает idle-timer и рисуется
        # в event feed дашборда (та же форма, что и реальный Bash tool_use у claude).
        if [[ "$stream_ext" -eq 1 ]]; then
            # Provider ids may repeat between rounds, so the round is part of the id.
            tool_id="t${turn}-${call_id:-$i}"
            jq -nc --rawfile cmd <(printf '%s' "$command") --arg id "$tool_id" \
                '{type:"assistant", message:{content:[{type:"tool_use", id:$id, name:"Bash", input:{command:$cmd}}]}}'
        else
            jq -nc --rawfile cmd <(printf '%s' "$command") '{type:"assistant", message:{content:[{type:"tool_use", name:"Bash", input:{command:$cmd}}]}}'
        fi

        printf '%s\n' "$command" > "$command_file"
        set +e
        # The schema file is the shim's own control; the subshell keeps it from the model's commands only.
        tool_output=$(unset AFM_OUTPUT_SCHEMA_FILE; bash "$command_file" 2>&1)
        tool_exit=$?
        set -e
        if [[ "$stream_ext" -eq 1 ]]; then
            # Only the exit code is published; the whole command is repeated for a finish whose start was missed, and
            # afm redacts it before it cuts it.
            jq -nc --rawfile cmd <(printf '%s' "$command") --arg id "$tool_id" --argjson code "$tool_exit" \
                '{type:"user", message:{content:[{type:"tool_result", tool_use_id:$id, is_error:($code != 0)}]},
                  afm:{exit_code:$code, tool:"Bash", detail:$cmd}}'
        fi
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

if [[ "$stream_ext" -eq 1 ]]; then
    # The answer message is named only when its text is the answer itself (not after the max-turns note).
    named_item=""
    [[ "$max_turns_reached" -eq 0 ]] && named_item="$answer_item"
    jq -nc --rawfile text <(printf '%s' "$final_text") --arg item "$named_item" \
        '{type:"afm_final", version:1, text:$text} + (if $item != "" then {item_id:$item} else {} end)'
else
    jq -nc --rawfile text <(printf '%s' "$final_text") '{type:"assistant", message:{content:[{type:"text", text:$text}]}}'
fi
build_result_line "success"
