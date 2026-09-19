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
#                    is already isolated); ignored when CODEX_VERIFY=1.
#   CODEX_VERBOSE  — set to 1 to include command execution output (default: 0)
#   CODEX_HOME     — codex config dir (default: ~/.codex), read-only, only to
#                    resolve a fallback model id for the usage envelope below.
#   CODEX_VERIFY   — set to 1 for a read-only AI-verify pass (set by
#                    Executor.RunVerifyAgent, pkg/executor). Never passes the
#                    bypass/full-access flags, always requests -s read-only,
#                    and never escalates to broader access on error. Captures
#                    exactly the final answer via --output-last-message when
#                    the installed codex CLI supports it (probed via
#                    `codex exec --help`, no network call), otherwise falls
#                    back to the same aggregated agent_message text used for
#                    a normal run. Unset (default 0) keeps prior behavior
#                    byte-identical.
#
# codex's stderr flows through to this script's stderr (captured by afm's
# executor into <phase>.stderr.log). If codex exits non-zero (e.g. not
# logged in), this script prints a short diagnostic; it still emits the
# accumulated text + whatever usage was gathered before the failure (see
# below) so afm's accounting collector isn't left guessing, then exits with
# codex's own exit code — afm fails the stage regardless of what was printed.
#
# usage accounting: codex's own JSONL stream carries a public `turn.completed`
# event with a `usage` object (input/output/cached/reasoning token counts —
# numbers only, never prompt/response text or secrets). This script forwards
# that object verbatim into a synthetic terminal `result` line, in the shape
# pkg/accounting's Collector already parses (usage_contract_version:1,
# usage_schema:"openai", channel:"codex"). Model resolution precedence:
# $CODEX_MODEL (explicit) -> the safe top-level `model` key of codex's own
# config.toml (never rollout/session files) -> omitted (collector falls back
# to whatever it discovered/was hinted elsewhere).

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
CODEX_VERIFY="${CODEX_VERIFY:-0}"

# supports_output_last_message probes whether the installed codex CLI accepts
# --output-last-message (writes the agent's exact final message to a file).
# A --help probe, not a real invocation — no network call, no auth needed.
supports_output_last_message() {
    "${CODEX_BIN:-codex}" exec --help 2>/dev/null | grep -q -- '--output-last-message'
}

# last_msg_file stays empty outside verify mode (or when unsupported) —
# non-verify behavior must remain byte-identical to before this feature.
last_msg_file=""
if [[ "$CODEX_VERIFY" == "1" ]]; then
    # Read-only verify mode: never the bypass/full-access flags, always an
    # explicit supported read-only sandbox, never escalate on error.
    codex_args=(exec --json -s read-only)
    if supports_output_last_message; then
        last_msg_file=$(mktemp)
        codex_args+=(--output-last-message "$last_msg_file")
    fi
else
    codex_args=(exec --json --dangerously-bypass-approvals-and-sandbox -s "$CODEX_SANDBOX")
fi
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
trap 'rm -f "$out_file" "$last_msg_file"' EXIT

set +e
printf '%s' "$prompt" | "${CODEX_BIN:-codex}" "${codex_args[@]}" > "$out_file"
codex_exit=$?
set -e

final_text=""
usage_json=""
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
            # .usage is numeric-only (input/output/cached/reasoning token counts) —
            # never prompt/response text or secrets — safe to forward verbatim.
            # Keeps the LAST usage object seen, in case a run somehow reports more
            # than one turn.completed (a normal `codex exec` reports exactly one).
            u=$(printf '%s' "$line" | jq -c '.usage // empty' 2>/dev/null) || continue
            [[ -n "$u" && "$u" != "null" ]] && usage_json="$u"
            ;;
    esac
done < "$out_file"

# When codex wrote an exact final message (verify mode, supported CLI),
# it replaces the aggregated agent_message text wholesale — this is the
# EXACT final answer, not a concatenation of intermediate turns.
if [[ -n "$last_msg_file" && -s "$last_msg_file" ]]; then
    final_text=$(cat "$last_msg_file")
fi

# resolve_codex_model: $CODEX_MODEL (explicit) -> safe top-level `model` key from
# codex's own config.toml (text match only, no TOML eval, no rollout/session
# files) -> empty (omitted from the envelope below; the collector falls back to
# whatever it discovers/was hinted elsewhere).
resolve_codex_model() {
    if [[ -n "$CODEX_MODEL" ]]; then
        printf '%s' "$CODEX_MODEL"
        return
    fi
    local config_file="${CODEX_HOME:-$HOME/.codex}/config.toml"
    [[ -r "$config_file" ]] || return 0
    # only the top-level table (before the first [section] header) counts as
    # "the" model — a model key nested under e.g. [profiles.x] is scoped there,
    # not the effective default.
    awk '
        /^[[:space:]]*\[/ { exit }
        /^[[:space:]]*model[[:space:]]*=/ { print; exit }
    ' "$config_file" 2>/dev/null | sed -E 's/^[[:space:]]*model[[:space:]]*=[[:space:]]*"([^"]*)".*/\1/'
}
resolved_model=$(resolve_codex_model)

# build_result_line <subtype> — emits the terminal result line. When no usage was
# ever captured (process killed before any turn.completed, or a garbled stream),
# omits the usage envelope entirely rather than fabricating zeros: afm's
# Collector.Finish then reports an honest "unmetered" observation instead of a
# fake $0.
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

if [[ "$codex_exit" -ne 0 ]]; then
    echo "error: codex exited with status $codex_exit" >&2
    # still print whatever text/usage were gathered before the failure — a
    # process killed before any output at all just leaves both empty, which
    # Collector.Finish degrades to its own unmetered observation.
    jq -nc --arg t "$final_text" '{type:"assistant",message:{content:[{type:"text",text:$t}]}}'
    build_result_line "error_during_execution"
    exit "$codex_exit"
fi

# assistant-конверт: агрегированный текст всего ответа (matches openai-as-claude.sh /
# cursor-as-claude.sh pattern — afm's executor only accepts "assistant"-typed events,
# see pkg/executor/executor.go parseStreamEvent).
jq -nc --arg t "$final_text" '{type:"assistant",message:{content:[{type:"text",text:$t}]}}'
build_result_line "success"
