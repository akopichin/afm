Domain: file-based dialog protocol for the interactive agent↔user question/answer exchange. Audience:
consumers that drive an interactive stage's agent lifecycle (`pkg/orchestrator`) and the HTTP handler
that records answers and serves dialog history to the UI (`pkg/server`).

## Writing and polling for a question (agent side)

The agent writes a question file and polls for the answer file in the stage directory
(`AFM_STAGE_DIR`):

```go
q := mcp.Question{ID: "q1", Question: "Which approach?", Options: []string{"A", "B"}, AllowCustom: true}
_ = mcp.AppendQuestion(filepath.Join(stageDir, "planning.q1.question.json"), q)
// agent-side bash loop then polls for planning.q1.answer.json
```

## Detecting unanswered questions (orchestrator side)

```go
questions, err := mcp.FindUnansweredQuestions(stageDir)
for _, qf := range questions {
    // qf.Phase, qf.ID, qf.Question, qf.Options, qf.AllowCustom
    // publish an ask_user Event, transition the stage to awaiting_user_input
}
```

The poller re-scans `stageDir` on every tick, so the same unanswered question is returned repeatedly until
it is answered. Before appending it to `<phase>.dialog.jsonl` for UI history, check `FindEntry` so a
repeated poll does not write a duplicate history line for the same question:

```go
dialogPath := filepath.Join(stageDir, qf.Phase+".dialog.jsonl")
if e, _ := mcp.FindEntry(dialogPath, qf.ID); e == nil {
    _ = mcp.AppendQuestion(dialogPath, mcp.Question{
        ID: qf.ID, Question: qf.Question, Options: qf.Options, AllowCustom: qf.AllowCustom,
    })
}
```

## Recording the answer (HTTP handler side)

`AppendAnswer` does NOT create <phase>.<id>.answer.json and does NOT provide an atomic/exclusive-create
guarantee — it appends a JSON line (O_APPEND|O_CREATE) to whatever path it is given, same as
`AppendQuestion`. The atomic delivery guarantee for the protocol is implemented separately by the HTTP
handler itself (`pkg/server/handlers.go`, `handleDialogAnswer`), which creates <phase>.<id>.answer.json
directly via `os.OpenFile(answerPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)` — O_EXCL failure (already
exists) is what yields the 409 Conflict. `AppendAnswer` is called afterwards, separately, targeting
<phase>.dialog.jsonl (history for UI, best-effort, non-critical):

```go
// critical path — O_EXCL directly in pkg/server, not via mcp.AppendAnswer:
f, err := os.OpenFile(answerPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
// os.IsExist(err) => 409 Conflict to the caller

// best-effort UI history, appended after answer.json is safely on disk:
a := mcp.Answer{ID: "q1", Answer: "Option A", FromOptions: true}
_ = mcp.AppendAnswer(filepath.Join(stageDir, "planning.dialog.jsonl"), a)
```

## Reading dialog history for display

```go
entries, err := mcp.ReadDialog(filepath.Join(stageDir, "planning.dialog.jsonl"))
for _, e := range entries {
    // e.Question, e.Options, e.AllowCustom are always populated.
    // e.Answer is *string, not string — nil means the question is still open;
    // this is NOT represented by an empty string.
    if e.Answer != nil {
        answerText := *e.Answer // e.g. render in the UI
        _ = answerText
    }
}
```

## Constraints for consumers

- Question/answer filenames must follow `<phase>.<id>.question.json` / `<phase>.<id>.answer.json`;
  `FindUnansweredQuestions` only recognizes files matching this pattern.
- `AppendAnswer` itself gives no exclusivity guarantee. The sole correctness mechanism preventing a
  duplicate answer delivery is the caller's own O_EXCL create of <phase>.<id>.answer.json (done in
  pkg/server, not in pkg/mcp) — callers must not bypass that with a read-then-write of their own, and
  must not rely on `AppendAnswer` for exclusivity.
