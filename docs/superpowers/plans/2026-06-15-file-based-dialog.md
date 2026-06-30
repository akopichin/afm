# File-based Dialog Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the MCP HTTP server with a file-based question/answer protocol, eliminating the 45s polling timeout and making question context fully visible in the UI.

**Architecture:** Agents write `<phase>.<id>.question.json` to `$FLOWMANAGER_STAGE_DIR` and poll for `<phase>.<id>.answer.json` via a bash loop. A 1-second polling goroutine in the orchestrator detects new question files and publishes `EventAskUser`. When the user answers via `POST /api/stages/<id>/dialog/answer`, the handler atomically writes `answer.json` (the agent's bash loop picks it up) and transitions stage status. `pkg/mcp/server.go` (~370 lines), `pkg/orchestrator/mcp_notifier.go` (~60 lines), and `pkg/mcp/server_test.go` (~200 lines) are deleted entirely.

**Tech Stack:** Go 1.22+, standard library only. No new dependencies.

---

## File Structure

| File | Action | What changes |
|------|--------|--------------|
| `pkg/mcp/dialog.go` | Modify | Add `QuestionFile` type + `FindUnansweredQuestions(stageDir)` |
| `pkg/mcp/dialog_test.go` | Modify | Add test for `FindUnansweredQuestions` |
| `pkg/mcp/server.go` | **Delete** | Entire MCP HTTP server |
| `pkg/mcp/server_test.go` | **Delete** | Tests for deleted server |
| `pkg/orchestrator/mcp_notifier.go` | **Delete** | Bridge deleted with server |
| `pkg/executor/executor.go` | Modify | Remove `McpConfig`, add `StageDir`; pass `FLOWMANAGER_STAGE_DIR` env var |
| `pkg/executor/executor_test.go` | Modify | Remove McpConfig test, add StageDir test |
| `pkg/orchestrator/orchestrator.go` | Modify | Add polling goroutine + agent tracking + `NotifyAnswer` + update `hasOpenQuestion` + update `onUserAnswered` + update `runnerFor` |
| `pkg/orchestrator/recovery.go` | Modify | Add agent activity tracking to all goroutine starts |
| `pkg/orchestrator/session.go` | Modify | Remove `writeMcpConfig` function |
| `pkg/server/handlers.go` | Modify | `handleDialogAnswer`: check question.json, write answer.json atomically |
| `pkg/server/server.go` | Modify | Remove `mcpSrv` field, `McpServer` from Config, `/mcp/` route |
| `pkg/prompts/builder.go` | Modify | Replace MCP instruction with file-based protocol instruction |
| `cmd/flowmanager/run.go` | Modify | Remove `mcp.NewServer`, wire `orch.NotifyAnswer` |
| `pkg/orchestrator/integration_interactive_test.go` | Modify | Rewrite for file-based protocol |
| `pkg/orchestrator/integration_resume_test.go` | Modify | Rewrite `TestResumeAfterCrash` for file-based protocol |

---

## Task 1: Add `FindUnansweredQuestions` to `pkg/mcp/dialog.go`

**Files:**
- Modify: `pkg/mcp/dialog.go`
- Test: `pkg/mcp/dialog_test.go`

- [x] **Step 1: Write the failing test**

Add to the bottom of `pkg/mcp/dialog_test.go`:

```go
func TestFindUnansweredQuestions(t *testing.T) {
	dir := t.TempDir()

	// Empty directory → empty result.
	got, err := mcp.FindUnansweredQuestions(dir)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty dir: want [], got %v, err %v", got, err)
	}

	// Single unanswered question.
	q1 := filepath.Join(dir, "planning.q1.question.json")
	if err := os.WriteFile(q1, []byte(`{"id":"q1","question":"proceed?","options":["yes","no"],"allow_custom":true}`), 0644); err != nil {
		t.Fatal(err)
	}
	got, err = mcp.FindUnansweredQuestions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 unanswered, got %d", len(got))
	}
	if got[0].Phase != "planning" || got[0].ID != "q1" || got[0].Question != "proceed?" {
		t.Fatalf("unexpected entry: %+v", got[0])
	}
	if !got[0].AllowCustom || len(got[0].Options) != 2 {
		t.Fatalf("allow_custom or options mismatch: %+v", got[0])
	}

	// Second question from a different phase.
	q2 := filepath.Join(dir, "implementation.q1.question.json")
	if err := os.WriteFile(q2, []byte(`{"id":"q1","question":"how?"}`), 0644); err != nil {
		t.Fatal(err)
	}
	got, err = mcp.FindUnansweredQuestions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 unanswered, got %d", len(got))
	}

	// Answer planning.q1 → should disappear from results.
	a1 := filepath.Join(dir, "planning.q1.answer.json")
	if err := os.WriteFile(a1, []byte(`{"id":"q1","answer":"yes"}`), 0644); err != nil {
		t.Fatal(err)
	}
	got, err = mcp.FindUnansweredQuestions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Phase != "implementation" {
		t.Fatalf("want 1 unanswered (implementation), got %v", got)
	}

	// Malformed JSON → skipped silently, does not error.
	bad := filepath.Join(dir, "review.q1.question.json")
	if err := os.WriteFile(bad, []byte(`not json`), 0644); err != nil {
		t.Fatal(err)
	}
	got, err = mcp.FindUnansweredQuestions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("malformed JSON must be skipped; want 1, got %d", len(got))
	}

	// allow_custom defaults to true when omitted.
	if !got[0].AllowCustom {
		t.Error("allow_custom should default to true when not present in JSON")
	}
}
```

- [x] **Step 2: Run test to confirm it fails**

```bash
cd /Users/alexander.kopichin/work/flowManager
go test ./pkg/mcp/ -run TestFindUnansweredQuestions -v
```

Expected: `FAIL — mcp.FindUnansweredQuestions undefined`

- [x] **Step 3: Add `QuestionFile` type and `FindUnansweredQuestions` to `pkg/mcp/dialog.go`**

Add `"path/filepath"` to the import block in `dialog.go`.

Then append to the end of `pkg/mcp/dialog.go` (after the `appendLine` function):

```go
// QuestionFile holds metadata extracted from a *.question.json file.
type QuestionFile struct {
	Phase       string
	ID          string
	Question    string
	Options     []string
	AllowCustom bool
}

// FindUnansweredQuestions scans stageDir for *.question.json files that do not
// have a matching *.answer.json. Filenames must follow "<phase>.<id>.question.json".
func FindUnansweredQuestions(stageDir string) ([]QuestionFile, error) {
	matches, err := filepath.Glob(filepath.Join(stageDir, "*.question.json"))
	if err != nil {
		return nil, err
	}
	var out []QuestionFile
	for _, qPath := range matches {
		base := strings.TrimSuffix(filepath.Base(qPath), ".question.json")
		dot := strings.Index(base, ".")
		if dot < 0 {
			continue
		}
		phase, id := base[:dot], base[dot+1:]

		answerPath := strings.TrimSuffix(qPath, ".question.json") + ".answer.json"
		if _, statErr := os.Stat(answerPath); statErr == nil {
			continue // already answered
		}

		raw, readErr := os.ReadFile(qPath)
		if readErr != nil {
			continue
		}
		var qf struct {
			ID          string   `json:"id"`
			Question    string   `json:"question"`
			Options     []string `json:"options"`
			AllowCustom *bool    `json:"allow_custom"`
		}
		if json.Unmarshal(raw, &qf) != nil {
			continue // skip malformed files
		}
		actualID := qf.ID
		if actualID == "" {
			actualID = id
		}
		allowCustom := true
		if qf.AllowCustom != nil {
			allowCustom = *qf.AllowCustom
		}
		out = append(out, QuestionFile{
			Phase: phase, ID: actualID, Question: qf.Question,
			Options: qf.Options, AllowCustom: allowCustom,
		})
	}
	return out, nil
}
```

- [x] **Step 4: Run tests to confirm they pass**

```bash
go test ./pkg/mcp/ -v
```

Expected: all tests PASS including `TestFindUnansweredQuestions`

- [x] **Step 5: Commit**

```bash
git add pkg/mcp/dialog.go pkg/mcp/dialog_test.go
git commit -m "feat: добавить FindUnansweredQuestions в pkg/mcp/dialog"
```

---

## Task 2: Executor — remove `McpConfig`, add `StageDir`; clean up orchestrator and session

These three files must be committed together because removing `McpConfig` from executor breaks compilation in orchestrator.

**Files:**
- Modify: `pkg/executor/executor.go:18-27` (Config struct)
- Modify: `pkg/executor/executor.go:340-344` (run() MCP arg)
- Modify: `pkg/executor/executor.go:355-363` (run() env filter)
- Modify: `pkg/executor/executor_test.go:366,379,445` (McpConfig tests)
- Modify: `pkg/orchestrator/orchestrator.go:173-214` (runnerFor)
- Modify: `pkg/orchestrator/session.go:55-73` (remove writeMcpConfig)

- [x] **Step 1: Update `Config` struct in `pkg/executor/executor.go`**

In `pkg/executor/executor.go`, replace `McpConfig string` with `StageDir string` in Config:

```go
// Config configures the executor.
type Config struct {
	Command     string
	ExtraArgs   []string
	IdleTimeout time.Duration
	OnAction    func(tool, detail string)
	SessionID   string
	Resume      bool
	StageDir    string // passed to agent as FLOWMANAGER_STAGE_DIR env var
}
```

- [x] **Step 2: Update `run()` in `pkg/executor/executor.go`**

In `run()` (line ~340), remove the `--mcp-config` block:

Remove these lines:
```go
	if e.cfg.McpConfig != "" {
		args = append(args, "--mcp-config", e.cfg.McpConfig)
	}
```

In the env filter section (line ~355), add `FLOWMANAGER_STAGE_DIR` after the CLAUDECODE filter:

```go
	env := os.Environ()
	filtered := env[:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, "CLAUDECODE=") {
			filtered = append(filtered, kv)
		}
	}
	if e.cfg.StageDir != "" {
		filtered = append(filtered, "FLOWMANAGER_STAGE_DIR="+e.cfg.StageDir)
	}
	cmd.Env = filtered
```

- [x] **Step 3: Update executor tests in `pkg/executor/executor_test.go`**

Find and replace the test that checks `--mcp-config` is present (around line 366). Remove the McpConfig field usage and replace with a StageDir test:

Find the test that sets `McpConfig: "/tmp/mcp-test.json"` and checks for `--mcp-config` in args. Replace it with a test that verifies `FLOWMANAGER_STAGE_DIR` is set:

```go
func TestRunSetsStageDir(t *testing.T) {
	// echoenv prints the value of FLOWMANAGER_STAGE_DIR
	scriptPath := writeTempScript(t, "#!/bin/bash\necho \"$FLOWMANAGER_STAGE_DIR\"\necho '{\"type\":\"result\",\"subtype\":\"success\"}'")
	e := executor.New(executor.Config{
		Command:  scriptPath,
		StageDir: "/tmp/test-stage-dir",
	})
	var got string
	err := e.RunAgent(context.Background(), "test", "stage", "prompt", filepath.Join(t.TempDir(), "log"))
	_ = got
	_ = err
	// Verify env is set by running a script that prints it
	outDir := t.TempDir()
	logFile := filepath.Join(outDir, "test.log")
	envScript := writeTempScript(t, "#!/bin/bash\nprintf '%s' \"$FLOWMANAGER_STAGE_DIR\"\necho\necho '{\"type\":\"result\",\"subtype\":\"success\"}'")
	e2 := executor.New(executor.Config{
		Command:  envScript,
		StageDir: "/tmp/my-stage",
	})
	err = e2.RunAgent(context.Background(), "test", "stage", "prompt", logFile)
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	data, _ := os.ReadFile(logFile)
	if !strings.Contains(string(data), "/tmp/my-stage") {
		t.Errorf("FLOWMANAGER_STAGE_DIR not in agent output; log: %s", data)
	}
}
```

Also find and remove the old test asserting `--mcp-config` is absent or present (search for `"--mcp-config"` in the test file).

At line ~445 where the test checks `!strings.Contains(got, "--mcp-config")`, remove that assertion since `--mcp-config` no longer exists at all.

- [x] **Step 4: Update `runnerFor` in `pkg/orchestrator/orchestrator.go`**

Replace the entire `runnerFor` function (lines 170-225):

```go
// runnerFor returns the appropriate Runner for a stage's phase.
// For interactive stages it generates a session id and returns an executor
// configured with --session-id / --resume and FLOWMANAGER_STAGE_DIR env.
func (o *Orchestrator) runnerFor(s flow.Stage, phase string) executor.Runner {
	if !s.Interactive {
		if s.Command == "" {
			return o.runner
		}
		return executor.New(executor.Config{
			Command:     s.Command,
			IdleTimeout: o.opts.Config.Executor.IdleTimeout,
			OnAction:    uiActionPublisher(o.ui, s.ID),
		})
	}

	stageDir := filepath.Join(o.opts.RunDir, s.ID)
	resume := sessionExists(stageDir, phase)
	sessionID, err := loadOrCreateSession(stageDir, phase)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: interactive stage %q: session failed: %v; using non-interactive runner\n", s.ID, err)
		return o.runnerForFallback(s)
	}

	cmd := s.Command
	if cmd == "" {
		cmd = o.opts.Config.Client.Command
	}
	requiredArgs := []string{"--print", "--output-format", "stream-json", "--dangerously-skip-permissions"}
	extraArgs := append(requiredArgs, o.opts.Config.Client.ExtraArgs...)
	return executor.New(executor.Config{
		Command:     cmd,
		ExtraArgs:   extraArgs,
		IdleTimeout: o.opts.Config.Executor.IdleTimeout,
		OnAction:    uiActionPublisher(o.ui, s.ID),
		SessionID:   sessionID,
		Resume:      resume,
		StageDir:    stageDir,
	})
}
```

- [x] **Step 5: Remove `writeMcpConfig` from `pkg/orchestrator/session.go`**

Delete lines 55-73 (the entire `writeMcpConfig` function):

```go
func writeMcpConfig(stageDir, stageID, phase, dashURL string) (string, error) {
	// ... entire function
}
```

The file should keep only: `phaseSession`, `sessionFile`, `loadOrCreateSession`, `sessionExists`, `newUUID`.

Also remove unused imports in session.go if any appear (the import block uses `crypto/rand`, `encoding/json`, `fmt`, `os`, `path/filepath` — all still needed).

- [x] **Step 6: Build check**

```bash
go build ./...
```

Expected: compiles without errors.

- [x] **Step 7: Run tests**

```bash
go test ./pkg/executor/ -v
go test ./pkg/orchestrator/ -run TestIntegration -v
```

Expected: PASS (executor tests updated, orchestrator integration tests pass or skip gracefully).

- [x] **Step 8: Commit**

```bash
git add pkg/executor/executor.go pkg/executor/executor_test.go \
    pkg/orchestrator/orchestrator.go pkg/orchestrator/session.go
git commit -m "feat: заменить McpConfig на StageDir (FLOWMANAGER_STAGE_DIR) в executor и orchestrator"
```

---

## Task 3: Orchestrator — agent tracking, `NotifyAnswer`, polling goroutine

All additions. No existing behavior changes in this task.

**Files:**
- Modify: `pkg/orchestrator/orchestrator.go` (add struct fields, methods, goroutine)
- Modify: `pkg/orchestrator/recovery.go` (add tracking to goroutine starts)

- [x] **Step 1: Add imports to `pkg/orchestrator/orchestrator.go`**

Add `"sync"` and `"time"` to the imports block in orchestrator.go.

- [x] **Step 2: Add `activeAgents` field to the `Orchestrator` struct**

In the `Orchestrator` struct (line ~69), add:

```go
type Orchestrator struct {
	opts         Options
	graph        *Graph
	runner       executor.Runner
	critical     *CriticalBus
	ui           *UIBus
	fsm          *FSM
	sems         map[string]interface {
		acquire()
		release()
	}
	activeAgents sync.Map // stageID → struct{}: set while an agent goroutine runs
}
```

- [x] **Step 3: Add agent activity helpers**

Add after the `New` function or near the other helper functions:

```go
func (o *Orchestrator) markAgentActive(stageID string) { o.activeAgents.Store(stageID, struct{}{}) }
func (o *Orchestrator) markAgentDone(stageID string)   { o.activeAgents.Delete(stageID) }
func (o *Orchestrator) isAgentActive(stageID string) bool {
	_, ok := o.activeAgents.Load(stageID)
	return ok
}
```

- [x] **Step 4: Add `NotifyAnswer` method**

Add after the `FailStage` function:

```go
// NotifyAnswer is called by the HTTP handler when the user submits an answer.
// If the agent goroutine is still running (bash loop awaiting answer.json),
// we only transition the status — the bash loop will detect the file and
// continue without a restart. If the goroutine has exited, we publish to
// the critical bus so onUserAnswered can restart it.
func (o *Orchestrator) NotifyAnswer(stageID, phase, qID, answer string, fromOptions bool) error {
	if o.isAgentActive(stageID) {
		if phase == phasePlanning {
			o.Trigger(stageID, EvUserAnswered, GuardCtx{Phase: phasePlanning}, "")
		} else {
			o.Trigger(stageID, EvUserAnswered, GuardCtx{Phase: phaseImplementation}, "")
		}
		o.ui.Publish(Event{Type: EventUserAnswered, StageID: stageID, Data: map[string]any{
			"id": qID, "phase": phase, "answer": answer,
		}})
		return nil
	}
	return o.critical.Publish(context.Background(), Event{
		Type:    EventUserAnswered,
		StageID: stageID,
		Data:    map[string]any{"id": qID, "phase": phase, "answer": answer},
	})
}
```

- [x] **Step 5: Add `startQuestionPoller` and `pollQuestions`**

Add after the `Run` method:

```go
// startQuestionPoller launches a goroutine that scans active stage directories
// every second for new *.question.json files (file-based dialog protocol).
func (o *Orchestrator) startQuestionPoller(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		processed := map[string]bool{} // "stageID|phase|id" → true
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				o.pollQuestions(processed)
			}
		}
	}()
}

// pollQuestions scans each active stage directory for unanswered question files.
// For each new file: writes to dialog.jsonl (for UI history) and publishes
// EventAskUser to transition the stage to awaiting_user_input.
func (o *Orchestrator) pollQuestions(processed map[string]bool) {
	snap := o.opts.Store.Snapshot()
	for stageID, st := range snap.Stages {
		switch st.Status {
		case state.StatusPlanning, state.StatusRunning, state.StatusRevising,
			state.StatusRetrying, state.StatusAwaitingUserInput:
		default:
			continue
		}
		stageDir := filepath.Join(o.opts.RunDir, stageID)
		questions, err := mcp.FindUnansweredQuestions(stageDir)
		if err != nil {
			continue
		}
		for _, q := range questions {
			key := stageID + "|" + q.Phase + "|" + q.ID
			if processed[key] {
				continue
			}
			processed[key] = true
			// Write to dialog.jsonl for history (idempotent via FindEntry check).
			dialogPath := filepath.Join(stageDir, q.Phase+".dialog.jsonl")
			if e, _ := mcp.FindEntry(dialogPath, q.ID); e == nil {
				_ = mcp.AppendQuestion(dialogPath, mcp.Question{
					ID:          q.ID,
					Question:    q.Question,
					Options:     q.Options,
					AllowCustom: q.AllowCustom,
				})
			}
			// Notify UI and transition stage status.
			o.ui.Publish(Event{
				Type:    EventAskUser,
				StageID: stageID,
				Data: map[string]any{
					"id": q.ID, "phase": q.Phase, "question": q.Question,
					"options": q.Options, "allow_custom": q.AllowCustom,
				},
			})
			o.Trigger(stageID, EvAskUser, GuardCtx{Phase: q.Phase}, "")
		}
	}
}
```

- [x] **Step 6: Call `startQuestionPoller` from `Run()`**

In the `Run` method (line ~252), add the poller call:

```go
func (o *Orchestrator) Run(ctx context.Context) error {
	o.startPlanningForPending(ctx)
	o.startQuestionPoller(ctx) // file-based dialog poller

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev := <-o.critical.Recv():
			if err := o.handleEvent(ctx, ev); err != nil {
				return err
			}
			if o.allTerminal() {
				return nil
			}
		}
	}
}
```

- [x] **Step 7: Add agent tracking to all goroutine starts in `orchestrator.go`**

Add `markAgentActive`/`markAgentDone` to every goroutine that runs an agent function. Pattern to apply:

```go
go func(st flow.Stage) {
    sem := o.semFor(st)
    sem.acquire()
    o.markAgentActive(st.ID)
    defer func() {
        o.markAgentDone(st.ID)
        sem.release()
    }()
    o.runPlanningAgent(ctx, st)  // or runImplementationAgent
}(s)
```

Apply this pattern to ALL of the following goroutines in orchestrator.go:
1. `startPlanningForUnblocked` (line ~576): goroutine calling `runPlanningAgent`
2. `startReadyStages` (line ~600): goroutine calling `runImplementationAgent`
3. `onRevised` (line ~439): goroutine calling `runPlanningWithFeedback` (use `s.ID` for stageID)
4. `onManualRetry` (line ~469): goroutine calling `runImplementationAgent`
5. `onManualRetry` (line ~484): goroutine calling `runImplementationAgent`
6. `onManualRetry` (line ~501): goroutine calling `runPlanningAgent`
7. `onUserAnswered` (line ~379): goroutine calling `runPlanningAgent`
8. `onUserAnswered` (line ~387): goroutine calling `runImplementationAgent`

For the `onRevised` goroutine (which doesn't receive `st`):
```go
go func() {
    sem.acquire()
    o.markAgentActive(stageID)
    defer func() {
        o.markAgentDone(stageID)
        sem.release()
    }()
    o.runPlanningWithFeedback(ctx, s)
}()
```

- [x] **Step 8: Add agent tracking to goroutine starts in `recovery.go`**

Same pattern, apply to these goroutines in `startPlanningForPending`:
1. Line ~67: goroutine calling `resumeInteractiveAgent` — tracking goes inside `resumeInteractiveAgent` itself (see Step 9)
2. Line ~86 (retrying path): goroutine calling `runPlanningAgent`
3. Line ~94 (revising path): goroutine calling `runPlanningWithFeedback` — tracking in `runPlanningWithFeedback` itself (see Step 9)
4. Line ~108 (running path): goroutine calling `runImplementationAgent`
5. Line ~129 (default path): goroutine calling `runPlanningAgent`

Add tracking to goroutines for paths 2, 4, 5 in recovery.go. Skip 1 and 3 since they're handled in the next step.

- [x] **Step 9: Add tracking inside `resumeInteractiveAgent` and `runPlanningWithFeedback`**

In `resumeInteractiveAgent`:
```go
func (o *Orchestrator) resumeInteractiveAgent(ctx context.Context, s flow.Stage) {
	o.markAgentActive(s.ID)
	defer o.markAgentDone(s.ID)
	// ... rest unchanged
}
```

In `runPlanningWithFeedback` (orchestrator.go):
```go
func (o *Orchestrator) runPlanningWithFeedback(ctx context.Context, s flow.Stage) {
	o.markAgentActive(s.ID)
	defer o.markAgentDone(s.ID)
	// ... rest unchanged
}
```

Note: `runPlanningAgent` and `runImplementationAgent` are NOT marked here — they are called both from goroutines (tracked at goroutine level) and from `resumeInteractiveAgent` (tracked at its level). Double-marking is safe because `sync.Map.Store` is idempotent.

- [x] **Step 10: Write polling goroutine test**

Add to `pkg/orchestrator/orchestrator_test.go` (or create a new file `pkg/orchestrator/poller_test.go`):

```go
func TestPollQuestions_DetectsNewQuestion(t *testing.T) {
	runDir := t.TempDir()
	stageID := "poll-test"
	stageDir := filepath.Join(runDir, stageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}

	store, err := state.Open(runDir, []string{stageID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	_ = store.Apply(state.Transition{StageID: stageID, From: state.StatusPending, To: state.StatusRunning, Event: "test"})

	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir,
		Stages: []flow.Stage{{ID: stageID, Name: "poll-test"}},
		Store:  store,
		Config: config.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	uiBus := orch.UIBus()
	sub := uiBus.Subscribe()
	defer uiBus.Unsubscribe(sub)

	// Write question file BEFORE starting poller.
	qPath := filepath.Join(stageDir, "implementation.q1.question.json")
	if err := os.WriteFile(qPath, []byte(`{"id":"q1","question":"go?"}`), 0644); err != nil {
		t.Fatal(err)
	}

	go func() { _ = orch.Run(ctx) }()

	// Polling goroutine should detect the question within 2 seconds.
	timeout := time.After(2 * time.Second)
	for {
		select {
		case ev := <-sub:
			if ev.Type == orchestrator.EventAskUser && ev.StageID == stageID {
				return // success
			}
		case <-timeout:
			t.Fatal("timeout: EventAskUser not received within 2s")
		}
	}
}

func TestPollQuestions_Idempotent(t *testing.T) {
	runDir := t.TempDir()
	stageID := "idem-test"
	stageDir := filepath.Join(runDir, stageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}

	store, err := state.Open(runDir, []string{stageID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	_ = store.Apply(state.Transition{StageID: stageID, From: state.StatusPending, To: state.StatusRunning, Event: "test"})

	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir,
		Stages: []flow.Stage{{ID: stageID, Name: "idem-test"}},
		Store:  store,
		Config: config.Default(),
	})

	qPath := filepath.Join(stageDir, "implementation.q1.question.json")
	if err := os.WriteFile(qPath, []byte(`{"id":"q1","question":"go?"}`), 0644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	uiBus := orch.UIBus()
	sub := uiBus.Subscribe()
	defer uiBus.Unsubscribe(sub)

	go func() { _ = orch.Run(ctx) }()

	askCount := 0
	timer := time.After(2 * time.Second)
	done := false
	for !done {
		select {
		case ev := <-sub:
			if ev.Type == orchestrator.EventAskUser && ev.StageID == stageID {
				askCount++
			}
		case <-timer:
			done = true
		}
	}
	if askCount != 1 {
		t.Errorf("EventAskUser published %d times, want exactly 1", askCount)
	}
}
```

Note: these tests require `UIBus.Subscribe`/`Unsubscribe` to be exported. If they don't exist, check the existing test helpers (like `uiBus.Recv()`) and adapt accordingly.

- [x] **Step 11: Build and run tests**

```bash
go build ./...
go test ./pkg/orchestrator/ -run TestPollQuestions -v -timeout 30s
```

Expected: both polling tests PASS.

- [x] **Step 12: Commit**

```bash
git add pkg/orchestrator/orchestrator.go pkg/orchestrator/recovery.go
git commit -m "feat: polling-горутин, NotifyAnswer и отслеживание активных агентов"
```

---

## Task 4: Orchestrator — update `hasOpenQuestion` and `onUserAnswered`

**Files:**
- Modify: `pkg/orchestrator/orchestrator.go:340-395`

- [x] **Step 1: Replace `hasOpenQuestion` implementation**

Replace lines 340-350 with a file-based scan instead of dialog.jsonl:

```go
// hasOpenQuestion reports whether stageDir contains a *.question.json file
// for the given phase that has no corresponding *.answer.json yet.
func (o *Orchestrator) hasOpenQuestion(stageID, phase string) bool {
	if phase == "" {
		return false
	}
	questions, err := mcp.FindUnansweredQuestions(filepath.Join(o.opts.RunDir, stageID))
	if err != nil {
		return false
	}
	for _, q := range questions {
		if q.Phase == phase {
			return true
		}
	}
	return false
}
```

- [x] **Step 2: Update `onUserAnswered`**

Replace the `onUserAnswered` function (lines 356-395):

```go
// onUserAnswered resumes a stage that was paused on awaiting_user_input.
// If the agent is still running (its bash loop is waiting for answer.json),
// NotifyAnswer already transitioned the status — this is a no-op.
// If the agent exited before the user answered, we restart it here.
func (o *Orchestrator) onUserAnswered(ctx context.Context, ev Event) error {
	if o.currentStatus(ev.StageID) != state.StatusAwaitingUserInput {
		return nil
	}

	data, _ := ev.Data.(map[string]any)
	phase, _ := data["phase"].(string)
	if phase == "" {
		return nil
	}

	if o.hasOpenQuestion(ev.StageID, phase) {
		return nil
	}

	stage := o.graph.Stage(ev.StageID)
	if stage == nil {
		return nil
	}

	// Agent exited before the user answered. Restart it so it can read
	// answer.json (bash loop exits immediately since the file now exists).
	switch phase {
	case phasePlanning:
		o.Trigger(ev.StageID, EvUserAnswered, GuardCtx{Phase: phasePlanning}, "")
		go func(st flow.Stage) {
			sem := o.semFor(st)
			sem.acquire()
			o.markAgentActive(st.ID)
			defer func() {
				o.markAgentDone(st.ID)
				sem.release()
			}()
			o.runPlanningAgent(ctx, st)
		}(*stage)
	default:
		o.Trigger(ev.StageID, EvUserAnswered, GuardCtx{Phase: phaseImplementation}, "")
		go func(st flow.Stage) {
			sem := o.semFor(st)
			sem.acquire()
			o.markAgentActive(st.ID)
			defer func() {
				o.markAgentDone(st.ID)
				sem.release()
			}()
			o.runImplementationAgent(ctx, st)
		}(*stage)
	}
	return nil
}
```

- [x] **Step 3: Build and run tests**

```bash
go build ./...
go test ./pkg/orchestrator/ -run TestIntegration -v -timeout 30s
```

Expected: PASS.

- [x] **Step 4: Commit**

```bash
git add pkg/orchestrator/orchestrator.go
git commit -m "feat: hasOpenQuestion и onUserAnswered через файловый протокол"
```

---

## Task 5: Server handlers — check question.json, write answer.json atomically

**Files:**
- Modify: `pkg/server/handlers.go:255-303`
- Test: `pkg/server/handlers_test.go`

- [x] **Step 1: Write failing test for answer.json write**

Add to `pkg/server/handlers_test.go`:

```go
func TestHandleDialogAnswer_WritesAnswerFile(t *testing.T) {
	srv, runDir := setupTestServer(t)
	stageDir := filepath.Join(runDir, testStageID)

	// Write a question file (agent-side).
	qPath := filepath.Join(stageDir, "planning."+testQuestionID+".question.json")
	if err := os.WriteFile(qPath, []byte(`{"id":"q1","question":"proceed?","options":["yes"],"allow_custom":false}`), 0644); err != nil {
		t.Fatal(err)
	}
	// Pre-populate dialog.jsonl so AppendAnswer has a matching question.
	dialogPath := filepath.Join(stageDir, "planning.dialog.jsonl")
	if err := mcp.AppendQuestion(dialogPath, mcp.Question{ID: testQuestionID, Question: "proceed?"}); err != nil {
		t.Fatal(err)
	}

	body := `{"id":"q1","phase":"planning","answer":"yes","from_options":true}`
	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/dialog/answer",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}

	// answer.json must exist with correct content.
	answerPath := filepath.Join(stageDir, "planning."+testQuestionID+".answer.json")
	data, err := os.ReadFile(answerPath)
	if err != nil {
		t.Fatalf("answer.json not created: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("invalid JSON in answer.json: %v", err)
	}
	if got["answer"] != "yes" {
		t.Errorf("answer.json answer mismatch: %v", got)
	}
	if got["from_options"] != true {
		t.Errorf("answer.json from_options mismatch: %v", got)
	}
}

func TestHandleDialogAnswer_QuestionNotFound(t *testing.T) {
	srv, _ := setupTestServer(t)
	body := `{"id":"nonexistent","phase":"planning","answer":"yes"}`
	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/dialog/answer",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", w.Code)
	}
}

func TestHandleDialogAnswer_DuplicateAnswer(t *testing.T) {
	srv, runDir := setupTestServer(t)
	stageDir := filepath.Join(runDir, testStageID)

	// Create question.json + answer.json (already answered).
	qPath := filepath.Join(stageDir, "planning.q1.question.json")
	aPath := filepath.Join(stageDir, "planning.q1.answer.json")
	if err := os.WriteFile(qPath, []byte(`{"id":"q1","question":"x?"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(aPath, []byte(`{"id":"q1","answer":"yes"}`), 0644); err != nil {
		t.Fatal(err)
	}

	body := `{"id":"q1","phase":"planning","answer":"no"}`
	req := httptest.NewRequest("POST", "/api/stages/"+testStageID+"/dialog/answer",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Errorf("want 409, got %d", w.Code)
	}
}
```

- [x] **Step 2: Run tests to confirm they fail**

```bash
go test ./pkg/server/ -run "TestHandleDialogAnswer" -v
```

Expected: FAIL — tests fail because answer.json is not created and question checks are wrong.

- [x] **Step 3: Rewrite `handleDialogAnswer` in `pkg/server/handlers.go`**

Replace the full `handleDialogAnswer` function (lines 255-303):

```go
func (s *Server) handleDialogAnswer(w http.ResponseWriter, r *http.Request) {
	stageID := extractStageID(r.URL.Path, "/api/stages/", "/dialog/answer")
	if !isValidStageID(stageID) {
		http.Error(w, "invalid stage id", http.StatusBadRequest)
		return
	}
	var req dialogAnswerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if req.ID == "" || req.Phase == "" || req.Answer == "" {
		http.Error(w, "id, phase, answer required", http.StatusBadRequest)
		return
	}
	if req.Phase != phasePlanning && req.Phase != phaseImplementation && req.Phase != phaseReview {
		http.Error(w, "invalid phase", http.StatusBadRequest)
		return
	}
	stageDir := filepath.Join(s.runDir, stageID)
	questionPath := filepath.Join(stageDir, req.Phase+"."+req.ID+".question.json")
	answerPath := filepath.Join(stageDir, req.Phase+"."+req.ID+".answer.json")

	// Question must exist as a file written by the agent.
	if _, err := os.Stat(questionPath); err != nil {
		http.Error(w, "question not found", http.StatusNotFound)
		return
	}
	// Reject duplicate answers.
	if _, err := os.Stat(answerPath); err == nil {
		http.Error(w, "question already answered", http.StatusConflict)
		return
	}

	// Persist answer in dialog history for the UI.
	dialogPath := filepath.Join(stageDir, req.Phase+".dialog.jsonl")
	if err := mcp.AppendAnswer(dialogPath, mcp.Answer{
		ID: req.ID, Answer: req.Answer, FromOptions: req.FromOptions,
	}); err != nil {
		http.Error(w, "persist answer: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Atomically write answer.json so the agent's bash loop picks it up.
	payload, _ := json.Marshal(map[string]any{
		"id": req.ID, "answer": req.Answer, "from_options": req.FromOptions,
	})
	tmp := answerPath + ".tmp"
	if err := os.WriteFile(tmp, payload, 0644); err == nil {
		_ = os.Rename(tmp, answerPath)
	}

	if s.dialogAnswerFn != nil {
		if err := s.dialogAnswerFn(stageID, req.Phase, req.ID, req.Answer, req.FromOptions); err != nil {
			http.Error(w, "notify: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{keyStatus: "ok"})
}
```

Make sure `"os"` is in the imports of handlers.go (it likely already is, but verify).

- [x] **Step 4: Run tests to confirm they pass**

```bash
go test ./pkg/server/ -v
```

Expected: all tests PASS including new dialog answer tests.

- [x] **Step 5: Commit**

```bash
git add pkg/server/handlers.go pkg/server/handlers_test.go
git commit -m "feat: handleDialogAnswer пишет answer.json атомарно через tmp+rename"
```

---

## Task 6: Update `pkg/prompts/builder.go` — file-based interactive instruction

**Files:**
- Modify: `pkg/prompts/builder.go:43-47`

- [x] **Step 1: Replace the MCP instruction block**

In `Build()` function, replace lines 43-47:

Current code:
```go
	if in.Interactive {
		sb.WriteString("\n\n<interactive_rules>\n")
		sb.WriteString("You may use the mcp__flowmanager__ask_user tool. Ask ONE question at a time. The tool BLOCKS until the user answers — wait, do not retry, do not skip.\n")
		sb.WriteString("</interactive_rules>\n")
	}
```

New code:
```go
	if in.Interactive {
		sb.WriteString("\n\n<interactive_rules>\n")
		sb.WriteString("Use the file-based dialog protocol to ask the user questions.\n")
		sb.WriteString("The env var FLOWMANAGER_STAGE_DIR contains your stage directory.\n")
		sb.WriteString("Assign sequential IDs: q1, q2, … (never reuse an ID within a phase).\n\n")
		sb.WriteString("For each question:\n")
		sb.WriteString("1. Write the question file using the Write tool:\n")
		sb.WriteString("   Path: $FLOWMANAGER_STAGE_DIR/<phase>.q<N>.question.json\n")
		sb.WriteString("   Content: {\"id\":\"qN\",\"question\":\"## Full context here\\n\\nYour question?\",\"options\":[\"A\",\"B\"],\"allow_custom\":true}\n")
		sb.WriteString("   Put ALL context in 'question': descriptions, trade-offs, examples. Use markdown freely.\n")
		sb.WriteString("2. Wait for the answer via Bash:\n")
		sb.WriteString("   while [ ! -f $FLOWMANAGER_STAGE_DIR/<phase>.qN.answer.json ]; do sleep 30; done && cat $FLOWMANAGER_STAGE_DIR/<phase>.qN.answer.json\n")
		sb.WriteString("3. If bash times out (10 min) without the file: run the exact same bash command again.\n")
		sb.WriteString("   NEVER give up waiting — keep retrying the bash loop until the file appears.\n")
		sb.WriteString("Ask ONE question at a time.\n")
		sb.WriteString("</interactive_rules>\n")
	}
```

- [x] **Step 2: Build and run prompts tests**

```bash
go build ./...
go test ./pkg/prompts/ -v
```

Expected: PASS.

- [x] **Step 3: Commit**

```bash
git add pkg/prompts/builder.go
git commit -m "feat: инструкция файлового диалога вместо MCP в системном промпте"
```

---

## Task 7: Remove MCP server — update server.go, run.go, delete MCP files

All changes in this task must be committed together to maintain a compilable state.

**Files:**
- Modify: `pkg/server/server.go`
- Modify: `cmd/flowmanager/run.go`
- Delete: `pkg/mcp/server.go`
- Delete: `pkg/mcp/server_test.go`
- Delete: `pkg/orchestrator/mcp_notifier.go`

- [x] **Step 1: Clean up `pkg/server/server.go`**

Remove `mcpSrv *mcp.Server` from the `Server` struct, `McpServer *mcp.Server` from `Config`, `mcpSrv: cfg.McpServer` from `New()`, and the `/mcp/` route registration.

New `server.go`:

```go
package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
	"github.com/akopichin/afm/pkg/web"
)

// Server is the HTTP server for the dashboard and API.
type Server struct {
	runDir         string
	store          *state.Store
	uiBus          *orchestrator.UIBus
	approveFn      func(ctx context.Context, stageID string) error
	reviseFn       func(ctx context.Context, stageID, feedback string) error
	retryFn        func(ctx context.Context, stageID string) error
	dialogAnswerFn func(stageID, phase, qID, answer string, fromOptions bool) error
	dialogCancelFn func(stageID string) error
	httpSrv        *http.Server
}

// Config holds server settings.
type Config struct {
	Port           int
	RunDir         string
	Store          *state.Store
	UIBus          *orchestrator.UIBus
	ApproveFn      func(ctx context.Context, stageID string) error
	ReviseFn       func(ctx context.Context, stageID, feedback string) error
	RetryFn        func(ctx context.Context, stageID string) error
	DialogAnswerFn func(stageID, phase, qID, answer string, fromOptions bool) error
	DialogCancelFn func(stageID string) error
}

// New creates a Server.
func New(cfg Config) *Server {
	s := &Server{
		runDir:         cfg.RunDir,
		store:          cfg.Store,
		uiBus:          cfg.UIBus,
		approveFn:      cfg.ApproveFn,
		reviseFn:       cfg.ReviseFn,
		retryFn:        cfg.RetryFn,
		dialogAnswerFn: cfg.DialogAnswerFn,
		dialogCancelFn: cfg.DialogCancelFn,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/stages/", s.routeStages)
	mux.HandleFunc("/ws", s.handleWebSocket)
	mux.Handle("/", http.FileServer(http.FS(web.FS)))

	s.httpSrv = &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s
}

func (s *Server) routeStages(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case strings.HasSuffix(path, "/plan"):
		s.handlePlan(w, r)
	case strings.HasSuffix(path, "/log"):
		s.handleLog(w, r)
	case strings.HasSuffix(path, "/approve") && r.Method == http.MethodPost:
		s.handleApprove(w, r)
	case strings.HasSuffix(path, "/revise") && r.Method == http.MethodPost:
		s.handleRevise(w, r)
	case strings.HasSuffix(path, "/retry") && r.Method == http.MethodPost:
		s.handleRetry(w, r)
	case strings.HasSuffix(path, "/dialog") && r.Method == http.MethodGet:
		s.handleDialogGet(w, r)
	case strings.HasSuffix(path, "/dialog/answer") && r.Method == http.MethodPost:
		s.handleDialogAnswer(w, r)
	case strings.HasSuffix(path, "/dialog/cancel") && r.Method == http.MethodPost:
		s.handleDialogCancel(w, r)
	default:
		http.NotFound(w, r)
	}
}

// Handler returns the HTTP handler for testing.
func (s *Server) Handler() http.Handler {
	return s.httpSrv.Handler
}

// Start starts the HTTP server. Returns actual address.
func (s *Server) Start() (string, error) {
	ln, err := net.Listen("tcp", s.httpSrv.Addr)
	if err != nil {
		return "", fmt.Errorf("listen: %w", err)
	}
	go func() { _ = s.httpSrv.Serve(ln) }()
	return ln.Addr().String(), nil
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpSrv.Shutdown(ctx)
}
```

- [x] **Step 2: Update `cmd/flowmanager/run.go`**

Remove `mcp.NewServer`, remove `McpServer` from server.Config, update `DialogAnswerFn` to call `orch.NotifyAnswer`, simplify `DialogCancelFn`.

Replace lines 86-135 (from `mcpSrv := ...` through `orch.SetDashboardURL(dashURL)`):

```go
			// Disable interactive flags when dashboard is not running.
			if cfg.Server.GetPort() == 0 {
				for i := range f.Stages {
					if f.Stages[i].Interactive {
						f.Stages[i].Interactive = false
						fmt.Fprintf(os.Stderr, "warning: stage %q: interactive requires dashboard (server port > 0); running as non-interactive\n", f.Stages[i].ID)
					}
				}
			}

			// Start HTTP server if port > 0.
			if cfg.Server.GetPort() > 0 {
				srv := server.New(server.Config{
					Port:      cfg.Server.GetPort(),
					RunDir:    runDir,
					Store:     store,
					UIBus:     orch.UIBus(),
					ApproveFn: orch.Approve,
					ReviseFn:  orch.Revise,
					RetryFn:   orch.Retry,
					DialogAnswerFn: func(stageID, phase, qID, answer string, fromOptions bool) error {
						return orch.NotifyAnswer(stageID, phase, qID, answer, fromOptions)
					},
					DialogCancelFn: func(stageID string) error {
						orch.FailStage(stageID, "cancelled by user")
						return nil
					},
				})
				addr, err := srv.Start()
				if err != nil {
					return fmt.Errorf("start dashboard: %w", err)
				}
				defer func() { _ = srv.Shutdown(context.Background()) }()

				_, port, _ := net.SplitHostPort(addr)
				dashURL := fmt.Sprintf("http://localhost:%s", port) //nolint:revive
				orch.SetDashboardURL(dashURL)
				fmt.Printf("  dashboard: %s\n", dashURL)
				if cfg.Server.IsOpenBrowser() {
					openBrowser(dashURL)
				}
			}
```

Also remove the `"github.com/akopichin/afm/pkg/mcp"` import from run.go.

- [x] **Step 3: Delete the MCP server files**

```bash
rm /Users/alexander.kopichin/work/flowManager/pkg/mcp/server.go
rm /Users/alexander.kopichin/work/flowManager/pkg/mcp/server_test.go
rm /Users/alexander.kopichin/work/flowManager/pkg/orchestrator/mcp_notifier.go
```

- [x] **Step 4: Build check**

```bash
go build ./...
```

Expected: compiles without errors.

- [x] **Step 5: Run server tests**

```bash
go test ./pkg/server/ -v
go test ./pkg/mcp/ -v
```

Expected: PASS. (The mcp package still has dialog.go and its tests; server tests no longer reference mcp.Server.)

- [x] **Step 6: Commit**

```bash
git add pkg/server/server.go cmd/flowmanager/run.go
git rm pkg/mcp/server.go pkg/mcp/server_test.go pkg/orchestrator/mcp_notifier.go
git commit -m "feat: удалить MCP-сервер, подключить NotifyAnswer к HTTP-серверу"
```

---

## Task 8: Rewrite integration tests for file-based protocol

**Files:**
- Modify: `pkg/orchestrator/integration_interactive_test.go`
- Modify: `pkg/orchestrator/integration_resume_test.go`

- [x] **Step 1: Rewrite `integration_interactive_test.go`**

Replace the entire file with the file-based equivalents:

```go
package orchestrator_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/mcp"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

// fileQuestionRunner writes a question.json on the Nth RunPlanning call,
// simulating an agent that asked a question and is waiting for an answer.
type fileQuestionRunner struct {
	delegate     executor.Runner
	runDir       string
	stageID      string
	phase        string
	qID          string
	leaveOpenOn  int
	mu           sync.Mutex
	planningRuns int
}

func (r *fileQuestionRunner) RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error {
	r.mu.Lock()
	r.planningRuns++
	run := r.planningRuns
	r.mu.Unlock()

	if run == r.leaveOpenOn {
		stageDir := filepath.Join(r.runDir, r.stageID)
		_ = os.MkdirAll(stageDir, 0755)
		qPath := filepath.Join(stageDir, r.phase+"."+r.qID+".question.json")
		payload, _ := json.Marshal(map[string]any{"id": r.qID, "question": "left open"})
		_ = os.WriteFile(qPath, payload, 0644)
	}
	return r.delegate.RunPlanning(ctx, stageName, prompt, outFile, logFile)
}

func (r *fileQuestionRunner) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	return r.delegate.RunAgent(ctx, agentType, stageName, prompt, logFile)
}

// TestFullDialogCycle verifies the full interactive dialog lifecycle with
// the file-based protocol:
// stage starts → agent writes question.json → polling goroutine detects it →
// awaiting_user_input → user POSTs answer → answer.json written →
// agent bash loop exits → stage done.
func TestFullDialogCycle(t *testing.T) {
	dir := t.TempDir()

	// Mock agent: uses FLOWMANAGER_STAGE_DIR env var, writes question.json,
	// polls for answer.json (max 10s for test), then creates .done.
	agentScript := filepath.Join(dir, "mock-agent.sh")
	script := "#!/bin/bash\n" +
		"STAGE_DIR=\"$FLOWMANAGER_STAGE_DIR\"\n" +
		"if [ -z \"$STAGE_DIR\" ]; then echo 'no FLOWMANAGER_STAGE_DIR' >&2; exit 1; fi\n" +
		"printf '{\"id\":\"q1\",\"question\":\"go ahead?\"}' > \"$STAGE_DIR/implementation.q1.question.json\"\n" +
		"for i in $(seq 1 20); do\n" +
		"  if [ -f \"$STAGE_DIR/implementation.q1.answer.json\" ]; then break; fi\n" +
		"  sleep 0.5\n" +
		"done\n" +
		"if [ ! -f \"$STAGE_DIR/implementation.q1.answer.json\" ]; then echo 'timeout' >&2; exit 1; fi\n" +
		"echo 'done' > \"$STAGE_DIR/.done\"\n" +
		"echo '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"done\"}]}}'\n" +
		"echo '{\"type\":\"result\",\"subtype\":\"success\"}'\n"
	if err := os.WriteFile(agentScript, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{{
		ID: "discovery", Name: "Discovery", Description: "ask user",
		Agents:      []flow.AgentType{flow.AgentImplementation},
		Interactive: true,
		Command:     agentScript,
	}}

	stageDir := filepath.Join(dir, "discovery")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# plan"), 0644); err != nil {
		t.Fatal(err)
	}

	store, err := state.Open(dir, []string{"discovery"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	_ = store.Apply(state.Transition{StageID: "discovery", From: state.StatusPending, To: state.StatusReady, Event: "test_setup"})
	stateFile := filepath.Join(dir, "state.json")

	cfg := config.Default()
	orch := orchestrator.New(orchestrator.Options{
		RunDir:  dir,
		Stages:  stages,
		Store:   store,
		Config:  cfg,
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go func() { _ = orch.Run(ctx) }()

	// Wait for agent to write question.json and polling goroutine to detect it.
	waitForStatus(t, stateFile, "discovery", state.StatusAwaitingUserInput, 10*time.Second)

	// Simulate the HTTP handler: write answer.json (normally done by handleDialogAnswer).
	answerPath := filepath.Join(stageDir, "implementation.q1.answer.json")
	payload, _ := json.Marshal(map[string]any{"id": "q1", "answer": "go for it", "from_options": false})
	tmp := answerPath + ".tmp"
	if err := os.WriteFile(tmp, payload, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, answerPath); err != nil {
		t.Fatal(err)
	}
	// Notify orchestrator so it can transition status.
	if err := orch.NotifyAnswer("discovery", "implementation", "q1", "go for it", false); err != nil {
		t.Fatal(err)
	}

	waitForStatus(t, stateFile, "discovery", state.StatusDone, 10*time.Second)

	// Verify dialog history was populated by polling goroutine.
	dialogPath := filepath.Join(stageDir, "implementation.dialog.jsonl")
	entries, err := mcp.ReadDialog(dialogPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 1 {
		t.Fatalf("expected at least 1 dialog entry, got %d", len(entries))
	}
}

// TestIntegration_PlanningWithOpenQuestionWaits verifies the open-question
// gate: when planning completes but a question.json still has no answer.json,
// the stage must NOT advance to awaiting_approval. It must hold in
// awaiting_user_input until the answer is recorded, then re-run planning.
func TestIntegration_PlanningWithOpenQuestionWaits(t *testing.T) {
	stages := []flow.Stage{{
		ID: "gated", Name: "Gated", Description: "interactive planning",
		Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation},
	}}

	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{"gated"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stateFile := filepath.Join(runDir, "state.json")

	base := mockRunner(t, mockPlanningScript)
	openR := &fileQuestionRunner{
		delegate:    base,
		runDir:      runDir,
		stageID:     "gated",
		phase:       "planning",
		qID:         "q-stuck",
		leaveOpenOn: 1,
	}
	runner := &doneCreatingRunner{delegate: openR}

	cfg := config.Default()
	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		Stages:  stages,
		Store:   store,
		Config:  cfg,
		Prompts: orchestrator.DefaultPrompts(),
		Runner:  runner,
	})

	cancelApprove := autoApprove(orch)
	defer cancelApprove()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = orch.Run(ctx) }()

	waitForStatus(t, stateFile, "gated", state.StatusAwaitingUserInput, 5*time.Second)

	// Stage must stay in awaiting_user_input while question.json has no answer.json.
	time.Sleep(150 * time.Millisecond)
	rs2 := loadStateJSON(t, stateFile)
	if got := rs2.Stages["gated"].Status; got != state.StatusAwaitingUserInput {
		t.Fatalf("stage moved away from awaiting_user_input while question open: got %s", got)
	}

	// Write answer.json and persist dialog answer for history.
	stageDir := filepath.Join(runDir, "gated")
	answerPath := filepath.Join(stageDir, "planning.q-stuck.answer.json")
	payload, _ := json.Marshal(map[string]any{"id": "q-stuck", "answer": "go ahead", "from_options": false})
	if err := os.WriteFile(answerPath, payload, 0644); err != nil {
		t.Fatal(err)
	}
	dialogPath := filepath.Join(stageDir, "planning.dialog.jsonl")
	if err := mcp.AppendAnswer(dialogPath, mcp.Answer{ID: "q-stuck", Answer: "go ahead"}); err != nil {
		t.Fatal(err)
	}

	// Notify orchestrator (agent is not active since RunPlanning returned synchronously).
	orch.PublishCriticalForTest(orchestrator.Event{
		Type:    orchestrator.EventUserAnswered,
		StageID: "gated",
		Data:    map[string]any{"id": "q-stuck", "phase": "planning", "answer": "go ahead"},
	})

	waitForStatus(t, stateFile, "gated", state.StatusDone, 10*time.Second)

	openR.mu.Lock()
	runs := openR.planningRuns
	openR.mu.Unlock()
	if runs < 2 {
		t.Errorf("expected planning to re-run after the answer, got %d runs", runs)
	}
}
```

- [x] **Step 2: Rewrite `TestResumeAfterCrash` in `integration_resume_test.go`**

Replace the entire `TestResumeAfterCrash` function (lines 185-283):

```go
// TestResumeAfterCrash verifies that when flowManager crashes while a stage is
// in awaiting_user_input, and both question.json and answer.json already exist
// on disk, the orchestrator on restart resumes the interactive agent, which
// reads the pre-existing answer.json via its bash loop, and the stage reaches done.
func TestResumeAfterCrash(t *testing.T) {
	dir := t.TempDir()
	stageDir := filepath.Join(dir, "discovery")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("# plan"), 0644); err != nil {
		t.Fatal(err)
	}

	// Pre-populate: agent had already asked q1 and user answered before crash.
	qPath := filepath.Join(stageDir, "implementation.q1.question.json")
	if err := os.WriteFile(qPath, []byte(`{"id":"q1","question":"x?"}`), 0644); err != nil {
		t.Fatal(err)
	}
	aPath := filepath.Join(stageDir, "implementation.q1.answer.json")
	if err := os.WriteFile(aPath, []byte(`{"id":"q1","answer":"after restart","from_options":false}`), 0644); err != nil {
		t.Fatal(err)
	}
	// Also populate dialog.jsonl for history.
	dialogPath := filepath.Join(stageDir, "implementation.dialog.jsonl")
	if err := mcp.AppendQuestion(dialogPath, mcp.Question{ID: "q1", Question: "x?"}); err != nil {
		t.Fatal(err)
	}
	if err := mcp.AppendAnswer(dialogPath, mcp.Answer{ID: "q1", Answer: "after restart"}); err != nil {
		t.Fatal(err)
	}
	// Pre-populate: session.json to simulate prior run.
	if err := os.WriteFile(filepath.Join(stageDir, "implementation.session.json"),
		[]byte(`{"session_id":"test-uuid-resume"}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Agent script: checks FLOWMANAGER_STAGE_DIR, reads answer.json (already exists),
	// creates .done, exits.
	agentScript := filepath.Join(dir, "mock-resume-agent.sh")
	script := "#!/bin/bash\n" +
		"STAGE_DIR=\"$FLOWMANAGER_STAGE_DIR\"\n" +
		"if [ -z \"$STAGE_DIR\" ]; then echo 'no FLOWMANAGER_STAGE_DIR' >&2; exit 1; fi\n" +
		"# answer.json should already exist from before the crash\n" +
		"if [ ! -f \"$STAGE_DIR/implementation.q1.answer.json\" ]; then echo 'answer missing' >&2; exit 1; fi\n" +
		"echo 'done' > \"$STAGE_DIR/.done\"\n" +
		"echo '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"resumed\"}]}}'\n" +
		"echo '{\"type\":\"result\",\"subtype\":\"success\"}'\n"
	if err := os.WriteFile(agentScript, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{{
		ID: "discovery", Name: "Discovery", Description: "ask user",
		Agents:      []flow.AgentType{flow.AgentImplementation},
		Interactive: true,
		Command:     agentScript,
	}}

	store, err := state.Open(dir, []string{"discovery"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	_ = store.Apply(state.Transition{StageID: "discovery", From: state.StatusPending, To: state.StatusAwaitingUserInput, Event: "test_setup"})
	stateFile := filepath.Join(dir, "state.json")

	cfg := config.Default()
	orch := orchestrator.New(orchestrator.Options{
		RunDir:  dir,
		Stages:  stages,
		Store:   store,
		Config:  cfg,
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	go func() { _ = orch.Run(ctx) }()

	// Stage goes from awaiting_user_input → running → done via file-based resume.
	waitForStatus(t, stateFile, "discovery", state.StatusDone, 10*time.Second)

	// Verify dialog was preserved.
	entries, err := mcp.ReadDialog(dialogPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 dialog entry, got %d", len(entries))
	}
	if entries[0].Answer == nil || *entries[0].Answer != "after restart" {
		t.Errorf("answer mismatch: %+v", entries[0])
	}
}
```

- [x] **Step 3: Run integration tests**

```bash
go test ./pkg/orchestrator/ -run "TestFullDialogCycle|TestIntegration_PlanningWithOpenQuestionWaits|TestResumeAfterCrash" -v -timeout 60s
```

Expected: all three tests PASS.

- [x] **Step 4: Commit**

```bash
git add pkg/orchestrator/integration_interactive_test.go \
    pkg/orchestrator/integration_resume_test.go
git commit -m "test: переписать интеграционные тесты диалога под файловый протокол"
```

---

## Task 9: Full verification — build, tests, lint

- [x] **Step 1: Full build**

```bash
go build ./...
```

Expected: no errors.

- [x] **Step 2: Full test suite**

```bash
go test ./... -timeout 120s
```

Expected: all packages PASS. If any test is flaky (especially polling-based tests with tight timeouts), re-run once to confirm.

- [x] **Step 3: Lint**

```bash
golangci-lint run ./...
```

Fix any issues found. Common ones to watch for:
- Unused imports after MCP removal
- `os.Rename` return value ignored (expected, use `_ =`)
- `json.Marshal` error ignored (expected for known-safe data)

- [x] **Step 4: Commit any lint fixes**

```bash
git add -p  # stage only lint fixes
git commit -m "fix: линт после удаления MCP"
```

---

## Notes for implementation

**Agent tracking timing:** `markAgentActive` is called AFTER `sem.acquire()` so it only reflects actively-running agents, not those queued behind the semaphore. `markAgentDone` is called via `defer` before `sem.release()`.

**Polling goroutine and `awaiting_user_input`:** The `pollQuestions` loop calls `o.Trigger(stageID, EvAskUser, ...)` every tick for new questions. The FSM guard prevents double-transitions — calling `EvAskUser` on an already-`awaiting_user_input` stage is a no-op.

**Race between answer and status:** When the user answers while the agent is running:
1. `handleDialogAnswer` writes `answer.json` first
2. Then calls `dialogAnswerFn` → `orch.NotifyAnswer`
3. `NotifyAnswer` sees `isAgentActive=true` → transitions status back to running
4. Agent's bash loop detects `answer.json` (within 30s) → continues

When the user answers after the agent has exited:
1. Same steps 1-2
2. `NotifyAnswer` sees `isAgentActive=false` → publishes `EventUserAnswered` to critical bus
3. `onUserAnswered` fires → stage is still `awaiting_user_input` → restarts agent
4. Agent starts with `--resume`, finds `answer.json` already exists → bash loop exits immediately

**`TestFullDialogCycle` uses a real bash script** that polls for `answer.json` every 0.5s with a 10s timeout. This should pass reliably in CI since the test writes `answer.json` synchronously before the timeout.

**Restart detection in `resumeInteractiveAgent`:** `detectInterruptedPhase` still uses session.json files (unchanged). Interactive stages still write session.json via `loadOrCreateSession`. The only change is that the resumed agent now looks for `answer.json` via bash loop instead of calling the MCP server.
