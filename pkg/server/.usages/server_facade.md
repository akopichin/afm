Domain: starting the HTTP dashboard and wiring it to the orchestrator. Audience: `cmd/afm`.

## Starting the server

```go
srv := server.Server{}.New(server.Config{
    Port: cfg.Server.GetPort(), RunDir: runDir, Store: store, UIBus: o.UIBus(),
    ApproveFn:      o.Approve,
    ReviseFn:       o.Revise,
    RetryFn:        o.Retry,
    DialogAnswerFn: o.NotifyAnswer,
    DialogCancelFn: cancelFn,
})
addr, err := srv.Start()
if err != nil {
    return err
}
if cfg.Server.IsOpenBrowser() {
    openBrowser(addr)
}
defer srv.Shutdown(ctx)
```

The callback fields (`ApproveFn`/`ReviseFn`/`RetryFn`/`DialogAnswerFn`/`DialogCancelFn`) are the only
coupling point to `pkg/orchestrator` — pass the corresponding `Orchestrator` methods directly, the
server does not hold an `Orchestrator` reference itself, only `UIBus` and these callbacks.
