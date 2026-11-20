---
name: afm-dev-escalation
description: Single source of the dev-spec repeated-failure route (Astra diagnosis, Opus implementation, Fabula verification and one residual repair). Use when counting rejected rounds, assigning or acting as rescue-architect/opus/fabula, or auditing that route in verify.
---

# Repeated defects and mandatory ownership handoff

This skill is the only definition of the escalation route. Flows, stage prompts
and other skills refer to it instead of restating thresholds.

## Quick reference

| Event for one logical task | Required action |
| --- | --- |
| Initial work, first correction | Ordinary backend/frontend writer |
| 2 rejected candidate rounds or 2 failed repairs | Astra (`rescue-architect`) diagnoses, then Opus (`opus`) implements; never a third ordinary correction |
| 1st rejected post-escalation candidate | User decision before another bounded Astra/Opus attempt; Fabula stays idle |
| `escalation.post_escalation_rejected_rounds >= 2` | Fabula (`fabula`) verifies findings/design read-only |
| After accepted Fabula verification | At most ONE bounded Fabula residual repair (`escalation.fabula_repair_used`), then fresh independent acceptance |
| Anything further, unclear cause, redesign | Concrete blocker through the flow's dialog; no automatic restart |

## Count the work, not the reviewers

Initial implementation and the first correction belong to backend/frontend
writers. After **two rejected candidate rounds for the same logical work item**,
escalate before a third correction/review round. Two unsuccessful repairs also
trigger escalation even if they failed checks before review. A lower explicit
flow ceiling still applies; a higher budget never postpones this checkpoint.

A round is the complete selected review batch for one candidate: two reviewers
rejecting it count once. Include blocking AI verify and live acceptance outcomes
in the same history; the same candidate rejected by multiple gates is not several
repair attempts. Count only completed outcomes requiring an in-scope correction,
not individual findings, ACKs, optional polish, expected TDD red, diagnostic
experiments, infrastructure failures or cancelled/inconclusive reviews.

Group changing symptoms by logical task and underlying invariant. A new finding
ID, narrower reproduction, helper, writer, stage, session or restart does not
reset the history. Import earlier implement/live/verify evidence before dispatch.
Unrelated work items have separate histories; a stage's total review count is
not the trigger for every task in it.
If the grouping is uncertain, ask the diagnosis agent to resolve it; do not start
another ordinary repair while deciding. A user-requested handoff can happen earlier.

Keep in the task's team-progress.json entry: logical task/invariant, `review_rounds`
(each round's candidate identity, result/request IDs, verdict and dispositions),
`rejected_rounds`, `failed_repairs`, and an `escalation` record. Reconcile counts
against durable AFM replies and reports, rather than trusting a stale number.

## Required route: Astra diagnosis, Opus implementation

The standing diagnosis agent is team ID `rescue-architect` (default GPT-6 Astra
`high`); the independent findings verifier is team ID `fabula` (Фабула, default
Claude Fable 5.1 `medium`); the reserve writer is team ID `opus` (default Opus 5.5
`xhigh`). Resolve participant IDs and actual model/command bindings from the run's roster and frozen
configuration. The historical IDs are not model identities. Use the configured
bindings; report a missing role or incompatible access rather than silently
substituting another writer. All three roles remain idle during ordinary work.

At the threshold the route is mandatory, not an optional consultation followed
by another fix from the original writer:

1. Finish the selected review batch and preserve every full result. Confirm the
   old writer AND its modifying commands have stopped; freeze the candidate.
2. Record `escalation.trigger`, prior task/request IDs and counts, candidate and
   evidence paths, previous owner's release, diagnosis assignee, canonical request
   ID and finite budget. Send ONE bounded diagnosis request to `rescue-architect`,
   using the afm-dev-diagnosis skill. This is a standing run agent: use `send`/`reply`
   and save its full linked deliverable, not `helper-result` or a new helper.
3. Give the diagnosis agent the original requirement, all repair/review history,
   current diff, failing commands/logs, attempted hypotheses and scratch grant.
   Require root cause, a distinguishing reproduction, the smallest corrective
   design, evidence testing that design and required regression scenarios.
   Separate product defects, missing regression guards and faulty test harnesses.
   A green rerun alone does not refute a failure. The diagnosis may use scratch
   prototypes; it does not edit the product or weaken approved acceptance.
4. Read and accept the diagnosis, including counterevidence and limitations.
   Store its request/reply IDs, artifact and acceptance in the same escalation
   record. An ACK or untested advice is insufficient. Keep Fabula idle on the
   first escalation and the first rejected post-escalation candidate; its trigger
   is the repeated post-escalation rejection defined below.
5. Transfer the bounded implementation to `opus`, NEVER back to the original
   writer after this threshold. Record the new owner, file/test grant, diagnosis,
   canonical request ID and acceptance/check budget before waiting. The writer
   requires this record before editing. Other writers and the coordinator leave
   the granted files untouched. Do not use a temporary read-only helper as writer.
6. Obtain fresh independent review of the corrected candidate and regression
   evidence, then the required checks and affected live acceptance. Astra's
   diagnosis, Fabula's verification, the writer's reply and green unit tests are
   not final acceptance.
   Preserve the logical task and counters throughout this rescue.

An explicit user direction can override the route or assign implementation to
Astra: record the original instruction, changed owner/access and grant. Anyone
who edited product code cannot independently approve that candidate. Such an
exception does not silently become the normal pipeline route.

## Bound the rescue

One escalation permits one Astra diagnosis and one Opus implementation with
independent acceptance. Track `escalation.post_escalation_rejected_rounds` for
this logical task: increment it once per distinct completed candidate batch
requiring an in-scope correction AFTER an escalation implementation. Multiple
reviewers, findings or gates rejecting the same candidate count once. Ordinary
pre-escalation rejections, infrastructure failures, cancelled/inconclusive reviews
and diagnostic experiments do not count. Preserve candidate identities and this
counter across stages, verify, live acceptance, sessions and restarts.

On the FIRST rejected post-escalation candidate, report the concrete blocker for
a user decision before another bounded Astra/Opus attempt. Do not activate Fabula
or automatically restart the rescue. A user-authorized retry records the original
input, scope and finite budget on the same work item without resetting counters.

Activate `fabula` ONLY when `escalation.post_escalation_rejected_rounds >= 2`:
at least TWO distinct post-escalation candidate rounds for the same logical task
have required corrections. Send one bounded read-only verification request with
Astra's full linked diagnosis, repeated findings, prior attempts, frozen candidate
and evidence. Verify its binding against
the pipeline's `escalation_verifier` role. Require the exact review/check
skill paths, distinguishing reproductions and a per-finding disposition. Fabula
checks the findings, counterevidence, corrective design and adjacent producers;
it separates product bugs, missing guards and harness faults. Reconcile both
agents' full linked replies against evidence. Save request/reply IDs, candidate,
artifacts, dispositions and acceptance in the same escalation record. An ACK is
insufficient; an unresolved disagreement or inconclusive bounded verification is
a concrete blocker, not permission to loop or bypass diagnosis.

After this repeat threshold and accepted verification, there is one rare residual
repair exception: the coordinator may hand concrete remaining in-scope bugs to
Fabula when the cause is understood and the fix needs no material redesign.
Record why this exception applies, all rejected evidence, unchanged counters,
the prior writer's release and stopped modifying commands, an exclusive file/test
grant, one canonical writer request and finite check/acceptance budgets. Set
`escalation.fabula_repair_used` before dispatch and recover it on resume; the limit
is once per logical task across stages, verify, live acceptance and restarts.
For this separate assignment Fabula reads the writer/check skills, replacing its
earlier read-only review assignment. Its earlier verification never approves its
own changes. Require fresh independent review by non-authors, regression evidence,
required checks and affected live acceptance after the repair.

If the cause is unclear, scope/design must change, Fabula is unavailable, or this
single repair fails acceptance, report the concrete blocker through the flow's
dialog protocol. Do not restart Astra -> Opus or Fabula, return to an ordinary
writer or reset counters automatically. Unchanged failure alone is not permission
for another attempt.
Infrastructure failure alone does not activate the residual repair.
Task size, subsystem boundaries and spare capacity do not activate these roles.
Routine escalation within the approved scope needs no additional user approval.

## Unavailable participants or capabilities

Authentication errors, missing commands and unreachable participants call for an
infrastructure response. Record the actual error and inspect roster/capabilities.
Follow the coordinator's [five-minute health checks](../afm-dev-coordinator/references/health.md): long diagnosis/implementation
budgets use wait windows of at most 300 seconds, and confirmed terminal failures
trigger recovery before the full task deadline. Silence alone is not proof of death.
Select a reachable replacement with the needed access and independence only after
reconciling pending work and confirming the prior writer stopped. Do not keep
sending to a failed participant or replace required independent review with
self-review. With no suitable replacement, report the missing capability and
remaining work. Own only processes you started; no kills by name or pattern,
shared-environment changes, token borrowing or parent-run state mutations.

## Your role in this route

- **Coordinator:** owns counting, records and dispatch as described above. Keep
  `rescue-architect`, `opus` and `fabula` unassigned in plans and initial work;
  complexity or subsystem boundaries alone never justify them.
- **Astra (`rescue-architect`):** stay idle until a bounded diagnosis request
  arrives. Follow afm-dev-diagnosis; product files are read-only; no managed
  browser or delegation. Return a full linked diagnosis, not an ACK or a patch.
- **Opus (`opus`):** before editing, confirm the recorded escalation: trigger and
  counters (or explicit user handoff), accepted diagnosis, previous owner's
  release, file/test grant and check/acceptance budget. If anything is missing,
  ask the coordinator and do not edit. Never take initial work or restart your loop.
- **Fabula (`fabula`):** stay idle until the threshold above. Verification is
  read-only under afm-review-work and returns one linked pass/needs_changes/
  inconclusive result. The single residual repair is a separate writer request
  under the Desktop implementation contract; your verification never approves your own patch.
- All roles: no commits, pushes, delegation or managed browser; preserve the
  logical task's counters and evidence in every reply.

## Audit checklist for verify and review

Reconcile the complete candidate-round history of each logical task across
stages, verify and live runs, counting each rejected batch once. Reject:

- initial or first-repair ownership assigned to `opus`, `rescue-architect` or `fabula`;
- a third ordinary-writer correction after the threshold, or Opus edits without a
  recorded escalation, accepted diagnosis, ownership release and bounded grant;
- diagnoses without full canonical linked replies (standing agents do not have
  helper-result IDs) or bindings that differ from the configured escalation roles;
- Fabula activity before `post_escalation_rejected_rounds >= 2`, more than one
  Fabula repair, or a Fabula repair without fresh non-author acceptance;
- an automatically restarted rescue loop, unless the original user instruction
  explicitly authorized that exception.
