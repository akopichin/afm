#!/bin/sh
# Fake interactive agent: asks one question during planning and returns the plan only after the answer.
set -eu
stage=$(basename "$AFM_STAGE_DIR")
afm=${AFM_IT_BIN:-afm}
if [ -e "$AFM_STAGE_DIR/plan.md" ]; then phase=implementation; else phase=planning; fi
[ -z "${AFM_IT_RUNS_LOG:-}" ] || echo "$stage:$phase" >> "$AFM_IT_RUNS_LOG"
if [ "$phase" = planning ]; then
	echo "which database?" | "$afm" stage ask --option sqlite --option postgres > /dev/null
	until "$afm" stage wait-answer --timeout 5 > /dev/null; do :; done
	printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"## Tasks\n\n- [ ] step\n\n## Assumptions\n\nnone\n\n## Acceptance Criteria\n\n- done\n"}]}}'
	echo '{"type":"result","subtype":"success"}'
	exit 0
fi
if [ -n "${AFM_IT_RELEASE_DIR:-}" ]; then
	while [ ! -e "$AFM_IT_RELEASE_DIR/$stage" ]; do sleep 0.02; done
fi
echo done > "$AFM_STAGE_DIR/.done"
echo summary > "$AFM_STAGE_DIR/execution_summary.md"
echo '{"type":"result","subtype":"success"}'
