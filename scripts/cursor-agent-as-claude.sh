#!/usr/bin/env bash
# Translate cursor-agent stream-json to Claude stream-json. The run fails (exit 1, error result) unless
# cursor-agent exits 0 and reports a non-error result, so a crashed or limited agent is never reported as success.

set -euo pipefail

command -v jq >/dev/null 2>&1 || { echo "error: jq is required but not found" >&2; exit 1; }

# Ignore every Claude CLI flag (--model, --session-id, --resume, ...): the recipe's model arrives via CURSOR_MODEL.
while [[ $# -gt 0 ]]; do
    shift
done

if [[ -t 0 ]]; then
    echo "error: no prompt on stdin (cursor-agent-as-claude requires prompt via stdin pipe)" >&2
    exit 1
fi
prompt=$(cat)
if [[ -z "$prompt" ]]; then
    echo "error: empty prompt" >&2
    exit 1
fi

# cursor-agent reads the key from its environment; it must never reach argv or the logs.
if [[ -z "${CURSOR_API_KEY:-}" ]]; then
    echo "error: CURSOR_API_KEY is not set" >&2
    exit 1
fi

CURSOR_MODEL="${CURSOR_MODEL:-auto}"
cursor_args=(-p --force --output-format stream-json --model "$CURSOR_MODEL")

# Raw cursor-agent stream: the translator reads it live, the result line is derived from it afterwards.
out_file=$(mktemp)
trap 'rm -f "$out_file"' EXIT

# One jq process keeps the thinking buffer between events and flushes each translated line immediately.
XLATE='
    def asst($blocks): {type:"assistant", message:{content:$blocks}};
    def tool_name($key): if $key == "shellToolCall" then "Bash"
        else ($key | sub("ToolCall$"; "") | (.[0:1] | ascii_upcase) + .[1:]) end;
    # Shell args carry a large parse tree; the feed only needs the command. Glob gets the "pattern" key the feed reads.
    def tool_input($key; $args): if $key == "shellToolCall" then {command: ($args.command // "")}
        elif $key == "globToolCall" then $args + {pattern: ($args.globPattern // "")}
        else $args end;
    foreach (inputs | fromjson? | select(type == "object")) as $e ({thinking: ""};
        if $e.type == "thinking" and $e.subtype == "delta" then .thinking += ($e.text // "") | .out = null
        elif $e.type == "thinking" and $e.subtype == "completed" then
            .out = (if (.thinking | test("\\S")) then asst([{type:"thinking", thinking:.thinking}]) else null end) | .thinking = ""
        elif $e.type == "assistant" then
            .out = ([$e.message.content[]? | select(.type == "text" and (.text // "") != "") | {type:"text", text:.text}]
                    | if length > 0 then asst(.) else null end)
        elif $e.type == "tool_call" and $e.subtype == "started" then
            ($e.tool_call | to_entries | first) as $call
            | .out = (if $call == null then null else
                asst([{type:"tool_use", id:($e.call_id // ""), name:tool_name($call.key), input:tool_input($call.key; $call.value.args // {})}]) end)
        elif $e.type == "tool_call" and $e.subtype == "completed" then
            # The end of a call: result.success carries the shell exit code, result.failure marks an error; no result
            # block is a plain completion. Output is not forwarded, only the code and the command for the feed.
            ($e.tool_call | to_entries | first) as $call
            | .out = (if $call == null or ($e.call_id // "") == "" then null else
                ($call.value.result // {}) as $r
                | ($r.success.exitCode? // $r.failure.exitCode? // null) as $code
                | (($r.failure // null) != null or (($code | type) == "number" and $code != 0)) as $failed
                | {type:"user", message:{content:[{type:"tool_result", tool_use_id:$e.call_id, is_error:$failed}]},
                   afm:({tool: tool_name($call.key)}
                        + (if $call.key == "shellToolCall" then {detail: ($call.value.args.command // "")} else {} end)
                        + (if ($code | type) == "number" then {exit_code:$code} else {} end))} end)
        else .out = null end;
        .out | select(. != null))
'

# The prompt goes through stdin so the model-sized text never lands in argv.
set +e
printf '%s' "$prompt" | "${CURSOR_BIN:-cursor-agent}" "${cursor_args[@]}" | tee "$out_file" | jq -nR --unbuffered -c "$XLATE" 2>/dev/null
cursor_exit=${PIPESTATUS[1]}
set -e

# The last result event decides success; cursor-agent output that never reached one is a failure.
result_event=$(jq -Rc 'fromjson? | select(type == "object" and .type == "result")' "$out_file" 2>/dev/null | tail -n1)
init_model=$(jq -Rr 'fromjson? | select(type == "object" and .type == "system" and .subtype == "init") | .model // empty' "$out_file" 2>/dev/null | tail -n1)

# The failure flag is kept apart from its message: an error result with a blank text is still an error.
failed=false
failure=""
if [[ "$cursor_exit" -ne 0 ]]; then
    failed=true
    failure="cursor-agent exited with status $cursor_exit"
elif [[ -z "$result_event" ]]; then
    failed=true
    failure="cursor-agent finished without a result event"
elif [[ "$(printf '%s' "$result_event" | jq -r '.is_error // false')" == "true" ]]; then
    failed=true
    failure=$(printf '%s' "$result_event" | jq -r '.result // "" | tostring')
    [[ "$failure" =~ [^[:space:]] ]] || failure="cursor-agent reported an error"
fi

# A result that carries the answer but no assistant event still has to reach the feed as text.
if ! "$failed" && ! jq -Re 'fromjson? | select(type == "object" and .type == "assistant")' "$out_file" >/dev/null 2>&1; then
    printf '%s' "$result_event" | jq -c '
        select((.result // "") != "") | {type:"assistant", message:{content:[{type:"text", text:.result}]}}'
fi

# Usage keys follow cursor-agent's camelCase result.usage; absent usage stays absent so accounting records unmetered.
build_result_line() {
    local subtype="$1" err="$2" usage="null"
    [[ -n "$result_event" ]] && usage=$(printf '%s' "$result_event" | jq -c '
        (.usage // null) | if type == "object" then {
            input_tokens: (.inputTokens // .input_tokens // 0),
            output_tokens: (.outputTokens // .output_tokens // 0),
            cache_read_input_tokens: (.cacheReadTokens // .cache_read_input_tokens // 0),
            cache_creation_input_tokens: (.cacheWriteTokens // .cache_creation_input_tokens // 0)} else null end')
    local model="$init_model"
    [[ -z "$model" && "$CURSOR_MODEL" != "auto" ]] && model="$CURSOR_MODEL"
    jq -nc --arg st "$subtype" --arg err "${err:0:4096}" --arg model "$model" --argjson usage "$usage" '
        {type:"result", subtype:$st}
        + (if $usage != null then {usage_contract_version:1, usage_schema:"anthropic", channel:"cursor", usage:$usage} else {} end)
        + (if $model != "" then {model:$model} else {} end)
        + (if $err != "" then {is_error:true, result:$err} else {} end)'
}

if "$failed"; then
    echo "error: $failure" >&2
    if [[ -s "$out_file" ]]; then
        echo "--- cursor-agent raw output (last 50 lines) ---" >&2
        tail -n 50 "$out_file" >&2
        echo "--- end cursor-agent raw output ---" >&2
    fi
    build_result_line "error_during_execution" "$failure"
    exit 1
fi

build_result_line "success" ""
