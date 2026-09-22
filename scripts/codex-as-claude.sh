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
#                    back to aggregated agent_message text. Unset (default 0)
#                    STREAMS each item.completed live: agent_message/reasoning
#                    -> text, command_execution -> Bash tool_use (+ output
#                    text when CODEX_VERBOSE=1); CODEX_VERIFY=1 aggregates
#                    into ONE final answer for strict JSON decoding.
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

# run codex with JSON output.
#
# Two modes:
#   * CODEX_VERIFY=1 — aggregation path (UNCHANGED): codex runs to completion,
#     all agent_message text is concatenated into ONE assistant envelope (or the
#     exact --output-last-message final answer), because afm strictly JSON-decodes
#     the verify agent's text buffer (DecodeModelResult) — streamed narration or
#     tool rows would corrupt it.
#   * otherwise — streaming path: each codex item.completed is translated to its
#     OWN Claude assistant line the moment it arrives, so the dashboard feed shows
#     codex's thoughts and actions live (agent_message/reasoning -> text,
#     command_execution -> Bash tool_use). afm's executor.parseStreamEvent accepts
#     only type=="assistant" lines. (codex does file edits via command_execution,
#     so they already surface as Bash rows — no separate file_change mapping.)
CODEX_VERBOSE="${CODEX_VERBOSE:-0}"
if [[ "$CODEX_VERBOSE" != "0" && "$CODEX_VERBOSE" != "1" ]]; then
    echo "warning: CODEX_VERBOSE must be 0 or 1, got '$CODEX_VERBOSE', defaulting to 0" >&2
    CODEX_VERBOSE=0
fi

# codex's raw JSONL always goes to a temp file so we can (a) capture usage from
# turn.completed after the stream, (b) read the verify final message, and (c) in
# verify mode aggregate the full answer. Streaming mode ALSO tees each line to
# this file while translating live.
out_file=$(mktemp)
trap 'rm -f "$out_file" "$last_msg_file"' EXIT

# XLATE: per-line jq program (streaming path). One assistant/tool_use line per
# codex item.completed. Kept per-line (printf | jq) so a single malformed line
# can't abort the whole stream — same robustness the aggregation loop relies on.
# Narration (agent_message/reasoning) emits nothing unless the text has at least
# one non-whitespace character (test("\S") — no blank feed bubbles), keeping the
# ORIGINAL text (no trimming). reasoning resolves its source by shape: .item.text
# (string) wins, else a summary ARRAY (codex's RawResponseItemCompletedNotification
# shape) is normalized via map(.text // "") | join("\n"), else a plain-string
# summary, else "" — an array must never reach the string concat (jq would error
# and || true would silently drop the event).
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

# --- streaming mode (default) ---
# Each raw line is appended to out_file AND translated live. codex is the 2nd
# stage of the pipeline, so its exit status is ${PIPESTATUS[1]} (printf=0,
# codex=1, subshell=2). set -e is suspended so a non-zero codex exit is captured
# rather than aborting the script before we read PIPESTATUS.
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
