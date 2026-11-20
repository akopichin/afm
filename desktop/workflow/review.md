# Desktop review and escalation

The implementation author coordinates an initial review and one focused
correction review. Record each frozen candidate, check evidence, task ID,
message ID and disposition in `$AFM_STAGE_DIR/review-ledger.md` before yielding.
For later stages use the same ledger in the sibling `implement` directory.

- Spawn the applicable read-only profiles with `afm agent spawn`. Cover
  runtime and packaging, interface and performance, and assistant and browser
  where affected. Give each reviewer this file, the worktree scope, candidate,
  acceptance criteria, prior findings, and a finite check budget.
- Wait with `afm agent wait --condition reply_to_all --message-ids ID1,ID2` for
  a batch or `reply_to` for one message. End the turn on `yield`. Retrieve each
  full result with `afm agent helper-result --task-id ID`. Recover pending
  requests from the ledger after restart. An absent or inconclusive result
  requires independent evidence before acceptance.
- Count rejected candidates and unsuccessful repairs by logical defect.
  Multiple reviewers rejecting the same candidate count once. After two
  ordinary rejections for the same defect, stop ordinary fixes. Give the
  standing `rescue-architect` a bounded diagnosis via
  `afm agent send --to rescue-architect --kind request` with the reproduction,
  attempted repairs, logs and scope. Wait for the linked full reply.
- After evidence confirms the diagnosis, record the previous writer's
  ownership release and grant standing `opus` one bounded correction.
  Other writers leave the granted files untouched. Independently review the
  corrected candidate, rerun affected checks and record the results.
- Ask the owner before a further rescue after the first post-escalation
  rejection. After two distinct post-escalation rejections of the same defect,
  ask standing `fabula` for one independent verification. A concrete residual
  defect may receive one bounded Fabula repair with recorded exclusive
  ownership and fresh non-author review. Escalate unresolved blockers through
  the interactive stage dialog with the evidence and choices.

Record the final dispositions and outstanding host acceptance in
`execution_summary.md`. The existing npm and Codex verify steps still apply.
