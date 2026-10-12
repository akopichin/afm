#!/usr/bin/env bash
# Translate Codex JSONL to Claude stream-json; CODEX_VERIFY aggregates final output, normal runs stream
# live.

set -euo pipefail

command -v jq >/dev/null 2>&1 || { echo "error: jq is required but not found" >&2; exit 1; }

# Read the prompt from stdin or -p; ignore other Claude-compatible flags.
prompt=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        -p) prompt="${2:-}"; shift; shift 2>/dev/null || true ;;
        *)  shift ;;
    esac
done

if [[ -z "$prompt" ]]; then
    if [[ ! -t 0 ]]; then
        prompt=$(cat)
    fi
fi

if [[ -z "$prompt" ]]; then
    echo "error: no prompt provided (expected -p flag or stdin)" >&2
    exit 1
fi

CODEX_MODEL="${CODEX_MODEL:-}"
CODEX_SANDBOX="${CODEX_SANDBOX:-danger-full-access}"
CODEX_VERIFY="${CODEX_VERIFY:-0}"

# Probe exec --help for --output-last-message support without network or authentication.
supports_output_last_message() {
    "${CODEX_BIN:-codex}" exec --help 2>/dev/null | grep -q -- '--output-last-message'
}

# last_msg_file stays empty outside verify mode (or when unsupported) —
# non-verify behavior must remain byte-identical to before this feature.
last_msg_file=""
if [[ "$CODEX_VERIFY" == "1" ]]; then
    # AI-verify pass. The permission model is chosen by the isolation boundary:
    if [[ "${AFM_IN_DOCKER:-}" == "1" ]]; then
        # Use full access in afm's container: bwrap cannot create its namespace there; the container
        # provides isolation.
        codex_args=(exec --json --dangerously-bypass-approvals-and-sandbox -s danger-full-access)
    else
        # Host verification always uses read-only sandboxing; never bypass it or escalate on failure.
        codex_args=(exec --json -s read-only)
    fi
    if supports_output_last_message; then
        last_msg_file=$(mktemp)
        codex_args+=(--output-last-message "$last_msg_file")
    fi
else
    codex_args=(exec --json --dangerously-bypass-approvals-and-sandbox -s "$CODEX_SANDBOX")
fi
[[ -n "$CODEX_MODEL" ]] && codex_args+=(-m "$CODEX_MODEL")

# Author-only browser MCP passes a descriptor path, never its token. jq escapes command/args for Codex's
# TOML overrides.
if [[ "$CODEX_VERIFY" != "1" && -n "${AFM_BROWSER_DESCRIPTOR:-}" ]]; then
    if [[ -n "${AFM_BROWSER_LAUNCHER:-}" ]]; then
        browser_cmd_val=$(jq -nc --arg v "$AFM_BROWSER_LAUNCHER" '$v')
        browser_args_val=$(jq -nc --arg d "$AFM_BROWSER_DESCRIPTOR" '["browser","mcp","--descriptor",$d]')
        codex_args+=(-c "mcp_servers.afm_chrome.command=$browser_cmd_val")
        codex_args+=(-c "mcp_servers.afm_chrome.args=$browser_args_val")
    fi
    # If AFM_BROWSER_LAUNCHER is unset, skip the override rather than guess a bare
    # "afm" that may not be on PATH under that name.
fi

# resolve_codex_model: $CODEX_MODEL -> safe top-level `model` key from codex's own config.toml -> empty.
resolve_codex_model() {
    if [[ -n "$CODEX_MODEL" ]]; then
        printf '%s' "$CODEX_MODEL"
        return
    fi
    local config_file="${CODEX_HOME:-$HOME/.codex}/config.toml"
    [[ -r "$config_file" ]] || return 0
    # only the top-level table (before the first [section] header) counts as "the" model — a model key
    # nested under e.g. [profiles.x] is scoped there, not the effective default.
    awk '
        /^[[:space:]]*\[/ { exit }
        /^[[:space:]]*model[[:space:]]*=/ { print; exit }
    ' "$config_file" 2>/dev/null | sed -E 's/^[[:space:]]*model[[:space:]]*=[[:space:]]*"([^"]*)".*/\1/'
}

# Emit the terminal result; omit absent usage so accounting records unmetered rather than zero cost.
build_result_line() {
    local subtype="$1"
    if [[ -z "$usage_json" ]]; then
        jq -nc --arg st "$subtype" '{type:"result", subtype:$st}'
        return
    fi
    jq -nc --arg st "$subtype" --arg model "$resolved_model" --argjson usage "$usage_json" \
        '{type:"result", subtype:$st, usage_contract_version:1, usage_schema:"openai", channel:"codex", usage:$usage}
         + (if $model != "" then {model:$model} else {} end)'
}

# Verify emits one final answer for strict JSON decoding; normal runs translate each completed item live.
CODEX_VERBOSE="${CODEX_VERBOSE:-0}"
if [[ "$CODEX_VERBOSE" != "0" && "$CODEX_VERBOSE" != "1" ]]; then
    echo "warning: CODEX_VERBOSE must be 0 or 1, got '$CODEX_VERBOSE', defaulting to 0" >&2
    CODEX_VERBOSE=0
fi

# Keep raw JSONL for usage and final-answer extraction; streaming also translates it live.
out_file=$(mktemp)
trap 'rm -f "$out_file" "$last_msg_file"' EXIT

# Translate each completed item independently so malformed lines cannot stop the stream; normalize
# reasoning text/summary shapes.
XLATE='
    def asst($t): {type:"assistant",message:{content:[{type:"text",text:$t}]}};
    def tool($n;$inp): {type:"assistant",message:{content:[{type:"tool_use",name:$n,input:$inp}]}};
    def narrated($t): if ($t | test("\\S")) then asst($t + "\n") else empty end;
    def reasoning_text:
        if (.item.text | type) == "string" then .item.text
        elif (.item.summary | type) == "array" then (.item.summary | map(.text // "") | join("\n"))
        elif (.item.summary | type) == "string" then .item.summary
        else ""
        end;
    if .type == "item.completed" then
        (.item.type) as $it
        | if $it == "agent_message" then
            ((.item.text // "")) as $m
            | narrated($m)
        elif $it == "reasoning" then
            reasoning_text as $r
            | narrated($r)
        elif $it == "command_execution" then
            tool("Bash"; {command: (.item.command // "")}),
            (if $verbose == 1 and ((.item.aggregated_output // "") | length) > 0
             then asst((.item.aggregated_output) + "\n") else empty end)
        elif ($it == "mcp_tool_call" or $it == "mcp_call") then
            # Managed browser and other MCP tool calls: surface as a visible tool_use so the feed shows the
            # action and the idle timer resets. Field names are codex-version-dependent — best-effort display.
            tool(((.item.tool // .item.name // "mcp") | tostring);
                 {server: (.item.server // ""), arguments: (.item.arguments // .item.input // {})})
        else empty
        end
    else empty
    end
'

# extract_usage <file> — sets the global usage_json from the LAST turn.completed
# .usage seen in the raw codex stream (numeric-only, safe to forward verbatim).
usage_json=""
extract_usage() {
    local f="$1" line t u
    while IFS= read -r line; do
        t=$(printf '%s' "$line" | jq -r '.type // empty' 2>/dev/null) || continue
        if [[ "$t" == "turn.completed" ]]; then
            u=$(printf '%s' "$line" | jq -c '.usage // empty' 2>/dev/null) || continue
            if [[ -n "$u" && "$u" != "null" ]]; then
                usage_json="$u"
            fi
        fi
    done < "$f"
}

if [[ "$CODEX_VERIFY" == "1" ]]; then
    # --- verify mode: UNCHANGED aggregation path ---
    set +e
    printf '%s' "$prompt" | "${CODEX_BIN:-codex}" "${codex_args[@]}" > "$out_file"
    codex_exit=$?
    set -e

    final_text=""
    while IFS= read -r line; do
        ev_type=$(printf '%s' "$line" | jq -r '.type // empty' 2>/dev/null) || continue
        case "$ev_type" in
            item.completed)
                item_type=$(printf '%s' "$line" | jq -r '.item.type // empty' 2>/dev/null) || continue
                case "$item_type" in
                    agent_message)
                        text=$(printf '%s' "$line" | jq -r '.item.text // empty' 2>/dev/null) || continue
                        final_text="${final_text}${text}"$'\n'
                        ;;
                    command_execution)
                        if [[ "$CODEX_VERBOSE" == "1" ]]; then
                            cmd=$(printf '%s' "$line" | jq -r '.item.command // empty' 2>/dev/null) || continue
                            out=$(printf '%s' "$line" | jq -r '.item.aggregated_output // empty' 2>/dev/null) || continue
                            final_text="${final_text}\$ ${cmd}"$'\n'"${out}"$'\n'
                        fi
                        ;;
                esac
                ;;
            turn.completed)
                u=$(printf '%s' "$line" | jq -c '.usage // empty' 2>/dev/null) || continue
                [[ -n "$u" && "$u" != "null" ]] && usage_json="$u"
                ;;
        esac
    done < "$out_file"

    if [[ -n "$last_msg_file" && -s "$last_msg_file" ]]; then
        final_text=$(cat "$last_msg_file")
    fi

    resolved_model=$(resolve_codex_model)

    if [[ "$codex_exit" -ne 0 ]]; then
        echo "error: codex exited with status $codex_exit" >&2
        jq -nc --arg t "$final_text" '{type:"assistant",message:{content:[{type:"text",text:$t}]}}'
        build_result_line "error_during_execution"
        exit "$codex_exit"
    fi

    jq -nc --arg t "$final_text" '{type:"assistant",message:{content:[{type:"text",text:$t}]}}'
    build_result_line "success"
    exit 0
fi

# Capture Codex's PIPESTATUS[1] with errexit suspended while teeing and translating each line.
set +e
printf '%s' "$prompt" | "${CODEX_BIN:-codex}" "${codex_args[@]}" | {
    while IFS= read -r line; do
        printf '%s\n' "$line" >> "$out_file"
        printf '%s' "$line" | jq -c --argjson verbose "$CODEX_VERBOSE" "$XLATE" 2>/dev/null || true
    done
}
codex_exit=${PIPESTATUS[1]}
set -e

extract_usage "$out_file"
resolved_model=$(resolve_codex_model)

if [[ "$codex_exit" -ne 0 ]]; then
    echo "error: codex exited with status $codex_exit" >&2
    # Items already streamed live; just emit the terminal result and propagate.
    build_result_line "error_during_execution"
    exit "$codex_exit"
fi

build_result_line "success"
