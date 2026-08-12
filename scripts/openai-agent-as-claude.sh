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

system_prompt='You have exactly one tool, "bash", which runs a shell command in the current working directory and returns its combined stdout+stderr and exit code. Use it to read and write files, run scripts, and wait for external input (a blocking command is fine to run -- it will return once ready). If the task mentions a skill by name, first read its instructions with bash (e.g. `cat .claude/skills/<name>/SKILL.md` or `~/.claude/skills/<name>/SKILL.md`) before proceeding. When the task is fully complete, respond with your final answer as plain text and do not call any tool.'

tools_json='[{"type":"function","function":{"name":"bash","description":"Execute a shell command in the current working directory and return its combined stdout+stderr and exit code.","parameters":{"type":"object","properties":{"command":{"type":"string","description":"The shell command to run"}},"required":["command"]}}}]'

messages_file=$(mktemp)
trap 'rm -f "$messages_file" "${messages_file}.tmp"' EXIT

jq -nc --arg sys "$system_prompt" --arg user "$prompt" \
    '[{role:"system", content:$sys}, {role:"user", content:$user}]' > "$messages_file"

final_text=""
turn=0
max_turns_reached=0

while :; do
    turn=$((turn + 1))
    if [[ "$turn" -gt "$OPENAI_AGENT_MAX_TURNS" ]]; then
        max_turns_reached=1
        break
    fi

    request_body=$(jq -nc --slurpfile msgs "$messages_file" --arg model "$OPENAI_MODEL" --argjson tools "$tools_json" \
        '{model: $model, stream: true, tool_choice: "auto", tools: $tools, messages: $msgs[0]}')

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

    if [[ "$curl_exit" -ne 0 || "$http_code" -lt 200 || "$http_code" -ge 300 ]]; then
        echo "error: request to $OPENAI_BASE_URL failed (curl exit $curl_exit, http $http_code): $body" >&2
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
    done
done

if [[ "$max_turns_reached" -eq 1 ]]; then
    final_text="${final_text}
[openai-agent: max turns reached, stopping]"
fi

jq -nc --arg t "$final_text" '{type:"assistant", message:{content:[{type:"text", text:$t}]}}'
echo '{"type":"result","subtype":"success"}'
