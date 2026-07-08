Domain: consuming the per-run usage log written by the proxy's response inspection. Audience: `pkg/accounting`.

## File location and format

`.afm/runs/<run>/usage.jsonl` — one JSON object per line, written by `AppendUsageRecord` for every
proxied response that yielded a parseable `usage` field:

```json
{"timestamp":"2026-07-07T10:00:00Z","model":"claude-sonnet-5","inputTokens":1200,"outputTokens":340,"cacheCreationTokens":0,"cacheReadTokens":800,"requestBytes":2048,"responseBytes":6144}
```

## Reading the log

```go
records, err := accounting.LoadUsageRecords(filepath.Join(runDir, "usage.jsonl"))
// missing file (proxy was never started this run) -> empty slice, nil error — not an error case
```

Records are written in request-completion order — no assumption of strict timestamp ordering across
concurrent requests (parallel subagents), sort by `Timestamp` if a strict order is required.

## Absence signals "no active proxy this run"

An empty or missing `usage.jsonl` means real-time per-request capture wasn't available for the run —
consumers should fall back to the jsonl `result`-event reader for tokens/cost, and simply have no KB
figure at all (KB is proxy-only, no fallback source exists).
