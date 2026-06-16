# FlowManager Development Guide

## File-Based Dialog Protocol (Interactive Stages)

The interactive dialog system was refactored from an MCP HTTP server to a file-based protocol starting with the planning-depends-on-ref branch. This enables agents to ask users questions and receive answers through simple file I/O instead of HTTP.

### Architecture

**Agent writes question:**
- Agent writes: `$FLOWMANAGER_STAGE_DIR/<phase>.<id>.question.json`
- Example: `planning.q1.question.json`
- Format: `{"id":"q1", "question":"...", "options":[...], "allow_custom":true/false}`

**Agent polls for answer:**
- Bash loop in agent script: `while [ ! -f "$FLOWMANAGER_STAGE_DIR/<phase>.<id>.answer.json" ]; do sleep 30; done`
- When file appears, agent reads it and continues
- Format: `{"id":"q1", "answer":"...", "from_options":true/false}`

**Orchestrator polls for questions:**
- `startQuestionPoller()` launches goroutine that scans every 1 second
- Detects `*.question.json` files in stage directories
- Publishes `EventAskUser` to transition stage to `awaiting_user_input`
- UI dashboard polls `/api/stages/<id>/dialog` to fetch questions

**HTTP handler processes answer:**
1. Validates phase is one of: `planning`, `implementation`, `review`
2. Validates ID is safe filename component (no path traversal)
3. Checks question.json exists (else 404)
4. Rejects if answer.json already exists (409 Conflict)
5. **Atomically writes** answer.json (O_EXCL exclusive create) — critical path
6. Appends to dialog.jsonl for UI history (best-effort, non-critical)
7. Calls `NotifyAnswer()` to either transition FSM (if agent active) or restart agent (if exited)

### Key Files

| File | Responsibility |
|------|-----------------|
| `pkg/mcp/dialog.go` | `FindUnansweredQuestions()`, `QuestionFile` type, `appendLine()` for dialog.jsonl |
| `pkg/orchestrator/orchestrator.go` | `startQuestionPoller()`, `NotifyAnswer()`, `pollQuestions()`, active agent tracking |
| `pkg/executor/executor.go` | Passes `FLOWMANAGER_STAGE_DIR` environment variable to agent process |
| `pkg/server/handlers.go` | `handleDialogAnswer()` with atomic write pattern (O_EXCL) |
| `pkg/prompts/builder.go` | Interactive rules instruction in system prompt |

### Implementation Details

**Agent Activity Tracking**
- `Orchestrator.activeAgents` is a `sync.Map` tracking which stages have running agent goroutines
- Goroutine acquires semaphore → calls `markAgentActive(stageID)` → deferred `markAgentDone(stageID)`
- If user answers while agent is active: `NotifyAnswer()` transitions FSM, agent bash loop detects file
- If user answers after agent exited: `NotifyAnswer()` publishes to critical bus → `onUserAnswered()` restarts agent

**Question File Naming**
- Format: `<phase>.<id>.question.json`
- Phase must be: `planning`, `implementation`, or `review`
- ID must pass `isValidDialogID()` check (safe filename, no path traversal)
- Enforces: alphanumeric + underscore only

**Answer Delivery Guarantee**
- Answer.json is written atomically (O_EXCL exclusive create) BEFORE FSM transition
- Agent bash loop will always find the file if still running
- If agent already exited, restart with `--resume` flag to re-read answer

**Dialog History**
- `<phase>.dialog.jsonl` appended for UI (best-effort, NOT critical)
- Stores: `{"timestamp":"...", "phase":"...", "role":"assistant|user", "message":"..."}`
- If append fails, agent continues anyway (answer.json already safe on disk)

### Deleted Components

- `pkg/mcp/server.go` — MCP HTTP server (replaced by polling)
- `pkg/mcp/server_test.go` — MCP server tests
- `pkg/orchestrator/mcp_notifier.go` — MCP event notifier

### Environment Variables

| Variable | Purpose | Set By |
|----------|---------|--------|
| `FLOWMANAGER_STAGE_DIR` | Stage directory for question/answer files | `executor.New()` when `StageDir` configured |

### Testing File-Based Dialog Locally

Mock agents must implement their own polling:
```bash
while [ ! -f "$FLOWMANAGER_STAGE_DIR/<phase>.q1.answer.json" ]; do 
  sleep 0.5
done
answer=$(cat "$FLOWMANAGER_STAGE_DIR/<phase>.q1.answer.json" | jq -r '.answer')
```

Write question.json with proper schema:
```json
{
  "id": "q1",
  "question": "What should we do?",
  "options": ["Option A", "Option B"],
  "allow_custom": true
}
```

### Debugging Interactive Stages

Check stage directory for dialog files:
```bash
ls -la .flowManager/runs/<run_id>/<stage_id>/
# Look for: planning.q1.question.json, planning.q1.answer.json, planning.dialog.jsonl
```

Common patterns:
- **Agent waiting:** `*.question.json` exists, but no corresponding `*.answer.json` yet
- **Answer received:** Both `*.question.json` and `*.answer.json` exist, agent should have exited
- **Dialog history:** Check `*.dialog.jsonl` for full Q&A history (safe to ignore if missing)
- **Agent hung:** Check agent stdout/stderr in `<phase>.log` if polling loop times out (30s default)

### Polling Latency

- Orchestrator polls every **1 second** for new questions
- Answer detection is immediate (bash loop checks file existence continuously)
- UI dashboard polls `/api/stages/<id>/dialog` every ~2 seconds
- Total latency: question visible in UI within ~2-3 seconds of agent writing it

### Common Changes

When adding new interactive features:
1. Ensure agent writes `<phase>.<id>.question.json` in correct format
2. Ensure agent polls correctly: `while [ ! -f "$FLOWMANAGER_STAGE_DIR/<phase>.<id>.answer.json" ]; do sleep 30; done`
3. Update handler validation in `pkg/server/handlers.go` if phase names change
4. Add integration tests using `eagerProbeRunner` or similar wrappers
5. Verify atomic write pattern (O_EXCL) is preserved in handlers
