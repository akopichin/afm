Domain: driving a flow run end-to-end and observing its events. Audience: `pkg/server`, `cmd/afm`.

## Starting an orchestrator (cmd/afm)

```go
o := orchestrator.New(orchestrator.Options{
    RunDir: runDir, Stages: f.Stages, Store: store, Config: cfg,
    Prompts: prompts, DashboardURL: dashboardURL,
    GlobalPrompt: f.Prompt,
})
go func() { _ = o.Run(ctx) }()
o.SetDashboardURL(actualAddr) // once the server has actually bound its port
```

## Driving stage actions (cmd/afm subcommands)

```go
err := o.Approve(ctx, stageID)
err := o.Revise(ctx, stageID, feedbackText)
err := o.Retry(ctx, stageID)
o.FailStage(stageID, reason)
```

## Subscribing to events (pkg/server)

```go
bus := o.UIBus()
id, events := bus.Subscribe(bufSize)
defer bus.Unsubscribe(id)
for ev := range events {
    // ev.Type, ev.StageID, ev.Data — forward as JSON over the WebSocket
}
if bus.SubscriberDroppedCount(id) > 0 {
    // this subscriber's buffer overflowed; some events were dropped
}
```

## Delivering a dialog answer (pkg/server's dialog HTTP handler)

```go
err := o.NotifyAnswer(stageID, phase, questionID, answerText, fromOptions)
```

Call this only after the answer file has already been written atomically (see `pkg/mcp`'s
`dialog_protocol` usage) — `NotifyAnswer` assumes the on-disk answer already exists and only handles
the in-memory FSM/agent-restart side of the protocol.

Note for stage-status consumers (dashboard, retry tooling): an interactive stage's agent process can
exit cleanly before the user answers an open question. The orchestrator keeps such a stage in
`awaiting_user_input` rather than transitioning it to `failed` — do not treat "agent process no longer
running" as "this stage needs `Retry`" for interactive stages; wait for the user to answer and let
`NotifyAnswer` restart the agent instead.
