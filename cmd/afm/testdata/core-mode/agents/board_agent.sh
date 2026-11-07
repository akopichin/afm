#!/bin/sh
# Fake agent that publishes a board fact, waits for the key "release" to appear, then finishes like agent.sh.
set -eu
stage=$(basename "$AFM_STAGE_DIR")
afm=${AFM_IT_BIN:-afm}
if [ -e "$AFM_STAGE_DIR/plan.md" ]; then phase=implementation; else phase=planning; fi
[ -z "${AFM_IT_RUNS_LOG:-}" ] || echo "$stage:$phase" >> "$AFM_IT_RUNS_LOG"
"$afm" agent board publish --key "ready/$stage" --status ready --expected-version 0 > /dev/null
"$afm" agent wait --condition board_key --key release --status open --deadline-seconds 60 > /dev/null
echo done > "$AFM_STAGE_DIR/.done"
echo summary > "$AFM_STAGE_DIR/execution_summary.md"
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"## Tasks\n\n- [ ] step\n\n## Assumptions\n\nnone\n\n## Acceptance Criteria\n\n- done\n"}]}}'
echo '{"type":"result","subtype":"success"}'
