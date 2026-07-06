Domain: writing human-readable stage logs (start/end banners, timestamped messages, tool actions).
Audience: `pkg/executor`.

## Constructing and logging

```go
lg, err := progress.NewLogger(logFile)
if err != nil {
    return err
}
defer lg.Close()

lg.LogStart("planning", stageName)
lg.LogAction(toolName, detail)
lg.Log(msg)
lg.LogEnd(err) // err == nil → success banner
```

`NewLogger` opens the file in append mode — a restart of the same `logFile` gets a separator line
rather than truncating prior history. `Log`/`LogAction`/`LogStart`/`LogEnd` write to both the file and
stdout; callers only need to check the error from `NewLogger`/`Close`.
