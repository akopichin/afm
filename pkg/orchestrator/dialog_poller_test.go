package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/mcp"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

func writeQuestionFile(t *testing.T, stageDir, phase, id string, options []string) {
	t.Helper()
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{
		"id": id, "question": "which?", "options": options, "allow_custom": true,
	})
	path := filepath.Join(stageDir, phase+"."+id+".question.json")
	if err := os.WriteFile(path, payload, 0644); err != nil {
		t.Fatal(err)
	}
}

// TestPollQuestions_NonInteractiveStageAutoAnswers покрывает ядро фичи:
// non-interactive стадия с открытым вопросом получает ответ от afm, не
// переходя в awaiting_user_input.
func TestPollQuestions_NonInteractiveStageAutoAnswers(t *testing.T) {
	runDir := t.TempDir()
	stage := flow.Stage{ID: "s1", Name: "Backend", Agents: []flow.AgentType{flow.AgentImplementation}}

	store, err := state.Open(runDir, []string{stage.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: stage.ID, From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	stageDir := filepath.Join(runDir, stage.ID)
	writeQuestionFile(t, stageDir, "implementation", "q1", []string{"Вариант A", "Вариант B (recommended)"})

	o := New(Options{RunDir: runDir, Stages: []flow.Stage{stage}, Store: store, Config: config.Default()})

	subID, events := o.ui.Subscribe(8)
	defer o.ui.Unsubscribe(subID)

	o.pollQuestions(map[string]bool{})

	data, err := os.ReadFile(filepath.Join(stageDir, "implementation.q1.answer.json"))
	if err != nil {
		t.Fatalf("answer.json not written: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["answer"] != "Вариант B" {
		t.Errorf("auto-answer = %v, want %q", got["answer"], "Вариант B")
	}

	select {
	case ev := <-events:
		if ev.Type != bus.EventAutoAnswered {
			t.Errorf("got event %s, want %s", ev.Type, bus.EventAutoAnswered)
		}
	default:
		t.Fatal("expected EventAutoAnswered to be published")
	}

	if got := store.Snapshot().Stages[stage.ID].Status; got != state.StatusRunning {
		t.Errorf("stage status = %s, want unchanged %s (no FSM transition on auto-answer)", got, state.StatusRunning)
	}

	entries, err := mcp.ReadDialog(filepath.Join(stageDir, "implementation.dialog.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Answer == nil || !entries[0].AutoAnswered {
		t.Fatalf("dialog entry missing auto_answered marker: %+v", entries)
	}
}

// TestPollQuestions_AutoStageAutoAnswers покрывает "agents: [auto]" стадию —
// та же non-interactive обработка (Interactive: false у auto-стадий и так
// по умолчанию, специальной проверки на IsAuto() коду не требуется).
func TestPollQuestions_AutoStageAutoAnswers(t *testing.T) {
	runDir := t.TempDir()
	stage := flow.Stage{ID: "s1", Name: "Autonomous", Agents: []flow.AgentType{flow.AgentAuto}}

	store, err := state.Open(runDir, []string{stage.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: stage.ID, From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	stageDir := filepath.Join(runDir, stage.ID)
	writeQuestionFile(t, stageDir, "autonomous_execution", "q1", []string{"Вариант A"})

	o := New(Options{RunDir: runDir, Stages: []flow.Stage{stage}, Store: store, Config: config.Default()})
	o.pollQuestions(map[string]bool{})

	if _, err := os.Stat(filepath.Join(stageDir, "autonomous_execution.q1.answer.json")); err != nil {
		t.Errorf("auto-стадия должна получить авто-ответ так же, как обычная non-interactive: %v", err)
	}
	if got := store.Snapshot().Stages[stage.ID].Status; got != state.StatusRunning {
		t.Errorf("stage status = %s, want unchanged %s", got, state.StatusRunning)
	}
}

// noticeEnvelopeData — поле "data" одной строки notices.jsonl для
// auto_answered-уведомления (см. TestPollQuestions_AutoAnswerPersistsToNotices).
type noticeEnvelopeData struct {
	ID     string `json:"id"`
	Phase  string `json:"phase"`
	Answer string `json:"answer"`
}

// noticeEnvelope — одна строка notices.jsonl (см. stagefiles.AppendNotice).
type noticeEnvelope struct {
	Type    string             `json:"type"`
	StageID string             `json:"stage_id"`
	Data    noticeEnvelopeData `json:"data"`
}

// TestPollQuestions_AutoAnswerPersistsToNotices закрывает баг, найденный
// вручную в браузере: EventAutoAnswered публикуется ТОЛЬКО живьём в UI-шину,
// поэтому клиент, подключившийся ПОСЛЕ авто-ответа, никогда не видит эту
// строку в ленте событий (durable events.jsonl эта фича сознательно не
// трогает — FSM-переход отсутствует). Как и EventAgentCompleted/
// EventContextWarning/EventScriptOutput (см. agents.go/retry.go/hooks.go),
// авто-ответ должен ТАКЖЕ дублироваться в notices.jsonl через
// stagefiles.AppendNotice — /api/events (reconstructNotices) реплеит его
// оттуда для клиентов, подключившихся позже.
func TestPollQuestions_AutoAnswerPersistsToNotices(t *testing.T) {
	runDir := t.TempDir()
	stage := flow.Stage{ID: "s1", Name: "Backend", Agents: []flow.AgentType{flow.AgentImplementation}}

	store, err := state.Open(runDir, []string{stage.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: stage.ID, From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	stageDir := filepath.Join(runDir, stage.ID)
	writeQuestionFile(t, stageDir, "implementation", "q1", []string{"Вариант A", "Вариант B (recommended)"})

	o := New(Options{RunDir: runDir, Stages: []flow.Stage{stage}, Store: store, Config: config.Default()})
	o.pollQuestions(map[string]bool{})

	noticesData, err := os.ReadFile(filepath.Join(runDir, "notices.jsonl"))
	if err != nil {
		t.Fatalf("notices.jsonl not written: %v", err)
	}
	var notice noticeEnvelope
	if err := json.Unmarshal(noticesData, &notice); err != nil {
		t.Fatalf("invalid notices.jsonl line: %v (content: %s)", err, noticesData)
	}
	if notice.Type != string(bus.EventAutoAnswered) {
		t.Errorf("notice type = %q, want %q", notice.Type, bus.EventAutoAnswered)
	}
	if notice.StageID != stage.ID || notice.Data.ID != "q1" || notice.Data.Answer != "Вариант B" {
		t.Errorf("notice content mismatch: %+v", notice)
	}
}

// TestPollQuestions_InteractiveStageStillAsksUser — регрессионная гарантия:
// interactive-стадия НЕ получает авто-ответ, поведение (EvAskUser →
// awaiting_user_input) не меняется этой фичей.
func TestPollQuestions_InteractiveStageStillAsksUser(t *testing.T) {
	runDir := t.TempDir()
	stage := flow.Stage{ID: "s1", Name: "Interactive", Agents: []flow.AgentType{flow.AgentImplementation}, Interactive: true}

	store, err := state.Open(runDir, []string{stage.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: stage.ID, From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	stageDir := filepath.Join(runDir, stage.ID)
	writeQuestionFile(t, stageDir, "implementation", "q1", []string{"Вариант A (recommended)"})

	o := New(Options{RunDir: runDir, Stages: []flow.Stage{stage}, Store: store, Config: config.Default()})
	o.pollQuestions(map[string]bool{})

	if _, err := os.Stat(filepath.Join(stageDir, "implementation.q1.answer.json")); err == nil {
		t.Error("interactive-стадия не должна получать авто-ответ")
	}
	if got := store.Snapshot().Stages[stage.ID].Status; got != state.StatusAwaitingUserInput {
		t.Errorf("stage status = %s, want %s", got, state.StatusAwaitingUserInput)
	}
}

// TestPollQuestions_ReusedIDAfterAnswerAsksAgain is a regression test for a
// bug found in a real production log: the prompt tells agents "never reuse an
// ID within a phase", but a real agent (goga-brainstorm's revision loop) did
// reuse the same id ("q2") for a second, distinct question after the first
// "q2" was answered and the agent resumed. pollQuestions's `processed` map is
// keyed only by stageID|phase|id and, once marked true, was NEVER cleared —
// so the second, genuinely-unanswered "q2" was silently invisible to the
// poller forever: no EvAskUser, no dialog.jsonl entry, stage stuck at
// "running" with a real unanswered question.json sitting on disk. No restart
// of the browser fixes this (the bug is server-side, in-memory); only a full
// restart of the afm process happens to clear the map and rediscover it.
func TestPollQuestions_ReusedIDAfterAnswerAsksAgain(t *testing.T) {
	runDir := t.TempDir()
	stage := flow.Stage{ID: "s1", Name: "Interactive", Agents: []flow.AgentType{flow.AgentImplementation}, Interactive: true}

	store, err := state.Open(runDir, []string{stage.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Apply(&state.Transition{StageID: stage.ID, From: state.StatusPending, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	stageDir := filepath.Join(runDir, stage.ID)
	writeQuestionFile(t, stageDir, "implementation", "q1", []string{"A"})

	o := New(Options{RunDir: runDir, Stages: []flow.Stage{stage}, Store: store, Config: config.Default()})
	processed := map[string]bool{}

	// Round 1: question appears, poller asks the user.
	o.pollQuestions(processed)
	if got := store.Snapshot().Stages[stage.ID].Status; got != state.StatusAwaitingUserInput {
		t.Fatalf("round 1: status = %s, want awaiting_user_input", got)
	}

	// User answers; agent resumes (Running) and consumes+removes the answer,
	// mirroring the real bash loop's `cat` + later `rm -f` pattern.
	answerPath := filepath.Join(stageDir, "implementation.q1.answer.json")
	if err := os.WriteFile(answerPath, []byte(`{"id":"q1","answer":"A","from_options":true}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: stage.ID, From: state.StatusAwaitingUserInput, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}
	o.pollQuestions(processed) // must prune the now-answered key from `processed`
	if err := os.Remove(answerPath); err != nil {
		t.Fatal(err)
	}

	// Agent reuses "q1" for a brand new, distinct question.
	writeQuestionFile(t, stageDir, "implementation", "q1", []string{"B"})

	o.pollQuestions(processed)
	if got := store.Snapshot().Stages[stage.ID].Status; got != state.StatusAwaitingUserInput {
		t.Errorf("round 2 (reused id): status = %s, want awaiting_user_input — reused question was never re-surfaced", got)
	}
}
