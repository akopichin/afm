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
and never use native/CLI subagents for work the mesh team owns.

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
   before anything is dispatched.
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
3. **Record, then wait.** Record the returned message_id immediately (step 0
   fields). Register a reply wait for every unanswered request
   (`afm agent wait --condition reply_to`). `ready` → read the answer now;
   `yield` → finish registering the remaining waits, save progress, and END
   YOUR TURN. Never poll or sleep in a shell, and never write a final
   summary while replies are pending — yielding is not completing.
4. **Classify the reply by its request's purpose.** An implementation reply
   must carry the completed scope and verification evidence; a review reply
   must cover the requested scope — an explicit "no findings" is a valid,
   complete review. An acknowledgement or partial status is NEITHER: do not
   re-wait the satisfied message id and do not redispatch the task — send
   one linked follow-up to the same agent asking for the final result. A
   cancelled/expired/failed request proves nothing about the tree: confirm
   the writer stopped editing and inspect the partial changes in its files
   before retrying (at most one retry per request chain, counted in the
   ledger) or reporting the concrete blocker.
5. **Accept on evidence, not on words.** The writer's changes are ALREADY in
   the shared worktree — never apply a returned diff on top of them.
   Acceptance = inspect the staged, unstaged and new-file changes against
   the recorded baseline, verify the acceptance criteria and the ownership
   boundaries. Missing red→green evidence: ask for the existing record
   first; if the test-first step simply did not happen, record the unmet
   requirement — never invent evidence or rewind the shared tree to
   manufacture a red state.
6. **Integrate.** When the current unit's writers have finished, run the
   required checks yourself. Attribute a failure BEFORE routing it: it may
   belong to this task, to another writer's unfinished work, or to the
   environment — do not send writer A to fix writer B's red phase, and do
   not expand anyone's ownership just to make an aggregate check green.
   Small integration fixes are yours; a writer-sized defect goes back to its
   owning writer as a linked follow-up.

## Parallel dispatch

Send independent requests in the same turn BEFORE registering waits, so
writers work concurrently. Parallel is allowed ONLY for tasks with disjoint
ownership — including the shared files from step 2 — otherwise sequence
them. While implementation runs, the orchestrator never edits an active
writer's files. While a REVIEW round is outstanding, freeze the worktree
entirely — no orchestrator edits, no writer tasks, no file-rewriting
commands — until every reviewer in the round has replied. Do not invent work
for an irrelevant role just to parallelize.

## Common rationalizations

| Excuse | Reality |
|---|---|
| "I'll just code it myself, messaging is overhead" | Orchestrator edits race the writer in the same tree and skip its evidence trail. |
| "The writer acked, task is done" | An acknowledgement is not a deliverable. Accept only a classified, complete reply. |
| "I'll re-send to be safe after the resume" | <your_requests>/inbox are the durable truth. Reconcile; a sent request is never resent. |
| "`send` failed, so nothing was sent" | Delivery may be uncertain. Reconcile the durable records before retrying. |
| "The request expired, so its files are free" | Request status alone does not prove the writer stopped editing. |
| "One reviewer replied; I can fix that finding now" | Finish the review round before touching the reviewed tree. |
| "The tests are green now; that proves TDD" | A current green result does not establish a prior test-first red. |
| "Both tasks touch that file only a little" | Shared file = shared state. Sequence them. |
| "I'll poll in a loop until the reply lands" | Polling burns the turn. Register waits and yield; afm resumes you. |
| "No reply yet, so I'll write the summary now" | Yielding is not completing. A final summary with pending requests fakes completion. |
