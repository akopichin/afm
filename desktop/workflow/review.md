# Desktop review and escalation

The implementation author coordinates review in the shared worktree using
[afm-dev-coordinator](skills/afm-dev-coordinator/SKILL.md),
[afm-check-evidence](skills/afm-check-evidence/SKILL.md) and
[afm-dev-escalation](skills/afm-dev-escalation/SKILL.md). The latter defines the
Astra → Opus → Fabula route, including when each standing agent gets involved.
The coordinator, check-evidence, escalation, diagnosis and review skills come
from `dev-spec` and are distributed under the included [license](LICENSE).

For each planned task ID, complete its focused checks, freeze that task's
candidate and get independent review before starting the next task. The same
review loop applies when a task has to be split during implementation. Record
new task IDs with their parent and dependencies. A final verify audits the ledger
across the whole change.

Select the most relevant `desktop-dev-review-runtime`,
`desktop-dev-review-interface` and `desktop-dev-review-integrations` profiles.
For each product task, spawn two independent helpers, and the third when its
area is affected. Point their briefs to the task criteria and its diff from the
last accepted candidate. Start review as soon as the task candidate is ready.
For a docs-only task, use the relevant review profile.

Keep task ownership, request IDs, recovered waits and repair counts in
`$AFM_STAGE_DIR/team-progress.json` in `implement`, as in `dev-spec`. Keep frozen
candidates, evidence, helper results and dispositions in `review-ledger.md`
in the same directory. Later stages read and extend this task history. Record
outstanding native acceptance in the stage summary.
