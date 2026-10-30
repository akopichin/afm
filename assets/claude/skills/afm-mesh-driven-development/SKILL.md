---
name: afm-mesh-driven-development
description: Use when executing an implementation plan inside an afm run by delegating tasks to named AFM team/mesh agents via afm agent send/wait — writers share one worktree and replies arrive asynchronously
---

# Mesh-Driven Development

Execute a plan by sending each task as a durable AFM request to a named team
writer, validating the returned work against evidence, and only then moving
on. This is subagent-driven development re-based onto the AFM mesh: requests
and replies replace dispatch and report, `afm agent wait` + yield replaces
blocking on a child, and a shared worktree replaces isolated contexts.

**Division of labor.** This skill defines the dispatch, recovery and
acceptance protocol. The CALLING FLOW owns ordering and gates — sections,
review rounds, markers, board duties. Apply the loop below to the flow's
current unit of work; writers finishing never by itself completes a unit or
the stage.

**You are the orchestrator.** You assign, verify and integrate; writers
write. Never implement a task yourself while its assigned writer is active,
and never use native subagents for work the mesh team owns. Stage-started AFM
helpers are available for bounded review and analysis (see below).

## Key differences from native subagents

| Native subagents | AFM mesh |
|---|---|
| Dispatch prompt, blocking result | `afm agent send --kind request`, reply arrives later |
| Isolated context per agent | ONE shared worktree — file ownership is the isolation |
| Controller session memory | Your turn can END (yield); the ledger plus afm's durable message records are the memory |
| Resume agent in-session | A follow-up request to the same writer continues its conversation — but must still name the task, the prior request id and the remaining work explicitly |

## The Task Loop

0. **Reconcile before acting.** At the start of EVERY turn — before any send
   or edit — load the ledger (team-progress.json or the flow's equivalent),
   <your_requests> and `afm agent inbox`, and reconcile them. afm's own
   records are the source of truth: a request present in <your_requests> but
   missing from the ledger WAS sent — record it, never resend it.
   Re-dispatching completed or pending work is the single most expensive
   failure. Persist in the ledger: the current unit/phase, each task's
   assignee, request/reply IDs with their purpose, task ACCEPTANCE separately
   from "reply received", and retry counts. Write via temp file + atomic
   rename. A missing or stale ledger is rebuilt from <your_requests>/inbox
   before anything is dispatched. A registered batch and the replies already
   collected for it survive a restart; check `afm agent wait-status` before
   deciding whether a request still needs an answer.
1. **Contracts come from the plan.** If parallel tasks share a contract, pin
   down the one the plan or the code already defines; delegate only missing
   contract work the plan requires, and accept it before dependent tasks
   start. Do not create abstractions solely to enable parallelism.
2. **One request per plan task.** Each request is self-contained — a writer
   never reads your session history. It names: the task id, allowed files
   (explicit ownership), dependencies and interfaces from earlier tasks,
   acceptance criteria, required checks, the TDD requirement for behavior
   changes (failing test first, red→green evidence in the reply), and any
   identifiers the calling flow requires (board key etc.). Ownership covers
   the tests, fixtures, manifests and generated files the task's commands may
   touch; a writer needing a file outside its grant must stop and ask —
   resolve the overlap and record the expanded grant before it proceeds.
   Record the baseline (`git rev-parse HEAD` plus the dirty state) before
   dispatching — acceptance is read against it. Corrections are linked
   follow-up requests to the same writer, not replays of the original.
   Include the diagnosis and escalation checkpoints below in writer requests:
   writers may not have loaded this skill. An ownership handoff records a new
   assignee instead of sending another correction to the original writer.
3. **Record, then wait.** Record every returned message_id immediately (step 0
   fields). For independent requests whose outcomes belong in one round, register
   ONE `afm agent wait --condition reply_to_all --message-ids <id1,id2,...>`
   (optionally with `--deadline-seconds N`; at most 16 IDs). Inspect the returned
   status: `yield` means a pending wait is registered — save the `batch_id` and
   END YOUR TURN. `ready` means all items have ended: read their outcomes and
   continue working; do not end your turn or re-register the satisfied wait.
   A separate incoming question can wake you before the batch ends: answer it, check the batch,
   then park again if it is still pending. Never poll or sleep in a shell, and
   never write a final summary while requests are pending.
4. **Classify every outcome by its request's purpose.** On continuation, read
   `afm agent wait-status --wait-id <batch_id>` for each item's status and
   reason, and `afm agent inbox --all` for its answer. An implementation reply
   must carry the completed scope and verification evidence; a review reply
   must cover the requested scope — an explicit "no findings" is a valid,
   complete review. An acknowledgement or partial status is NEITHER: do not
   re-wait the satisfied message id and do not redispatch the task — send
   one NEW linked request to the same agent asking for the final result, using
   `afm agent send --kind request` with the original request ID in its body.
   Wait on the new request ID if needed. An ACK or ordinary reply linked to a
   request satisfies `reply_to`; check the required result contract or Board
   task state to establish task completion. A cancelled, expired or `run_closed`
   wait, or a rejected wait registration, does not mean the task is complete:
   inspect the reason. A cancelled or expired item is an outcome without an
   answer, even when the batch status is `ready`: retain the other results and
   its reason. It proves nothing about the tree: confirm the writer stopped editing and inspect the
   partial changes in its files before retrying (at most one retry per request
   chain, counted in the ledger) or reporting the concrete blocker.
5. **Accept on evidence, not on words.** The writer's changes are ALREADY in
   the shared worktree — never apply a returned diff on top of them.
   Acceptance = inspect the staged, unstaged and new-file changes against
   the recorded baseline, verify the acceptance criteria and the ownership
   boundaries. Missing red→green evidence: ask for the existing record
   first; if the test-first step simply did not happen, record the unmet
   requirement — never invent evidence or rewind the shared tree to
   manufacture a red state.
6. **Integrate.** Reuse successful writer checks for the same relevant code
   and environment; `HEAD` alone does not identify a dirty shared tree. Run
   missing checks and checks needed for combined changes. Repeat broad suites
   after relevant edits, failures or a concrete new concern, not on every
   wake-up. Preserve every final gate required by the calling flow. Record
   the command, actual exit code, log location and tested tree state; piping
   through `tail` must not hide the check's failure.
   Attribute a failure BEFORE routing it: it may belong to this task, another
   writer's unfinished work, or the environment — do not send writer A to fix
   writer B's red phase or expand ownership just to make an aggregate check
   green. Small integration fixes are yours; a writer-sized defect goes back
   to its owner, subject to the escalation budget below.

## Diagnosis and escalation

These are agent workflow rules, not an automatic AFM watchdog. Use the calling
flow's explicit repair budget when it supplies one; otherwise apply these
checkpoints. Verify failure ceilings and transport retry limits do not override
the diagnosis checkpoints:

- **Stop repeating unsuccessful repairs.** After two unsuccessful fix-and-check
  attempts for the same symptom, or a second verify rejection of the same
  underlying defect, obtain an independent diagnosis before another correction.
  Count attempts across requests, sessions, resumes and verify rounds in the
  ledger. Renaming the failing test or narrowing the same race does not reset
  the count. Expected TDD red, planned stability repetitions and infrastructure
  failures are not failed repair attempts.
- **Test a hypothesis.** For each repair, record the symptom, suspected cause,
  a check that distinguishes it from alternatives, and the observed result.
  Keep this concise and link logs instead of copying them. Another run of the
  same check without a new hypothesis is not progress.
- **Investigate flakes.** A passing rerun does not establish an environmental
  or pre-existing failure. Require evidence that separates code/test lifecycle
  errors from resource contention or infrastructure. Do not increase timeouts,
  weaken assertions or exclude a test merely to get green. Filtered diagnostic
  runs never replace a required unfiltered gate.
- **Reason about the whole race.** Before another concurrency fix, state the
  ownership, commit and completion invariants. A published status may precede
  deferred cleanup. Reproduce the relevant interleavings with controlled gates
  or barriers rather than sleeps or repeated runs alone; fix the invariant
  instead of adding a special case for each review finding.
- **Get a fresh specialist.** Send a bounded diagnosis request to an independent
  named architect or reviewer, preferably using a more capable configured model
  than the stalled executor. Include the task, invariant, owned files/current
  diff, failing command/logs and attempted hypotheses. A temporary review helper
  below is another option when available. Resolve concrete findings with a fix
  or counterevidence; "already solved" or a green suite alone does not answer a
  reported interleaving.
- **Transfer the fix when needed.** If diagnosis calls for an architectural
  correction, or the same defect survives a correction after consultation,
  assign a bounded fix to a capable named writer/architect with write access.
  First confirm the previous owner and its file-modifying commands have stopped;
  record the new owner, file grant (including tests), prior request IDs and
  acceptance checks in the ledger/board. The previous owner and coordinator
  leave those files untouched until the handoff finishes. A read-only advisory
  helper cannot substitute for the new implementation owner. An explicit user
  instruction to hand off takes effect immediately, without another self-fix.
- **Bound the rescue.** After the specialist's bounded correction, evaluate its
  evidence and the required gates. If the defect remains unresolved, report the
  remaining failure, hypotheses and attempted fixes through the flow's dialog
  protocol instead of starting another repair loop. A successful handoff does
  not waive independent review or final checks.

## Unavailable participants and tools

Authentication errors, missing commands and unavailable participants need an
infrastructure response, not another code correction. Record the actual error,
check the roster/capabilities and select a reachable replacement with the needed
access and independence. Reconcile pending work and confirm its writer stopped
before reassigning files. Do not keep sending to a failed participant or replace
required independent review with the coordinator's self-review. If no suitable
replacement is available, report that concrete blocker through the flow's dialog
protocol. Use temporary helper commands below only when the running AFM binary
supports them; otherwise use the existing named team.

## Parallel dispatch

Send independent requests in the same turn BEFORE registering waits, so
writers work concurrently. Parallel is allowed ONLY for tasks with disjoint
ownership — including the shared files from step 2 — otherwise sequence
them. While implementation runs, the orchestrator never edits an active
writer's files. While a REVIEW round is outstanding, freeze the worktree
entirely — no orchestrator edits, no writer tasks, no file-rewriting
commands — until every reviewer in the batch has an outcome. The batch wait
keeps the stage parked until then, unless a separate message wakes it. Do not
invent work for an irrelevant role just to parallelize.

## Temporary review helpers

For a bounded review or analysis job, a stage agent may use
`afm agent spawn --profile <profile> --title <title> --instruction <text>`.
The returned `task_id` identifies the helper and `message_id` identifies the
request it answers; record both in the ledger before waiting. Several helpers
can use the same profile with different instructions. Collect their message
IDs in one `reply_to_all` batch, then read each full committed result with
`afm agent helper-result --task-id <task_id>`; the inbox reply contains only a
summary. Check `<your_helpers>` on resume so an already started helper is not
spawned again.

The selected profile's configuration supplies the helper's command and model. The
helper is advisory and one-shot, under the existing quota and depth limits.
Use named mesh writers for implementation work with file ownership. A helper's
read-only prompt is not OS isolation: on the host the Codex adapter runs in a
read-only sandbox; in a container a helper can write within that container's
boundary. Keep these helpers scoped to review and analysis.

## Common rationalizations

| Excuse | Reality |
|---|---|
| "I'll just code it myself, messaging is overhead" | Orchestrator edits race the writer in the same tree and skip its evidence trail. |
| "The writer acked, task is done" | An acknowledgement is not a deliverable. Accept only a classified, complete reply. |
| "I'll re-send to be safe after the resume" | <your_requests>/inbox are the durable truth. Reconcile; a sent request is never resent. |
| "`send` failed, so nothing was sent" | Delivery may be uncertain. Reconcile the durable records before retrying. |
| "The request expired, so its files are free" | Request status alone does not prove the writer stopped editing. |
| "One reviewer replied; I can fix that finding now" | The batch remains pending until every reviewer has an outcome; finish the round before touching the reviewed tree. |
| "The tests are green now; that proves TDD" | A current green result does not establish a prior test-first red. |
| "Both tasks touch that file only a little" | Shared file = shared state. Sequence them. |
| "I'll poll in a loop until the reply lands" | Polling burns the turn. Register waits and yield; afm resumes you. |
| "No reply yet, so I'll write the summary now" | Yielding is not completing. A final summary with pending requests fakes completion. |
