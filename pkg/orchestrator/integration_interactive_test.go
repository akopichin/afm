package orchestrator_test

import (
	"context"
	"net/http/httptest"
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

// openQuestionRunner injects an unanswered ask_user question into the
// planning dialog file on the Nth RunPlanning call, simulating an agent
// that gave up before the user answered.
type openQuestionRunner struct {
	delegate     executor.Runner
	runDir       string
	stageID      string
	qID          string
	leaveOpenOn  int
	mu           sync.Mutex
	planningRuns int
}

func (r *openQuestionRunner) RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error {
	r.mu.Lock()
	r.planningRuns++
	run := r.planningRuns
	r.mu.Unlock()

	if run == r.leaveOpenOn {
		dialogPath := filepath.Join(r.runDir, r.stageID, "planning.dialog.jsonl")
		_ = mcp.AppendQuestion(dialogPath, mcp.Question{ID: r.qID, Question: "left open"})
	}
	return r.delegate.RunPlanning(ctx, stageName, prompt, outFile, logFile)
}

func (r *openQuestionRunner) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	return r.delegate.RunAgent(ctx, agentType, stageName, prompt, logFile)
}

// TestFullDialogCycle verifies the full interactive dialog lifecycle:
// stage starts → agent calls ask_user via MCP → awaiting_user_input →
// user answers → agent completes → stage done.
func TestFullDialogCycle(t *testing.T) {
	dir := t.TempDir()

	agentScript := filepath.Join(dir, "mock-agent.sh")
	script := "#!/bin/bash\n" +
		"MCP=\"\"\n" +
		"while [ $# -gt 0 ]; do\n" +
		"  case \"$1\" in\n" +
		"    --mcp-config) MCP=\"$2\"; shift 2;;\n" +
		"    --session-id|--resume) shift 2;;\n" +
		"    *) shift;;\n" +
		"  esac\n" +
		"done\n" +
		"if [ -z \"$MCP\" ]; then echo 'no mcp config' >&2; exit 1; fi\n" +
		"URL=$(python3 -c \"import json,sys;d=json.load(open(sys.argv[1]));print(d['mcpServers']['flowmanager']['url'])\" \"$MCP\" 2>/dev/null)\n" +
		"if [ -z \"$URL\" ]; then URL=$(grep -o '\"url\": *\"[^\"]*\"' \"$MCP\" | head -1 | sed 's/.*\"url\":[[:space:]]*\"[[:space:]]*//' | sed 's/\".*//'); fi\n" +
		"STAGE_DIR=$(dirname \"$MCP\")\n" +
		"curl -sf -X POST \"$URL\" -H 'Content-Type: application/json' -d '{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{}}' >/dev/null\n" +
		"curl -sf -X POST \"$URL\" -H 'Content-Type: application/json' -d '{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"ask_user\",\"arguments\":{\"id\":\"q1\",\"question\":\"go ahead?\"}}}' >/dev/null\n" +
		"echo 'done' > \"$STAGE_DIR/.done\"\n" +
		"echo '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"done\"}]}}'\n" +
		"echo '{\"type\":\"result\",\"subtype\":\"success\"}'\n"
	if err := os.WriteFile(agentScript, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{
		{
			ID: "discovery", Name: "Discovery", Description: "ask user",
			Agents:      []flow.AgentType{flow.AgentImplementation},
			Interactive: true,
			Command:     agentScript,
		},
	}

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

	mcpSrv := mcp.NewServer(dir, orchestrator.NewMcpNotifier(orch))
	srv := httptest.NewServer(mcpSrv)
	defer srv.Close()
	orch.SetDashboardURL(srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	go func() { _ = orch.Run(ctx) }()

	waitForStatus(t, stateFile, "discovery", state.StatusAwaitingUserInput, 5*time.Second)

	dialogPath := filepath.Join(stageDir, "implementation.dialog.jsonl")
	if err := mcp.AppendAnswer(dialogPath, mcp.Answer{ID: "q1", Answer: "go for it", FromOptions: false}); err != nil {
		t.Fatal(err)
	}
	if err := mcpSrv.NotifyAnswer("discovery", "implementation", "q1", "go for it", false); err != nil {
		t.Fatal(err)
	}

	waitForStatus(t, stateFile, "discovery", state.StatusDone, 5*time.Second)

	entries, err := mcp.ReadDialog(dialogPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 dialog entry, got %d", len(entries))
	}
	if entries[0].Answer == nil || *entries[0].Answer != "go for it" {
		t.Errorf("answer mismatch: %+v", entries[0])
	}
}

// TestIntegration_PlanningWithOpenQuestionWaits verifies the open-question
// gate: when planning completes but the dialog file still has an unanswered
// ask_user question, the stage must NOT advance to awaiting_approval. It
// must hold in awaiting_user_input until an answer is recorded, then
// re-run planning and proceed normally.
func TestIntegration_PlanningWithOpenQuestionWaits(t *testing.T) {
	stages := []flow.Stage{
		{
			ID: "gated", Name: "Gated", Description: "interactive planning",
			Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation},
		},
	}

	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{"gated"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stateFile := filepath.Join(runDir, "state.json")

	base := mockRunner(t, mockPlanningScript)
	openR := &openQuestionRunner{
		delegate:    base,
		runDir:      runDir,
		stageID:     "gated",
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

	// Make sure the stage does NOT jump straight to awaiting_approval or done
	// while the question is still open.
	time.Sleep(150 * time.Millisecond)
	rs2 := loadStateJSON(t, stateFile)
	if got := rs2.Stages["gated"].Status; got != state.StatusAwaitingUserInput {
		t.Fatalf("stage moved away from awaiting_user_input while question open: got %s", got)
	}

	// Persist the user's answer and notify the orchestrator. We bypass the
	// MCP server here because the mock runner is synchronous and never
	// connected to one.
	dialogPath := filepath.Join(runDir, "gated", "planning.dialog.jsonl")
	if err := mcp.AppendAnswer(dialogPath, mcp.Answer{ID: "q-stuck", Answer: "go ahead"}); err != nil {
		t.Fatal(err)
	}
	orch.PublishCriticalForTest(orchestrator.Event{
		Type:    orchestrator.EventUserAnswered,
		StageID: "gated",
		Data: map[string]any{
			"id":     "q-stuck",
			"phase":  "planning",
			"answer": "go ahead",
		},
	})

	waitForStatus(t, stateFile, "gated", state.StatusDone, 10*time.Second)

	openR.mu.Lock()
	runs := openR.planningRuns
	openR.mu.Unlock()
	if runs < 2 {
		t.Errorf("expected planning to re-run after the answer, got %d runs", runs)
	}
}
