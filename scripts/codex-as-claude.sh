#!/usr/bin/env bash
# codex-as-claude.sh — wraps the codex CLI to produce Claude-compatible
# stream-json output, so afm's executor (pkg/executor, claude stream-json only)
# can drive codex the same way it drives claude.
#
# environment variables:
#   CODEX_BIN      — abs path to the real codex binary (set by the autoShim
#                    "codex" wrapper, see pkg/docker/wrapper.go, to avoid PATH
#                    recursion — the wrapper shadows the bare "codex" name).
#                    Falls back to bare "codex" when unset (local, non-Docker use).
#   CODEX_MODEL    — codex model to use (default: codex's own default)
#   CODEX_SANDBOX  — sandbox mode (default: danger-full-access — the container
#                    is already isolated)
#   CODEX_VERBOSE  — set to 1 to include command execution output (default: 0)

set -euo pipefail

command -v jq >/dev/null 2>&1 || { echo "error: jq is required but not found" >&2; exit 1; }

# prompt via stdin (primary path — matches how afm's executor pipes the prompt
# to claude). also accept -p for direct invocations; all other flags
# (--dangerously-skip-permissions etc., unconditionally added by afm's executor
# for claude-compatible commands) are ignored.
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

codex_args=(exec --json --dangerously-bypass-approvals-and-sandbox -s "$CODEX_SANDBOX")
[[ -n "$CODEX_MODEL" ]] && codex_args+=(-m "$CODEX_MODEL")

# run codex with JSON output, translate events to claude stream-json format.
# only agent messages are emitted by default — command executions and file
# reads produce excessive noise; set CODEX_VERBOSE=1 to include them.
#
# event mapping:
#   item.completed + agent_message     -> content_block_delta (text_delta)
#   item.completed + command_execution -> skipped (or included if CODEX_VERBOSE=1)
#   item.completed + reasoning         -> skipped
#   turn.completed                     -> result (end of execution)
#   everything else                    -> skipped
CODEX_VERBOSE="${CODEX_VERBOSE:-0}"
if [[ "$CODEX_VERBOSE" != "0" && "$CODEX_VERBOSE" != "1" ]]; then
    echo "warning: CODEX_VERBOSE must be 0 or 1, got '$CODEX_VERBOSE', defaulting to 0" >&2
    CODEX_VERBOSE=0
fi

printf '%s' "$prompt" | "${CODEX_BIN:-codex}" "${codex_args[@]}" 2>/dev/null | while IFS= read -r line; do
    echo "$line" | jq -c --argjson verbose "$CODEX_VERBOSE" '
        if .type == "item.completed" then
            if .item.type == "agent_message" then
                {type: "content_block_delta", delta: {type: "text_delta", text: (.item.text + "\n")}}
            elif .item.type == "command_execution" and $verbose == 1 then
                {type: "content_block_delta", delta: {type: "text_delta",
                    text: ("$ " + .item.command + "\n" + (.item.aggregated_output // "") + "\n")}}
            else empty
            end
        elif .type == "turn.completed" then
            {type: "result", result: ""}
        else empty
        end
    ' 2>/dev/null || true
done || true

echo '{"type":"result","result":""}'
