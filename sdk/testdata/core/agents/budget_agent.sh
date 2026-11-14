#!/bin/sh
# Fake agent with scripted token usage. Invocation N of a stage reports the Nth number of AFM_IT_BUDGET_TOKENS
# (the last one repeats); the stage completes on invocation AFM_IT_BUDGET_DONE_AT (default 1), earlier ones leave
# it unfinished so the orchestrator asks the agent again.
set -eu
stage=$(basename "$AFM_STAGE_DIR")
counter="$AFM_STAGE_DIR/budget-invocations"
n=$(($(cat "$counter" 2>/dev/null || echo 0) + 1))
echo "$n" > "$counter"
[ -z "${AFM_IT_RUNS_LOG:-}" ] || echo "$stage:$n" >> "$AFM_IT_RUNS_LOG"
tokens=$(echo "${AFM_IT_BUDGET_TOKENS:-10}" | tr ' ' '\n' | sed -n "${n}p")
[ -n "$tokens" ] || tokens=$(echo "${AFM_IT_BUDGET_TOKENS:-10}" | tr ' ' '\n' | tail -n 1)
if [ "$n" -ge "${AFM_IT_BUDGET_DONE_AT:-1}" ]; then
	echo done > "$AFM_STAGE_DIR/.done"
	echo summary > "$AFM_STAGE_DIR/execution_summary.md"
fi
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"working"}]}}'
printf '{"type":"result","subtype":"success","usage":{"input_tokens":%s,"output_tokens":0}}\n' "$tokens"
