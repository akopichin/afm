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

func (r *fileQuestionRunner) RunJSONQuery(ctx context.Context, prompt string) ([]byte, error) {
	return r.delegate.RunJSONQuery(ctx, prompt)
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
	_ = store.Apply(&state.Transition{StageID: "discovery", From: state.StatusPending, To: state.StatusReady, Event: "test_setup"})
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
	runOrchestratorAsync(ctx, t, orch, cancel)

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
	runOrchestratorAsync(ctx, t, orch, cancel)

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
	runOrchestratorAsync(ctx, t, orch, cancel)

	waitForStatus(t, stateFile, "propose", state.StatusFailed, 10*time.Second)

	// loadOrCreateSession успел создать planning.session.json до падения;
	// после фикса non-retryable-ветка обязана его удалить.
	sessionPath := filepath.Join(dir, "propose", "planning.session.json")
	if _, err := os.Stat(sessionPath); !os.IsNotExist(err) {
		t.Errorf("planning.session.json should be removed after non-retryable failure; stat err=%v", err)
	}
}

// TestIntegration_MisplacedQuestionRelocated: интерактивный агент пишет
// question.json ВНЕ stageDir (баг GLM-4.7: путь из CWD вместо $AFM_STAGE_DIR).
// Стадия не должна зависнуть навсегда: оркестратор relocate'ит misplaced-файл в
// правильное место, переводит стадию в awaiting_user_input, а по неверному пути
// создаёт symlink на answer.json (чтобы агентский polling-loop нашёл ответ).
func TestIntegration_MisplacedQuestionRelocated(t *testing.T) {
	dir := t.TempDir()
	rootDir := filepath.Join(dir, "project")
	if err := os.MkdirAll(rootDir, 0755); err != nil {
		t.Fatal(err)
	}

	// «Неправильная» директория: агент по CWD-багу пишет вопрос прямо в
	// верхний уровень root_dir (см. дизайн: bare-relative-write паттерн),
	// а не в $AFM_STAGE_DIR.
	wrongQuestion := filepath.Join(rootDir, "planning.q1.question.json")
	wrongAnswer := filepath.Join(rootDir, "planning.q1.answer.json")

	scriptPath := filepath.Join(dir, "misplacedagent.sh")
	script := "#!/bin/bash\n" +
		fmt.Sprintf(`echo '{"id":"q1","question":"where?","options":["a","b"]}' > %q`+"\n", wrongQuestion) +
		"sleep 30\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{{
		ID:          "propose",
		Name:        "Propose",
		Description: "interactive planning that writes question outside stageDir",
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
		RootDir: rootDir,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	runOrchestratorAsync(ctx, t, orch, cancel)

	waitForStatus(t, stateFile, "propose", state.StatusAwaitingUserInput, 15*time.Second)

	stageDir := filepath.Join(dir, "propose")

	if _, err := os.Stat(filepath.Join(stageDir, "planning.q1.question.json")); err != nil {
		t.Errorf("relocated question missing in stageDir: %v", err)
	}
	link, err := os.Readlink(wrongAnswer)
	if err != nil {
		t.Errorf("expected answer symlink at %s: %v", wrongAnswer, err)
	} else if link != filepath.Join(stageDir, "planning.q1.answer.json") {
		t.Errorf("answer symlink points to %q, want %q", link, filepath.Join(stageDir, "planning.q1.answer.json"))
	}
}

// TestIntegration_BareQuestionFilenameNormalized: агент пишет вопрос вообще
// без префикса ("q1.question.json" вместо "planning.q1.question.json") прямо
// в root_dir. relocateMisplacedQuestions должен считать всё имя целиком id и
// подставить активную фазу (planning — единственный <phase>.jsonl, который
// успел появиться на диске к этому моменту).
func TestIntegration_BareQuestionFilenameNormalized(t *testing.T) {
	dir := t.TempDir()
	rootDir := filepath.Join(dir, "project")
	if err := os.MkdirAll(rootDir, 0755); err != nil {
		t.Fatal(err)
	}

	wrongQuestion := filepath.Join(rootDir, "q1.question.json")
	wrongAnswer := filepath.Join(rootDir, "q1.answer.json")

	scriptPath := filepath.Join(dir, "bareagent.sh")
	script := "#!/bin/bash\n" +
		fmt.Sprintf(`echo '{"id":"q1","question":"where?","options":["a","b"]}' > %q`+"\n", wrongQuestion) +
		"sleep 30\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{{
		ID:          "propose",
		Name:        "Propose",
		Description: "interactive planning that writes a bare-named question file",
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
		RootDir: rootDir,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	runOrchestratorAsync(ctx, t, orch, cancel)

	waitForStatus(t, stateFile, "propose", state.StatusAwaitingUserInput, 15*time.Second)

	stageDir := filepath.Join(dir, "propose")
	if _, err := os.Stat(filepath.Join(stageDir, "planning.q1.question.json")); err != nil {
		t.Errorf("bare-named question was not normalized into stageDir: %v", err)
	}
	if _, err := os.Readlink(wrongAnswer); err != nil {
		t.Errorf("expected answer symlink at %s: %v", wrongAnswer, err)
	}
}

// TestIntegration_MisprefixedQuestionNormalized: интерактивный агент пишет
// question.json ВНУТРЬ stageDir, но с префиксом = id стадии ("commit-changes")
// вместо канонической фазы ("planning"). Poller такой префикс не распознаёт
// (FindUnansweredQuestions матчит только planning/implementation/review/
// autonomous_execution) → без нормализации вопрос невидим в UI и стадия зависает.
// Оркестратор должен нормализовать файл к каноническому имени фазы и создать
// symlink на answer.json по неверному (agent-poll'ящемуся) пути.
func TestIntegration_MisprefixedQuestionNormalized(t *testing.T) {
	dir := t.TempDir()

	// Скрипт пишет вопрос ВНУТРЬ $AFM_STAGE_DIR, но с префиксом id стадии,
	// затем спит, имитируя зависший bash-polling-loop.
	scriptPath := filepath.Join(dir, "misprefixedagent.sh")
	script := "#!/bin/bash\n" +
		`q="$AFM_STAGE_DIR/commit-changes.q1.question.json"` + "\n" +
		`echo '{"id":"q1","question":"ready?","options":["a","b"]}' > "$q"` + "\n" +
		"sleep 30\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{{
		ID:          "commit-changes",
		Name:        "Commit changes",
		Description: "interactive planning that writes question with stage-id prefix",
		Agents:      []flow.AgentType{flow.AgentPlanning},
		Interactive: true,
		Command:     scriptPath,
	}}

	store, err := state.Open(dir, []string{"commit-changes"})
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
	runOrchestratorAsync(ctx, t, orch, cancel)

	// Mis-prefixed вопрос нормализуется → poller находит его → EvAskUser.
	waitForStatus(t, stateFile, "commit-changes", state.StatusAwaitingUserInput, 15*time.Second)

	stageDir := filepath.Join(dir, "commit-changes")

	// Файл нормализован к каноническому имени фазы — вопрос виден в UI.
	if _, err := os.Stat(filepath.Join(stageDir, "planning.q1.question.json")); err != nil {
		t.Errorf("normalized question missing in stageDir: %v", err)
	}
	// По неверному (agent-poll'ящемуся) пути создан dangling-symlink на будущий
	// канонический answer.json — bash-polling-loop агента найдёт ответ.
	wrongAnswer := filepath.Join(stageDir, "commit-changes.q1.answer.json")
	link, err := os.Readlink(wrongAnswer)
	if err != nil {
		t.Errorf("expected answer symlink at %s: %v", wrongAnswer, err)
	} else if link != filepath.Join(stageDir, "planning.q1.answer.json") {
		t.Errorf("answer symlink points to %q, want %q", link, filepath.Join(stageDir, "planning.q1.answer.json"))
	}
}

// TestIntegration_BrokenQuestionStillSurfaces воспроизводит реальный инцидент:
// агент (glm-5.2, autonomous_execution) записал question.json с пропущенной
// открывающей кавычкой у ключа "options" — `...,options":[...]` вместо
// `...,"options":[...]`. До фикса FindUnansweredQuestions делал continue на
// любой ошибке json.Unmarshal без единого лога — файл вопроса пропадал из
// вида поллера, стадия висела в "running" 12+ минут без единого следа в UI.
// Теперь jsonrepair чинит такой класс ошибок и стадия почти сразу доходит до
// awaiting_user_input с корректно распарсенными question/options.
func TestIntegration_BrokenQuestionStillSurfaces(t *testing.T) {
	dir := t.TempDir()

	scriptPath := filepath.Join(dir, "brokenagent.sh")
	script := "#!/bin/bash\n" +
		`printf '{"id":"q10","question":"...последний cell Phase 7.",options":["Утверждаю","Правки"]}' > "$AFM_STAGE_DIR/planning.q10.question.json"` + "\n" +
		"sleep 30\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{{
		ID:          "brainstorm",
		Name:        "Brainstorm",
		Description: "interactive planning that writes a question.json with a missing key quote",
		Agents:      []flow.AgentType{flow.AgentPlanning},
		Interactive: true,
		Command:     scriptPath,
	}}

	store, err := state.Open(dir, []string{"brainstorm"})
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
	runOrchestratorAsync(ctx, t, orch, cancel)

	waitForStatus(t, stateFile, "brainstorm", state.StatusAwaitingUserInput, 10*time.Second)

	stageDir := filepath.Join(dir, "brainstorm")
	questions, err := mcp.FindUnansweredQuestions(stageDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(questions) != 1 {
		t.Fatalf("want 1 surfaced question, got %d: %+v", len(questions), questions)
	}
	q := questions[0]
	if q.ID != "q10" || q.Question != "...последний cell Phase 7." {
		t.Fatalf("question was repaired incorrectly: %+v", q)
	}
	if want := []string{"Утверждаю", "Правки"}; len(q.Options) != 2 || q.Options[0] != want[0] || q.Options[1] != want[1] {
		t.Fatalf("options mismatch after repair: got %v, want %v", q.Options, want)
	}
}

// TestIntegration_UnrepairableQuestionFallsBackToStub покрывает last-resort
// fallback: агент пишет question.json настолько сломанный, что даже jsonrepair
// не может его починить, и запускаемые afm отдельные fix-агенты тоже не могут
// его восстановить. Стадия всё равно должна дойти до awaiting_user_input —
// после исчерпания maxJSONFixAttempts fix-агентов afm показывает вопрос-заглушку
// без вариантов ответа (свободный текст) вместо вечного зависания в "running".
//
// Один и тот же скрипт играет обе роли: как ОСНОВНОЙ агент стадии (afm
// выставляет ему AFM_STAGE_DIR) он пишет битый question.json и спит, оставаясь
// живым как настоящий агент в ожидании ответа; как FIX-агент (afm намеренно
// запускает его БЕЗ AFM_STAGE_DIR — см. runJSONFixAgent) он не в состоянии
// ничего починить и завершается сразу — быстрая, реалистичная модель
// «отдельный агент тоже не смог».
func TestIntegration_UnrepairableQuestionFallsBackToStub(t *testing.T) {
	dir := t.TempDir()

	scriptPath := filepath.Join(dir, "garbageagent.sh")
	script := "#!/bin/bash\n" +
		"if [ -z \"$AFM_STAGE_DIR\" ]; then exit 1; fi\n" +
		`printf 'not json at all {{{' > "$AFM_STAGE_DIR/planning.q1.question.json"` + "\n" +
		"sleep 30\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{{
		ID:          "propose",
		Name:        "Propose",
		Description: "interactive planning that writes an unrepairable question.json",
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
	runOrchestratorAsync(ctx, t, orch, cancel)

	// grace tick + maxJSONFixAttempts fast-failing fix agents, all inside 15s.
	waitForStatus(t, stateFile, "propose", state.StatusAwaitingUserInput, 15*time.Second)

	stageDir := filepath.Join(dir, "propose")
	questions, err := mcp.FindUnansweredQuestions(stageDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(questions) != 1 {
		t.Fatalf("want 1 fallback stub question, got %d: %+v", len(questions), questions)
	}
	q := questions[0]
	if q.ID != "q1" {
		t.Fatalf("fallback stub must keep the id from the filename, got %q", q.ID)
	}
	if q.Question == "" || len(q.Options) != 0 || !q.AllowCustom {
		t.Fatalf("unexpected fallback stub (want no options, free text only): %+v", q)
	}
}

// TestIntegration_InteractiveOpenQuestionHoldsOnAgentExit воспроизводит afm bug:
// интерактивный planning-агент задал вопрос и ВЫШЕЛ (claude завершился), не
// дождавшись ответа пользователя и не написав plan.md. Раньше такое завершение
// приводило к "missing artifact or incomplete" — стадия падала, пока агент
// легитимно ждал ответа. После фикса стадия удерживается в awaiting_user_input,
// а когда пользователь отвечает — агент перезапускается и дописывает план.
func TestIntegration_InteractiveOpenQuestionHoldsOnAgentExit(t *testing.T) {
	dir := t.TempDir()

	// Агент: пока нет answer.json — пишет question.json и сразу выходит (без
	// plan.md), имитируя преждевременный exit в ожидании ответа. Когда ответ
	// уже есть — пишет валидный план с обязательными секциями.
	agentScript := filepath.Join(dir, "mock-agent.sh")
	script := "#!/bin/bash\n" +
		"STAGE=\"$AFM_STAGE_DIR\"\n" +
		"A=\"$STAGE/planning.q1.answer.json\"\n" +
		"if [ ! -f \"$A\" ]; then\n" +
		"  printf '{\"id\":\"q1\",\"question\":\"decide?\"}' > \"$STAGE/planning.q1.question.json\"\n" +
		"  echo '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"waiting for q1\"}]}}'\n" +
		"  echo '{\"type\":\"result\",\"subtype\":\"success\"}'\n" +
		"  exit 0\n" +
		"fi\n" +
		"echo '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"## Tasks\\n\\n- [ ] do it\\n\\n## Assumptions\\n\\n- none\\n\\n## Acceptance Criteria\\n\\n- [ ] works\\n\"}]}}'\n" +
		"echo '{\"type\":\"result\",\"subtype\":\"success\"}'\n"
	if err := os.WriteFile(agentScript, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{{
		ID:          "propose",
		Name:        "Propose",
		Description: "interactive planning that exits while waiting",
		Agents:      []flow.AgentType{flow.AgentPlanning},
		Interactive: true,
		Command:     agentScript,
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

	cancelApprove := autoApprove(orch)
	defer cancelApprove()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	runOrchestratorAsync(ctx, t, orch, cancel)

	// Агент задал вопрос и вышел → poller перевёл стадию в awaiting_user_input.
	waitForStatus(t, stateFile, "propose", state.StatusAwaitingUserInput, 5*time.Second)

	// Ключевая проверка фикса: стадия не должна падать от того, что агент
	// завершился без артефакта. До фикса здесь уже лежал бы StatusFailed
	// ("missing artifact or incomplete").
	time.Sleep(500 * time.Millisecond)
	if got := loadStateJSON(t, stateFile).Stages["propose"].Status; got == state.StatusFailed {
		t.Fatal("interactive stage failed while agent waited for user reply; should hold awaiting_user_input")
	}

	// Пользователь отвечает — агент перезапускается и дописывает план.
	stageDir := filepath.Join(dir, "propose")
	answerPath := filepath.Join(stageDir, "planning.q1.answer.json")
	payload, _ := json.Marshal(map[string]any{"id": "q1", "answer": "go ahead", "from_options": false})
	tmp := answerPath + ".tmp"
	if err := os.WriteFile(tmp, payload, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, answerPath); err != nil {
		t.Fatal(err)
	}
	dialogPath := filepath.Join(stageDir, "planning.dialog.jsonl")
	if err := mcp.AppendAnswer(dialogPath, mcp.Answer{ID: "q1", Answer: "go ahead"}); err != nil {
		t.Fatal(err)
	}
	if err := orch.NotifyAnswer("propose", "planning", "q1", "go ahead", false); err != nil {
		t.Fatalf("NotifyAnswer: %v", err)
	}

	// План дописан, approved (autoApprove), planning-only стадия → done.
	waitForStatus(t, stateFile, "propose", state.StatusDone, 10*time.Second)
}

// TestIntegration_StaleAnsweredQuestionNotReopened воспроизводит баг:
// relocateMisplacedQuestions пере-открывал уже отвеченный вопрос ПРЕДЫДУЩЕЙ
// фазы под именем ТЕКУЩЕЙ активной фазы. У многофазной interactive-стадии
// (planning → implementation) planning.q1.question.json/planning.q1.answer.json
// остаются лежать в stageDir навсегда (ничто их не удаляет). Когда
// implementation.jsonl становится самым свежим <phase>.jsonl,
// activeDialogPhase() переключается на "implementation" — но
// collectQuestionFiles всё ещё находит СТАРЫЙ planning.q1.question.json как
// кандидата. Без проверки на уже существующий answer.json под его СОБСТВЕННЫМ
// именем normalizeMisplacedQuestion копировал его содержимое в свежесозданный
// implementation.q1.question.json, спонтанно создавая "вопрос", на который
// никто не отвечает — стадия зависала в awaiting_user_input.
func TestIntegration_StaleAnsweredQuestionNotReopened(t *testing.T) {
	dir := t.TempDir()

	// Interactive-стадии игнорируют инъектированный Runner — runnerFor всегда
	// строит executor.New(stage.Command) (см. CLAUDE.md), поэтому ОДИН bash-
	// скрипт обслуживает обе фазы. executor.RunAgent создаёт implementation.log
	// (progress.NewLogger) ДО спавна подпроцесса, так что его наличие в stageDir —
	// надёжный признак того, что текущий запуск — implementation, а не planning.
	scriptPath := filepath.Join(dir, "twophase.sh")
	script := "#!/bin/bash\n" +
		`STAGE="$AFM_STAGE_DIR"` + "\n" +
		`if [ -f "$STAGE/implementation.log" ]; then` + "\n" +
		`  sleep 3` + "\n" +
		`  echo done > "$STAGE/.done"` + "\n" +
		`  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"implementation done"}]}}'` + "\n" +
		`  echo '{"type":"result","subtype":"success"}'` + "\n" +
		`  exit 0` + "\n" +
		`fi` + "\n" +
		`A="$STAGE/planning.q1.answer.json"` + "\n" +
		`if [ ! -f "$A" ]; then` + "\n" +
		`  printf '{"id":"q1","question":"proceed?"}' > "$STAGE/planning.q1.question.json"` + "\n" +
		`  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"waiting for q1"}]}}'` + "\n" +
		`  echo '{"type":"result","subtype":"success"}'` + "\n" +
		`  exit 0` + "\n" +
		`fi` + "\n" +
		`echo '{"type":"assistant","message":{"content":[{"type":"text","text":"## Tasks\n\n- [ ] do it\n\n## Assumptions\n\n- none\n\n## Acceptance Criteria\n\n- [ ] works\n"}]}}'` + "\n" +
		`echo '{"type":"result","subtype":"success"}'` + "\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{{
		ID:          "propose",
		Name:        "Propose",
		Description: "interactive two-phase stage with a leftover answered planning question",
		Agents:      []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation},
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

	cancelApprove := autoApprove(orch)
	defer cancelApprove()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	runOrchestratorAsync(ctx, t, orch, cancel)

	// Планирование задаёт вопрос и ждёт ответа.
	waitForStatus(t, stateFile, "propose", state.StatusAwaitingUserInput, 10*time.Second)

	stageDir := filepath.Join(dir, "propose")
	answerPath := filepath.Join(stageDir, "planning.q1.answer.json")
	payload, _ := json.Marshal(map[string]any{"id": "q1", "answer": "go ahead", "from_options": false})
	tmp := answerPath + ".tmp"
	if err := os.WriteFile(tmp, payload, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, answerPath); err != nil {
		t.Fatal(err)
	}
	dialogPath := filepath.Join(stageDir, "planning.dialog.jsonl")
	if err := mcp.AppendAnswer(dialogPath, mcp.Answer{ID: "q1", Answer: "go ahead"}); err != nil {
		t.Fatal(err)
	}
	if err := orch.NotifyAnswer("propose", "planning", "q1", "go ahead", false); err != nil {
		t.Fatalf("NotifyAnswer: %v", err)
	}

	// Планирование дописывает валидный план → auto-approve → implementation
	// стартует. Скрипт implementation-фазы спит 3с, давая поллеру (тик раз в
	// секунду) несколько проходов relocateMisplacedQuestions, пока старые
	// planning.q1.question.json/planning.q1.answer.json ещё лежат в stageDir.
	waitForStatus(t, stateFile, "propose", state.StatusDone, 20*time.Second)

	// Баг: relocateMisplacedQuestions копировал уже отвеченный вопрос прошлой
	// фазы под именем текущей активной фазы. implementation.q1.question.json
	// не должен появиться — ничто в implementation-фазе не спрашивает q1.
	if _, err := os.Stat(filepath.Join(stageDir, "implementation.q1.question.json")); err == nil {
		t.Error("implementation.q1.question.json spuriously created from stale answered planning question")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat implementation.q1.question.json: %v", err)
	}
}

// TestIntegration_RealMalformedQuestionRepairedByFreshAgent drives the EXACT
// broken question.json captured from the production log
// (testdata/malformed_q4.question.json — an over-escaped `\\"1\\"` that
// prematurely terminates a JSON string; jsonrepair cannot recover it, verified
// by mcp.CanParseQuestion) end-to-end through a REAL orchestrator with REAL
// subprocesses, for BOTH stage types.
//
// One script plays two roles, distinguished by AFM_STAGE_DIR exactly as
// production does (the main stage agent always gets it; runJSONFixAgent
// deliberately does not):
//   - main agent (AFM_STAGE_DIR set): copies the real broken q4 into the stage
//     dir and blocks waiting for its answer, like a real agent polling.
//   - fix agent (no AFM_STAGE_DIR): reads the file path from its prompt on
//     stdin (buildJSONFixPrompt's "File (absolute path): ..." line) and
//     deterministically repairs the over-escaping — a stand-in for what a real
//     clean-context LLM agent does.
//
// Interactive → the repaired question surfaces to the user with its real
// content intact. Non-interactive (the stage type that hung forever in prod)
// → afm auto-answers and unblocks the stage instead of polling for an answer
// that never comes.
func TestIntegration_RealMalformedQuestionRepairedByFreshAgent(t *testing.T) {
	brokenAbs, err := filepath.Abs(filepath.Join("testdata", "malformed_q4.question.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(brokenAbs); err != nil {
		t.Fatalf("testdata missing: %v", err)
	}

	cases := []struct {
		name        string
		interactive bool
		agent       flow.AgentType
		phase       string
	}{
		{"interactive_surfaces_to_user", true, flow.AgentPlanning, "planning"},
		// The real incident stage (architecture-review) was agents:[auto] —
		// a non-interactive autonomous stage that runs the command directly
		// (no plan.md needed), exactly as reproduced here.
		{"non_interactive_auto_answers", false, flow.AgentAuto, "implementation"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			scriptPath := filepath.Join(dir, "agent.sh")
			script := fmt.Sprintf(`#!/bin/bash
if [ -n "$AFM_STAGE_DIR" ]; then
  cp %q "$AFM_STAGE_DIR/%s.q4.question.json"
  while [ ! -f "$AFM_STAGE_DIR/%s.q4.answer.json" ]; do sleep 0.2; done
  exit 0
fi
prompt="$(cat)"
qpath="$(printf '%%s' "$prompt" | sed -n 's/^File (absolute path): //p' | head -1)"
[ -z "$qpath" ] && exit 1
python3 - "$qpath" <<'PY'
import sys
p = sys.argv[1]
raw = open(p, encoding='utf-8').read()
open(p, 'w', encoding='utf-8').write(raw.replace(chr(92) + chr(92) + chr(34), chr(92) + chr(34)))
PY
`, brokenAbs, tc.phase, tc.phase)
			if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}

			stages := []flow.Stage{{
				ID:          "s",
				Name:        "S",
				Description: "writes the real broken q4 from the production log",
				Agents:      []flow.AgentType{tc.agent},
				Interactive: tc.interactive,
				Command:     scriptPath,
			}}
			store, err := state.Open(dir, []string{"s"})
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
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			runOrchestratorAsync(ctx, t, orch, cancel)

			stageDir := filepath.Join(dir, "s")

			if tc.interactive {
				// The repaired question must surface to the user, with real content.
				waitForStatus(t, stateFile, "s", state.StatusAwaitingUserInput, 25*time.Second)
				qs, err := mcp.FindUnansweredQuestions(stageDir)
				if err != nil {
					t.Fatal(err)
				}
				if len(qs) != 1 || qs[0].ID != "q4" || qs[0].Malformed {
					t.Fatalf("want exactly one repaired (non-malformed) q4, got %+v", qs)
				}
				if len(qs[0].Options) != 2 {
					t.Errorf("repaired question lost its options: %+v", qs[0].Options)
				}
				if !strings.Contains(qs[0].Question, "Companion gate") {
					t.Errorf("repaired question lost its real content: %q", qs[0].Question)
				}
				return
			}

			// Non-interactive: afm must AUTO-ANSWER the repaired question and
			// unblock the stage — the exact thing that never happened in prod,
			// where an unparseable question hung the stage forever.
			answerPath := filepath.Join(stageDir, tc.phase+".q4.answer.json")
			deadline := time.Now().Add(25 * time.Second)
			var data []byte
			for time.Now().Before(deadline) {
				if b, err := os.ReadFile(answerPath); err == nil {
					data = b
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			if data == nil {
				t.Fatal("no answer.json was written — the non-interactive stage would hang forever")
			}
			var got struct {
				Answer      string `json:"answer"`
				FromOptions bool   `json:"from_options"`
			}
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("answer.json is not valid JSON: %v", err)
			}
			// The repaired question's real first option flowed through PickAutoAnswer.
			if !got.FromOptions || !strings.HasPrefix(got.Answer, "Apply suggested fix") {
				t.Errorf("auto-answer did not pick the repaired question's real option: %+v", got)
			}
			if st := store.Snapshot().Stages["s"].Status; st == state.StatusAwaitingUserInput {
				t.Errorf("non-interactive stage must never sit in awaiting_user_input, got %s", st)
			}
		})
	}
}
