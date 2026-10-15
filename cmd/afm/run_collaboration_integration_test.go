package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/server"
	"github.com/akopichin/afm/pkg/state"
)

// TestAssistants_EndToEnd_AttachRunCompleteReadBack is the integration test the Stage 3 wiring gap asked
// for: collaboration enabled with a profile pointing at a scripted command, a task attached through the REAL
// HTTP layer (not a direct orchestrator.Attach call, and not an injected test seam), driven to "completed" by
// a real subprocess, and read back through the REAL HTTP read-side — proving the whole chain
// (cmd/afm's startCollaboration -> orchestrator.Options.AssistantsEnabled/Registry ->
// collaborationActions/collaborationReader -> server.Config.AssistantActions/AssistantsReader -> the mux
// routes in pkg/server/assistants.go) is actually wired together, not just unit-tested in isolation per
// package.
//
// The stage's OWN agent is driven by an injected executor.Runner (fakeAutoRunner) so the test does not need a
// second real subprocess for the stage itself — only the HELPER TASK goes through a real executor.Executor
// subprocess, because that is the one hop (profile Command -> config.HelperCommandRejection ->
// assistants.BuildRegistry's availability gate -> actual exec) this test exists to prove end to end. On the
// host (outside any container) that gate only accepts the literal command name "codex-as-claude" (see
// config.HelperCommandRejection), so the fake script is installed under exactly that name on PATH.
func TestAssistants_EndToEnd_AttachRunCompleteReadBack(t *testing.T) {
	binDir := writeFakeCodexHelper(t, "pass", "Looks fine from the fake reviewer.")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	runDir := t.TempDir()
	const stageID = "work"
	stages := []flow.Stage{{ID: stageID, Name: "Work", Agents: []flow.AgentType{flow.AgentAuto}}}

	store, err := state.Open(runDir, []string{stageID})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	enabled := true
	cfg := config.Default()
	cfg.Collaboration = config.CollaborationConfig{
		Enabled: &enabled,
		Profiles: map[string]config.ProfileConfig{
			"reviewer": {Title: "Reviewer", Command: "codex-as-claude", Instructions: "Review the work."},
		},
	}
	f := &flow.Flow{Name: "e2e"}

	// This is the exact production entry point (cmd/afm/run.go calls it the same way) — not a hand-rolled
	// substitute for it.
	collab := startCollaboration(store, cfg, f)
	if !collab.policy.Enabled || collab.registry == nil {
		t.Fatalf("collaboration runtime did not come up enabled with a registry: %+v", collab.policy)
	}

	runner := &fakeAutoRunner{acquired: make(chan string, 1), release: make(chan struct{})}
	orch := orchestrator.New(orchestrator.Options{
		RunDir:             runDir,
		Stages:             stages,
		Store:              store,
		Config:             cfg,
		Prompts:            orchestrator.DefaultPrompts(),
		Runner:             runner,
		AssistantsEnabled:  collab.policy.Enabled,
		AssistantsRegistry: collab.registry,
	})

	// Same composition cmd/afm/run_dashboard.go's startDashboard uses — reproduced here (rather than calling
	// startDashboard itself) only because that helper gates on a configured positive port and always binds
	// the configured port; the test needs an ephemeral one.
	srv := server.New(server.Config{
		Port:             0,
		RunDir:           runDir,
		Store:            store,
		UIBus:            orch.UIBus(),
		Actions:          orch,
		Secondary:        orch,
		FlowActions:      orch,
		Stages:           dashboardStageConfig(stages),
		AssistantActions: collab.actions(orch),
		AssistantsReader: collab.reader(orch),
	})
	addr, err := srv.Start()
	if err != nil {
		t.Fatalf("srv.Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	baseURL := "http://" + addr

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- orch.Run(ctx) }()

	// Wait for the stage to actually be running (fakeAutoRunner blocks inside RunAgent) so Attach hits the
	// "parent already active" branch and the task is queued/spawned immediately, same as a real live run.
	select {
	case <-runner.acquired:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the stage to start")
	}

	// --- Attach a task via the REAL HTTP path. ---
	attachBody, _ := json.Marshal(map[string]string{
		"request_id": "req-1", "profile_id": "reviewer", "title": "Sanity check",
		"instruction": "Please take a look.",
	})
	taskID := postAssistant(t, baseURL, "/api/stages/"+stageID+"/assistants", attachBody)

	// --- Poll the REAL HTTP read-side until the task reaches a terminal status. ---
	deadline := time.Now().Add(15 * time.Second)
	var finalStatus string
	var finalReadonlyEnforcement string
	for time.Now().Before(deadline) {
		var out struct {
			Tasks []server.AssistantTaskView `json:"tasks"`
		}
		fetchJSON(t, baseURL+"/api/stages/"+stageID+"/assistants", &out)
		for _, tv := range out.Tasks {
			if tv.ID == taskID {
				finalStatus = tv.Status
				finalReadonlyEnforcement = tv.ReadonlyEnforcement
			}
		}
		if finalStatus == string(assistants.StatusCompleted) || finalStatus == string(assistants.StatusFailed) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if finalStatus != string(assistants.StatusCompleted) {
		t.Fatalf("task final status (via HTTP) = %q, want %q", finalStatus, assistants.StatusCompleted)
	}

	// The dashboard-facing AssistantTaskView must carry the SAME read-only-enforcement verdict the registry's
	// availability check reported for "codex-as-claude" at admission time — frozen into ProfileSnapshot by
	// orchestrator.Attach (pkg/orchestrator/assistants.go), surfaced here by collaborationReader.TaskTree
	// (cmd/afm/run_collaboration.go). Computed from the SAME config.HelperReadonlyEnforcement the production
	// `available` closure in startCollaboration calls, rather than hardcoding "os"/"none", so this assertion
	// stays correct whether the test happens to run on a bare host or inside a container (config.InContainer()
	// changes the answer — see pkg/config/verify_resolve.go).
	wantEnforcement := config.HelperReadonlyEnforcement("codex-as-claude", cfg)
	if wantEnforcement == "" {
		t.Fatal("test setup: config.HelperReadonlyEnforcement(\"codex-as-claude\", cfg) returned empty — test cannot assert a meaningful expectation")
	}
	if finalReadonlyEnforcement == "" {
		t.Fatal("AssistantTaskView.ReadonlyEnforcement (via HTTP) is empty — the audit trail's read-only guarantee was never wired through")
	}
	if finalReadonlyEnforcement != wantEnforcement {
		t.Fatalf("AssistantTaskView.ReadonlyEnforcement (via HTTP) = %q, want %q", finalReadonlyEnforcement, wantEnforcement)
	}

	// --- The durable per-task event history is also readable back over HTTP. ---
	var evOut struct {
		Events []server.AssistantEventView `json:"events"`
	}
	fetchJSON(t, baseURL+"/api/assistants/"+taskID+"/events", &evOut)
	var sawCreated, sawCompleted bool
	var sawResult bool
	var resultVerdict, resultSummary string
	for _, e := range evOut.Events {
		switch e.Type {
		case "task_created":
			sawCreated = true
		case "task_status_changed":
			if m, ok := e.Data.(map[string]any); ok {
				if s, _ := m["status"].(string); s == string(assistants.StatusCompleted) {
					sawCompleted = true
				}
			}
		case "task_result":
			sawResult = true
			if m, ok := e.Data.(map[string]any); ok {
				resultVerdict, _ = m["verdict"].(string)
				resultSummary, _ = m["summary"].(string)
			}
		default:
		}
	}
	if !sawCreated {
		t.Error("task event history (via HTTP) is missing task_created")
	}
	if !sawCompleted {
		t.Error("task event history (via HTTP) is missing task_status_changed -> completed")
	}
	// This is the gap the fix closes: the decoded verdict/summary a real subprocess produced must be visible
	// through the REAL HTTP read path (not just persisted to disk) — the frontend's AssistantTaskView already
	// scans every event's `data` for exactly this shape and was, until this fix, a no-op against the real
	// backend because no such event was ever emitted.
	if !sawResult {
		t.Fatal("task event history (via HTTP) is missing a task_result event")
	}
	if resultVerdict != string(assistants.VerdictPass) {
		t.Errorf("task_result event verdict (via HTTP) = %q, want %q", resultVerdict, assistants.VerdictPass)
	}
	if resultSummary != "Looks fine from the fake reviewer." {
		t.Errorf("task_result event summary (via HTTP) = %q, want %q", resultSummary, "Looks fine from the fake reviewer.")
	}

	// Let the stage's own agent finish so orch.Run can return cleanly, and confirm the task's outcome never
	// touched the stage's own FSM status either way (Этап 3's acceptance criterion).
	close(runner.release)
	select {
	case runErr := <-runDone:
		if runErr != nil {
			t.Fatalf("orch.Run: %v", runErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for orch.Run to return")
	}
	if got := store.Get(stageID); got != state.StatusDone {
		t.Fatalf("stage status = %s, want done (the advisory task must not affect it)", got)
	}

	// The decoded verdict/summary a real subprocess produced is durably persisted to disk — proof the helper
	// actually ran for real, not just that the status flipped. The dashboard HTTP read-side does not surface
	// this content yet (only status/reason — see server.AssistantTaskView); that is explicitly T3.9/T3.10
	// (frontend) scope, not this wiring task.
	resultPath := filepath.Join(runDir, "assistants", taskID, "result.json")
	raw, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read persisted result.json: %v", err)
	}
	var result assistants.Result
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode persisted result.json: %v", err)
	}
	if result.Verdict != assistants.VerdictPass {
		t.Errorf("persisted result verdict = %q, want %q", result.Verdict, assistants.VerdictPass)
	}
}

// fakeAutoRunner is an executor.Runner for a single flow.AgentAuto stage: it blocks in RunAgent until the
// test signals release, then writes execution_summary.md (the file-probe stagefiles.CheckAutonomousCompletion
// requires) so the stage can complete normally — modeled on pkg/orchestrator/maxparallel_test.go's
// blockingAutoRunner, trimmed to the single-stage case this test needs.
type fakeAutoRunner struct {
	acquired chan string
	release  chan struct{}
}

func (r *fakeAutoRunner) RunPlanning(_ context.Context, _, _, outFile, _ string) error {
	// flow.AgentAuto stages never call this — defensive only, so a wiring mistake fails on a clear write
	// error instead of a nil-pointer panic.
	return os.WriteFile(outFile, []byte("## Tasks\n- x\n"), 0o644)
}

func (r *fakeAutoRunner) RunJSONQuery(_ context.Context, _ string) ([]byte, error) { return nil, nil }

func (r *fakeAutoRunner) RunAgent(ctx context.Context, _, _, _, logFile string) error {
	stageDir := filepath.Dir(logFile)
	select {
	case r.acquired <- stageDir:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-r.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return os.WriteFile(filepath.Join(stageDir, "execution_summary.md"), []byte("# Summary\n\nDone.\n"), 0o644)
}

// writeFakeCodexHelper writes a script named exactly "codex-as-claude" (the literal name
// config.HelperCommandRejection trusts on the host, see IsCodexCommand) that emits one final assistant
// text block containing a valid assistants-result envelope, followed by a stream-json success line — the
// same shape pkg/executor's own verify/helper tests use for the real agent protocol.
func writeFakeCodexHelper(t *testing.T, verdict, summary string) string {
	t.Helper()
	resultJSON, err := json.Marshal(map[string]any{
		"schema_version": 1, "kind": "result", "verdict": verdict, "summary": summary,
	})
	if err != nil {
		t.Fatal(err)
	}
	assistantLine, err := json.Marshal(map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []map[string]any{{"type": "text", "text": string(resultJSON)}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncat <<'EOF'\n" + string(assistantLine) + "\nEOF\n" +
		`echo '{"type":"result","subtype":"success"}'` + "\n"
	dir := t.TempDir()
	p := filepath.Join(dir, "codex-as-claude")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// postAssistant POSTs body to path (setting Origin so the handler's sameOriginOK same-origin check passes —
// a Go http.Client does not set that header on its own) and returns the created task id.
func postAssistant(t *testing.T, baseURL, path string, body []byte) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, baseURL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", baseURL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s status = %d, body=%s", path, resp.StatusCode, b)
	}
	var out struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode attach response %s: %v", b, err)
	}
	if out.TaskID == "" {
		t.Fatal("attach response carried an empty task_id")
	}
	return out.TaskID
}

// fetchJSON GETs url and decodes the JSON body into out.
func fetchJSON(t *testing.T, url string, out any) {
	t.Helper()
	resp, err := http.Get(url) //nolint:gosec // test-only, url is built from a loopback test server address
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, body=%s", url, resp.StatusCode, b)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("decode %s: %v, body=%s", url, err, b)
	}
}
