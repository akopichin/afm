#!/bin/sh
# Fake stage agent for the report tests: scripted model and usage per stage and invocation, no LLM.
#   build: invocation 1 prices at a known model, invocation 2 uses a model without a rate, done on 2.
#   ship:  no usage event at all (unmetered).
#   other (runtime) stages: a priced model.
# Stages with AFM_IT_RELEASE_DIR set wait for <dir>/<stage> before finishing.
set -eu
stage=$(basename "$AFM_STAGE_DIR")
counter="$AFM_STAGE_DIR/report-invocations"
n=$(($(cat "$counter" 2>/dev/null || echo 0) + 1))
echo "$n" > "$counter"
[ -z "${AFM_IT_RUNS_LOG:-}" ] || echo "$stage:$n" >> "$AFM_IT_RUNS_LOG"
done_at=1
[ "$stage" != build ] || done_at=2
if [ -n "${AFM_IT_RELEASE_DIR:-}" ] && [ "$n" -ge "$done_at" ]; then
	while [ ! -e "$AFM_IT_RELEASE_DIR/$stage" ]; do sleep 0.02; done
fi
if [ "$n" -ge "$done_at" ]; then
	echo done > "$AFM_STAGE_DIR/.done"
	echo summary > "$AFM_STAGE_DIR/execution_summary.md"
fi
model=report-priced
[ "$stage:$n" != build:2 ] || model=report-unpriced
printf '{"type":"assistant","message":{"model":"%s","content":[{"type":"text","text":"working"}]}}\n' "$model"
if [ "$stage" != ship ]; then
	printf '{"type":"result","subtype":"success","usage":{"input_tokens":%s,"output_tokens":20}}\n' "$((100 * n))"
fi
