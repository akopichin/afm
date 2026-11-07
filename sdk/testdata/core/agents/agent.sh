#!/bin/sh
# Fake stage agent: plans and implements in one go, planning text goes to stdout as stream-json.
# Works for planning/implementation phases and for agents: [auto].
set -eu
stage=$(basename "$AFM_STAGE_DIR")
if [ -n "${AFM_IT_SINGLE_PHASE:-}" ] || [ -e "$AFM_STAGE_DIR/plan.md" ]; then phase=implementation; else phase=planning; fi
[ -z "${AFM_IT_RUNS_LOG:-}" ] || echo "$stage:$phase" >> "$AFM_IT_RUNS_LOG"
if [ -n "${AFM_IT_RECORD_DIR:-}" ]; then
	pwd -P > "$AFM_IT_RECORD_DIR/$stage.cwd"
	echo "$AFM_STAGE_DIR" > "$AFM_IT_RECORD_DIR/$stage.stagedir"
fi
if [ "$phase" = implementation ] && [ -n "${AFM_IT_RELEASE_DIR:-}" ]; then
	while [ ! -e "$AFM_IT_RELEASE_DIR/$stage" ]; do sleep 0.02; done
fi
if [ "$phase" = implementation ]; then
	echo done > "$AFM_STAGE_DIR/.done"
	echo summary > "$AFM_STAGE_DIR/execution_summary.md"
fi
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"## Tasks\n\n- [ ] step\n\n## Assumptions\n\nnone\n\n## Acceptance Criteria\n\n- done\n"}]}}'
echo '{"type":"result","subtype":"success","usage":{"input_tokens":10,"output_tokens":5}}'
