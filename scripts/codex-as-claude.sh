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
#
# codex's stderr flows through to this script's stderr (captured by afm's
# executor into <phase>.stderr.log). If codex exits non-zero (e.g. not
# logged in), this script prints a short diagnostic and exits with the same
# code instead of emitting a success envelope — afm fails the stage.

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

# run codex with JSON output, accumulate agent messages, emit one assistant event.
# only agent messages are accumulated by default — command executions and file
# reads produce excessive noise; set CODEX_VERBOSE=1 to include them.
#
# event flow:
#   Parse all item.completed events and accumulate:
#     + agent_message        -> accumulate text
#     + command_execution    -> accumulate if CODEX_VERBOSE=1
#     + other types          -> skip
#   When all events are processed, emit one aggregated "assistant" event
#   (matches openai-as-claude.sh / cursor-as-claude.sh pattern;
#   pkg/executor/executor.go parseStreamEvent only accepts "assistant"-typed events).
CODEX_VERBOSE="${CODEX_VERBOSE:-0}"
if [[ "$CODEX_VERBOSE" != "0" && "$CODEX_VERBOSE" != "1" ]]; then
    echo "warning: CODEX_VERBOSE must be 0 or 1, got '$CODEX_VERBOSE', defaulting to 0" >&2
    CODEX_VERBOSE=0
fi

# codex's raw JSONL output goes to a temp file (not a process substitution)
# so we can reliably capture its exit status: a failing pipeline inside a
# `set -e` process-substitution subshell can abort before $? is readable.
# set -e is suspended around just this one invocation for the same reason.
out_file=$(mktemp)
trap 'rm -f "$out_file"' EXIT

set +e
printf '%s' "$prompt" | "${CODEX_BIN:-codex}" "${codex_args[@]}" > "$out_file"
codex_exit=$?
set -e

final_text=""
while IFS= read -r line; do
    ev_type=$(printf '%s' "$line" | jq -r '.type // empty' 2>/dev/null) || continue
    [[ "$ev_type" == "item.completed" ]] || continue
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
done < "$out_file"

if [[ "$codex_exit" -ne 0 ]]; then
    echo "error: codex exited with status $codex_exit" >&2
    exit "$codex_exit"
fi

# assistant-конверт: агрегированный текст всего ответа (matches openai-as-claude.sh /
# cursor-as-claude.sh pattern — afm's executor only accepts "assistant"-typed events,
# see pkg/executor/executor.go parseStreamEvent).
jq -nc --arg t "$final_text" '{type:"assistant",message:{content:[{type:"text",text:$t}]}}'
echo '{"type":"result","subtype":"success"}'
