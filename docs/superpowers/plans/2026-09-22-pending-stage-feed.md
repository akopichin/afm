# Pending stages stay on Feed until they have something to show

## Goal

When a user clicks a stage that has not started yet, the dashboard must keep the
workspace on `Feed` instead of opening an empty stage-detail tab with `No plan yet`.
Attention stages (`awaiting_approval`, `awaiting_user_input`, `failed`,
and `paused`) must keep their existing action flow. Script stages must not show
an empty plan while running or finished, but failed/paused script stages still
need PlanPanel for Retry/Continue.
Completed stages also remain on `Feed` when clicked; their read-only history is
not opened automatically. In a dialog with an active question, history is
collapsed by default; without an active question, the full history is shown.

## Observed behavior

In a flow with completed stages, one active stage, and later stages still
`pending`, clicking a not-yet-started autonomous stage opens a detail tab and
renders an empty Plan panel. A separate `Question` beacon for a stage waiting
for user input is correct and must remain available.

## Root cause

`pkg/server/stageview.go` currently computes:

```go
showPlan := !autonomous || st.Status == state.StatusFailed || st.Status == state.StatusPaused
```

For a pending stage, this reports `show_plan: true` whenever the runtime
`autonomous.flag` is not present. The flag is only created when an autonomous
stage is activated, so a not-yet-started autonomous stage can be misclassified
as a regular stage. `App.tsx` then handles the rail click through
`openHistory('plan-history')`, and `PlanPanel` displays `No plan yet`.

The `Question` beacon is a separate mechanism: a stage in
`awaiting_user_input` is deliberately added to the Attention queue and may
auto-open Attention when the status first arrives. This fix must not change
that behavior.

## Intended behavior matrix

| Stage status | User clicks stage in rail | Expected workspace |
|---|---|---|
| `pending`, no started work | no stage detail | Feed |
| `pending`, with a pre-existing visible artifact (if supported) | show artifact only if explicitly supported | no empty Plan tab |
| `running`/`planning`/`revising` | existing behavior | stage plan/dialog when available |
| `done`, plan/dialog exists | rail click stays on Feed | history is not auto-opened |
| `awaiting_approval` | user action required | Attention/Plan |
| `awaiting_user_input` | user action required | Attention/Dialog |
| `failed` | retry/recovery action | Attention/Plan |
| `paused` | Continue/recovery action | preserve current paused flow |

## Implementation plan

### 1. Tighten the server-side `ShowPlan` capability

Modify `buildStageViews` in `pkg/server/stageview.go` so a plain `pending`
stage does not advertise a plan panel. Preserve the explicit exceptions needed
for actions that live in `PlanPanel` (`failed` and `paused`). Keep
`ShowDialog` unchanged: it remains true only for dialog history or a live
`awaiting_user_input` question.

The implemented rule is a status/type capability calculation, rather than
making the frontend infer filesystem state:

```go
showPlan := (st.Status != state.StatusPending && !isScript && !autonomous) ||
    st.Status == state.StatusFailed || st.Status == state.StatusPaused
```

The deliberate contract is that `pending` never opens a plan, including the
short crash window where a pre-planned `plan.md` may already exist but the
durable status transition to `ready` has not landed. Recovery promotes that
state on restart. `paused` remains an Attention status because Continue is an
action, not read-only history.

### 2. Keep the Attention model unchanged

Do not remove `awaiting_approval`, `awaiting_user_input`, `failed`, or
`paused` from `attentionKindForStatus` as part of this fix. The requested bug is
the empty detail tab for an unstarted stage; the Question/Approval beacon is a
separate and useful action affordance.

Confirm that a new `awaiting_user_input` stage still auto-opens Attention when
the user is on Feed, and that returning to Feed leaves the Question beacon
visible.

### 3. Add server regression coverage

Extend `pkg/server/stageview_test.go`:

- pending ordinary stage → `ShowPlan == false`;
- pending autonomous stage with no `autonomous.flag` → `ShowPlan == false`;
- pending script stage → `ShowPlan == false`;
- running/done script stage → `ShowPlan == false`;
- autonomous failed stage → `ShowPlan == true`;
- autonomous paused stage → `ShowPlan == true` (Continue regression);
- script failed/paused stage → `ShowPlan == true` for Retry/Continue;
- running/planning stage behavior remains unchanged.

Update existing expectations that currently assert `ShowPlan == true` for a
pending stage solely because it is non-autonomous. Update the dashboard's
`stageView` test fixture helper too; it must mirror the backend wire contract
instead of defaulting pending stages to `show_plan: true`.

### 4. Add dashboard integration coverage

Extend `pkg/web/dashboard/src/app/App.test.tsx` with one focused fake-flow test:

- stages: two `done` stages, one `running` stage, one later `pending` stage,
  and one `awaiting_user_input` stage;
- click the completed stage with a plan → Plan/history is shown;
- click the completed stage with a dialog → Dialog/history is shown;
- click the pending stage → workspace remains Feed and no empty detail tab is
  created;
- verify the waiting stage still produces the Question beacon and that clicking
  the beacon opens live Attention/Dialog.

This test must click the actual `.stage-row` buttons, not invoke
`handleSelectStage` directly.

Also verify that clicking a completed stage with plan/dialog history keeps Feed
selected and does not create a detail tab.

### 5. Collapse dialog history around an active question

In `DialogChannel`, when `/dialog` contains a pending question, collapse the
answered history by default and leave the pending question visible. Keep the
existing explicit expand/collapse control. When the pending question disappears,
automatically show the full history again. A new pending question resets the
default to collapsed.

Add component tests for both states: answered history plus pending question is
collapsed initially and expandable; history without a pending question is
expanded.

### 6. Verify the real dashboard path

Run:

```bash
go test ./pkg/server/...
cd pkg/web/dashboard
npm test -- --run src/app/App.test.tsx
npm run typecheck
```

Then start a disposable fake flow with two completed stages, one active stage,
one pending stage, and one waiting-for-input stage. Verify manually:

1. clicking a completed stage keeps Feed selected;
2. clicking the pending stage leaves Feed selected and shows no `No plan yet`
   panel;
3. the waiting stage still exposes the Question beacon and its dialog;
4. resolving the question returns to the previous global view as before.

## Non-goals

- Do not change the Attention queue or its auto-open semantics.
- Do not remove PlanPanel for `failed` or `paused`; Retry and Continue depend on
  it even when no `plan.md` exists.
- Do not change stage selection, topological ordering, or Feed event filtering.
