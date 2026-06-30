package orchestrator_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

	// Mock agent: uses AFM_STAGE_DIR env var, writes question.json,
	// polls for answer.json (max 10s for test), then creates .done.
	agentScript := filepath.Join(dir, "mock-agent.sh")
	script := "#!/bin/bash\n" +
		"STAGE_DIR=\"$AFM_STAGE_DIR\"\n" +
		"if [ -z \"$STAGE_DIR\" ]; then echo 'no AFM_STAGE_DIR' >&2; exit 1; fi\n" +
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

	// Notify via the public API the HTTP handler uses. The planning agent is
	// not active (RunPlanning returned synchronously), so NotifyAnswer takes
	// its restart branch → onUserAnswered re-runs planning.
	if err := orch.NotifyAnswer("gated", "planning", "q-stuck", "go ahead", false); err != nil {
		t.Fatalf("NotifyAnswer: %v", err)
	}

	waitForStatus(t, stateFile, "gated", state.StatusDone, 10*time.Second)

	openR.mu.Lock()
	runs := openR.planningRuns
	openR.mu.Unlock()
	if runs < 2 {
		t.Errorf("expected planning to re-run after the answer, got %d runs", runs)
	}
}

// TestIntegration_InteractiveFailureClearsSession: интерактивная стадия падает
// на non-retryable ошибке — фантомный planning.session.json должен быть удалён,
// иначе retry упадёт с "No conversation found" (afm bug #1.3).
func TestIntegration_InteractiveFailureClearsSession(t *testing.T) {
	dir := t.TempDir()

	failScript := filepath.Join(dir, "fail.sh")
	script := "#!/bin/bash\necho 'fatal: exit status 1' >&2\nexit 1\n"
	if err := os.WriteFile(failScript, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{{
		ID:          "propose",
		Name:        "Propose",
		Description: "interactive planning that fails",
		Agents:      []flow.AgentType{flow.AgentPlanning},
		Interactive: true,
		Command:     failScript,
	}}

	store, err := state.Open(dir, []string{"propose"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stateFile := filepath.Join(dir, "state.json")

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  dir,
		Stages:  stages,
		Store:   store,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	go func() { _ = orch.Run(ctx) }()

	waitForStatus(t, stateFile, "propose", state.StatusFailed, 10*time.Second)

	// loadOrCreateSession успел создать planning.session.json до падения;
	// после фикса non-retryable-ветка обязана его удалить.
	sessionPath := filepath.Join(dir, "propose", "planning.session.json")
	if _, err := os.Stat(sessionPath); !os.IsNotExist(err) {
		t.Errorf("planning.session.json should be removed after non-retryable failure; stat err=%v", err)
	}
}

// TestIntegration_DialogViolationDetected: интерактивный агент пишет
// question.json ВНЕ stageDir — стадия должна перейти в failed с причиной
// "dialog protocol violation" вместо вечного зависания (afm bug-2).
func TestIntegration_DialogViolationDetected(t *testing.T) {
	dir := t.TempDir()

	// «Неправильная» директория, куда агент по ошибке кладёт вопрос.
	wrongDir := filepath.Join(dir, "wrong-stages", "propose")
	if err := os.MkdirAll(wrongDir, 0o755); err != nil {
		t.Fatal(err)
	}
	wrongQuestion := filepath.Join(wrongDir, "planning.q1.question.json")

	// Скрипт эмитит Write tool_use с неверным путём в stream-json, затем ждёт
	// (имитируя зависший bash-loop), пока poller не детектит нарушение.
	scriptPath := filepath.Join(dir, "badagent.sh")
	script := "#!/bin/bash\n" +
		fmt.Sprintf(`echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":%q,"content":"..."}}]}}'`+"\n", wrongQuestion) +
		"sleep 30\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{{
		ID:          "propose",
		Name:        "Propose",
		Description: "interactive planning that violates dialog contract",
		Agents:      []flow.AgentType{flow.AgentPlanning},
		Interactive: true,
		Command:     scriptPath,
	}}

	store, err := state.Open(dir, []string{"propose"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stateFile := filepath.Join(dir, "state.json")

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  dir,
		Stages:  stages,
		Store:   store,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go func() { _ = orch.Run(ctx) }()

	waitForStatus(t, stateFile, "propose", state.StatusFailed, 15*time.Second)

	// Причина отказа зафиксирована в events.jsonl — с упоминанием неверного пути.
	eventsData, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	if !strings.Contains(string(eventsData), "dialog protocol violation") {
		t.Errorf("events.jsonl missing violation reason: %q", string(eventsData))
	}
	if !strings.Contains(string(eventsData), wrongQuestion) {
		t.Errorf("events.jsonl missing offending path %q: %q", wrongQuestion, string(eventsData))
	}
}
