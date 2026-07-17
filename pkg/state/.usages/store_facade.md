Domain: reading and mutating persistent run state. Audience: `pkg/orchestrator`, `pkg/server`,
`cmd/afm`.

## Opening a store

```go
store, err := state.Open(runDir, stageIDs)
if err != nil {
    return err
}
defer store.Close()
```

## Reading current state (read-only consumers: pkg/server, cmd/afm)

```go
status := store.Get(stageID)      // current StageStatus
snap := store.Snapshot()          // full RunState
if snap.AllDone() { ... }
```

## Reading transition history (read-only)

`History()` returns the full transition log for audit/inspection — read-only access via `History()`
and `Snapshot()`, never `Apply`:

## Applying a transition (write path — restricted to pkg/orchestrator/fsm.go)

Inside a running `pkg/orchestrator` process, `Store.Apply` must only be called from
`pkg/orchestrator/fsm.go` — `tools/setstatuslinter` enforces this at build time via static analysis
(scoped to `./pkg/...`). `cmd/afm`'s `approve`/`retry`/`revise` commands are the one deliberate
exception: they call `Store.Apply` directly, because these CLI commands mutate `state.json` offline,
without a live `Orchestrator` process to route the transition through the FSM.

```go
// pkg/orchestrator/fsm.go (via FSM.Apply)
t := state.Transition{StageID: stageID, From: from, To: to, Event: event, Reason: reason}
err := store.Apply(t)

// cmd/afm/approve.go — direct call, no live orchestrator process to go through
err := store.Apply(state.Transition{StageID: stageID, From: state.StatusAwaitingApproval, To: state.StatusReady, Event: "cli_approve"})
```

## Setting display names

```go
store.SetStageNames(map[string]string{"propose": "Propose", "apply": "Apply Architecture"})
// the map is copied — later mutation by the caller does not affect the store
```

## Finding an existing run (cmd/afm)

```go
base := filepath.Join(fmDir(), "runs")
existing, err := state.FindLatestRunDir(base, flowName)
if err == nil {
    store, err = state.Open(existing, stageIDs)
    // ... check store.Snapshot().AllDone() to decide resume vs. start fresh
}
```

## Revising a plan (cmd/afm `revise`, pkg/orchestrator revision path)

`VersionPlan`/`SaveFeedback` operate on files inside a stage directory, not on the `Store` itself —
call them alongside `Store.Apply` when moving a stage back to `revising`:

```go
stageDir := filepath.Join(runDir, stageID)
if _, err := state.VersionPlan(stageDir); err != nil { // plan.md -> plan.vN.md
    return err
}
if err := state.SaveFeedback(stageDir, feedback); err != nil { // appends to feedback.md
    return err
}
err := store.Apply(state.Transition{
    StageID: stageID, From: state.StatusAwaitingApproval, To: state.StatusRevising,
    Event: "cli_revise", Reason: feedback,
})
```
