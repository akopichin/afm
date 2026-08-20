package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

	o.pollQuestions(map[string]bool{}, map[string]*malformedQuestionState{})

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
	o.pollQuestions(map[string]bool{}, map[string]*malformedQuestionState{})

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
	o.pollQuestions(map[string]bool{}, map[string]*malformedQuestionState{})

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
	o.pollQuestions(map[string]bool{}, map[string]*malformedQuestionState{})

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
	malformed := map[string]*malformedQuestionState{}

	// Round 1: question appears, poller asks the user.
	o.pollQuestions(processed, malformed)
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
	o.pollQuestions(processed, malformed) // must prune the now-answered key from `processed`
	if err := os.Remove(answerPath); err != nil {
		t.Fatal(err)
	}

	// Agent reuses "q1" for a brand new, distinct question.
	writeQuestionFile(t, stageDir, "implementation", "q1", []string{"B"})

	o.pollQuestions(processed, malformed)
	if got := store.Snapshot().Stages[stage.ID].Status; got != state.StatusAwaitingUserInput {
		t.Errorf("round 2 (reused id): status = %s, want awaiting_user_input — reused question was never re-surfaced", got)
	}
}

// setupMalformedTestOrch builds an interactive stage in StatusRunning with a
// broken question.json, shared by the malformed-JSON retry tests below.
func setupMalformedTestOrch(t *testing.T, broken string) (*Orchestrator, *state.Store, string) {
	t.Helper()
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
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	qPath := filepath.Join(stageDir, "implementation.q1.question.json")
	if err := os.WriteFile(qPath, []byte(broken), 0644); err != nil {
		t.Fatal(err)
	}

	o := New(Options{RunDir: runDir, Stages: []flow.Stage{stage}, Store: store, Config: config.Default()})
	return o, store, stageDir
}

// TestPollQuestions_MalformedQuestion_GraceTickHidesFromUser is a regression
// test for a bug found in a real production log, root-caused byte-for-byte:
// a question.json that fails to parse on its FIRST observation is very
// often a torn read — afm's poller caught the file while the agent's Write
// tool call was still landing on disk — not a genuine mistake. The very
// first sighting of broken content must never reach the user: no dialog
// entry, no synthetic answer, no EvAskUser, stage status untouched.
func TestPollQuestions_MalformedQuestion_GraceTickHidesFromUser(t *testing.T) {
	o, store, stageDir := setupMalformedTestOrch(t, `not json at all {{{`)

	o.pollQuestions(map[string]bool{}, map[string]*malformedQuestionState{})

	if got := store.Snapshot().Stages["s1"].Status; got != state.StatusRunning {
		t.Errorf("status = %s, want unchanged running (grace tick must not surface anything)", got)
	}
	if _, err := os.Stat(filepath.Join(stageDir, "implementation.q1.answer.json")); err == nil {
		t.Error("no answer.json should be written on the very first sighting of broken content")
	}
	if entries, _ := mcp.ReadDialog(filepath.Join(stageDir, "implementation.dialog.jsonl")); len(entries) != 0 {
		t.Errorf("no dialog entry should exist yet, got %+v", entries)
	}
}

// TestPollQuestions_MalformedQuestion_ResolvesSilentlyIfWriteCompletes proves
// the grace-tick mechanism actually fixes the real incident: once the
// (previously torn) file becomes valid, it must flow through completely
// normally on the next tick — no trace that anything was ever wrong, no
// agent involvement.
func TestPollQuestions_MalformedQuestion_ResolvesSilentlyIfWriteCompletes(t *testing.T) {
	o, store, stageDir := setupMalformedTestOrch(t, `not json at all {{{`)
	processed := map[string]bool{}
	malformed := map[string]*malformedQuestionState{}

	o.pollQuestions(processed, malformed) // grace tick

	qPath := filepath.Join(stageDir, "implementation.q1.question.json")
	if err := os.WriteFile(qPath, []byte(`{"id":"q1","question":"real question?"}`), 0644); err != nil {
		t.Fatal(err)
	}

	o.pollQuestions(processed, malformed)

	if got := store.Snapshot().Stages["s1"].Status; got != state.StatusAwaitingUserInput {
		t.Fatalf("status = %s, want awaiting_user_input once the write completed", got)
	}
	entries, err := mcp.ReadDialog(filepath.Join(stageDir, "implementation.dialog.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Question != "real question?" {
		t.Fatalf("expected the real question text, got %+v", entries)
	}
}

// TestPollQuestions_MalformedQuestion_StableSendsNudge covers the genuinely-
// broken case (the user's explicit request): once the SAME broken content is
// observed on a second tick — proof the write is done, not still in flight —
// afm nudges the agent through the exact channel its own bash polling loop
// already reads (answer.json), still without bothering the user.
func TestPollQuestions_MalformedQuestion_StableSendsNudge(t *testing.T) {
	o, store, stageDir := setupMalformedTestOrch(t, `not json at all {{{`)
	processed := map[string]bool{}
	malformed := map[string]*malformedQuestionState{}

	o.pollQuestions(processed, malformed) // grace tick: remembers the broken bytes
	o.pollQuestions(processed, malformed) // same bytes again: stable, genuinely broken

	if got := store.Snapshot().Stages["s1"].Status; got != state.StatusRunning {
		t.Errorf("status = %s, want unchanged running (still nudging, not asking the user)", got)
	}
	data, err := os.ReadFile(filepath.Join(stageDir, "implementation.q1.answer.json"))
	if err != nil {
		t.Fatalf("expected a synthetic nudge answer.json: %v", err)
	}
	var got struct {
		Answer string `json:"answer"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Answer, fmt.Sprintf("попытка 1 из %d", maxMalformedRetries)) {
		t.Errorf("nudge message missing attempt count: %q", got.Answer)
	}
	if entries, _ := mcp.ReadDialog(filepath.Join(stageDir, "implementation.dialog.jsonl")); len(entries) != 0 {
		t.Errorf("no dialog entry should exist yet, got %+v", entries)
	}
}

// TestPollQuestions_MalformedQuestion_AgentRewriteUnblocksRetry proves the
// agent's response to a nudge is actually picked up: it rewrites the SAME
// id's question.json (per the "never reuse an id" rule, this is a
// correction of the SAME question, not a new one) — the stale synthetic
// answer.json left over from the nudge must not permanently hide it, the
// way the unrelated id-reuse bug (TestPollQuestions_ReusedIDAfterAnswerAsksAgain)
// used to hide a genuinely different question reusing an id.
func TestPollQuestions_MalformedQuestion_AgentRewriteUnblocksRetry(t *testing.T) {
	o, store, stageDir := setupMalformedTestOrch(t, `not json at all {{{`)
	processed := map[string]bool{}
	malformed := map[string]*malformedQuestionState{}

	o.pollQuestions(processed, malformed) // grace tick
	o.pollQuestions(processed, malformed) // stable + broken: nudge sent, answer.json written

	if _, err := os.Stat(filepath.Join(stageDir, "implementation.q1.answer.json")); err != nil {
		t.Fatalf("nudge answer.json missing before rewrite: %v", err)
	}

	// Agent reads the nudge and rewrites the SAME id with valid content.
	qPath := filepath.Join(stageDir, "implementation.q1.question.json")
	if err := os.WriteFile(qPath, []byte(`{"id":"q1","question":"fixed now?"}`), 0644); err != nil {
		t.Fatal(err)
	}

	o.pollQuestions(processed, malformed)

	if _, err := os.Stat(filepath.Join(stageDir, "implementation.q1.answer.json")); err == nil {
		t.Error("stale nudge answer.json should have been removed once the agent rewrote the file")
	}
	if got := store.Snapshot().Stages["s1"].Status; got != state.StatusAwaitingUserInput {
		t.Fatalf("status = %s, want awaiting_user_input once the rewritten question is visible", got)
	}
	entries, err := mcp.ReadDialog(filepath.Join(stageDir, "implementation.dialog.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Question != "fixed now?" {
		t.Fatalf("expected the rewritten question text, got %+v", entries)
	}
}

// TestPollQuestions_MalformedQuestion_RealAnswerSurvivesAfterRecovery is a
// regression test for a bug found live in a real browser run (not by
// inspection): once the agent fixes a nudged question and a human answers
// it normally, the STALE malformed-retry tracking entry used to survive
// forever with its old, permanently-stale lastRaw — so
// unblockRewrittenMalformedQuestions kept seeing "content changed" on every
// subsequent tick (the valid content never matches the old broken bytes)
// and deleted the human's real answer.json before the agent's own bash
// polling loop ever got to read it, hanging the stage even though the
// dashboard had shown the recovery working correctly.
func TestPollQuestions_MalformedQuestion_RealAnswerSurvivesAfterRecovery(t *testing.T) {
	o, store, stageDir := setupMalformedTestOrch(t, `not json at all {{{`)
	processed := map[string]bool{}
	malformed := map[string]*malformedQuestionState{}

	o.pollQuestions(processed, malformed) // grace tick
	o.pollQuestions(processed, malformed) // stable + broken: nudge sent

	// Agent rewrites the SAME id with valid content in response to the nudge.
	qPath := filepath.Join(stageDir, "implementation.q1.question.json")
	if err := os.WriteFile(qPath, []byte(`{"id":"q1","question":"fixed?"}`), 0644); err != nil {
		t.Fatal(err)
	}
	o.pollQuestions(processed, malformed) // unblocks + surfaces the real question

	if got := store.Snapshot().Stages["s1"].Status; got != state.StatusAwaitingUserInput {
		t.Fatalf("status = %s, want awaiting_user_input after recovery", got)
	}

	// A human answers normally, exactly like handleDialogAnswer would.
	answerPath := filepath.Join(stageDir, "implementation.q1.answer.json")
	if err := os.WriteFile(answerPath, []byte(`{"id":"q1","answer":"yes","from_options":false}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: "s1", From: state.StatusAwaitingUserInput, To: state.StatusRunning, Event: "test_setup"}); err != nil {
		t.Fatal(err)
	}

	// Further ticks (the stale malformed-retry entry, if still tracked,
	// would delete this real answer on one of these) must leave it alone.
	for i := 0; i < 3; i++ {
		o.pollQuestions(processed, malformed)
	}

	if _, err := os.Stat(answerPath); err != nil {
		t.Fatalf("real answer.json was deleted by stale malformed-retry tracking: %v", err)
	}
}

// TestPollQuestions_MalformedQuestion_UnresponsiveAgentEventuallyExhausts is
// a regression test for a design gap found while fixing the integration test
// covering this same scenario end-to-end: an agent that never responds to a
// nudge at all (crashed, ignored it, hung) never changes question.json's
// content, so a design that only unblocks a retry round on a detected
// content change would wait forever — maxMalformedRetries would never be
// reached and the stage would hang exactly like the original bug this whole
// feature fixes. MalformedNudgeTimeout provides the other half of the
// unblock condition: elapsed time with no response.
func TestPollQuestions_MalformedQuestion_UnresponsiveAgentEventuallyExhausts(t *testing.T) {
	origTimeout := MalformedNudgeTimeout
	MalformedNudgeTimeout = 10 * time.Millisecond
	t.Cleanup(func() { MalformedNudgeTimeout = origTimeout })

	broken := `not json at all {{{`
	o, store, stageDir := setupMalformedTestOrch(t, broken)
	processed := map[string]bool{}
	malformed := map[string]*malformedQuestionState{}

	o.pollQuestions(processed, malformed) // grace tick

	// Never touch question.json again — the agent simply never responds.
	// Drive enough ticks (with a short sleep to clear MalformedNudgeTimeout
	// between them) for all maxMalformedRetries rounds to elapse.
	for i := 0; i < maxMalformedRetries+1; i++ {
		time.Sleep(15 * time.Millisecond)
		o.pollQuestions(processed, malformed)
	}

	if got := store.Snapshot().Stages["s1"].Status; got != state.StatusAwaitingUserInput {
		t.Fatalf("status = %s, want awaiting_user_input once an unresponsive agent exhausts retries", got)
	}
	data, err := os.ReadFile(filepath.Join(stageDir, "implementation.q1.question.json"))
	if err != nil {
		t.Fatal(err)
	}
	var stub struct {
		Options []string `json:"options"`
	}
	if err := json.Unmarshal(data, &stub); err != nil {
		t.Fatalf("persisted stub is not valid JSON: %v", err)
	}
	if len(stub.Options) != 0 {
		t.Errorf("stub must offer no options, got %v", stub.Options)
	}
}

// TestHandleMalformedQuestion_ExhaustedShowsRawTextNoOptions covers the
// user's explicit fallback: once retries are exhausted, afm stops asking the
// agent for valid JSON, persists a real parseable stub (handleDialogAnswer
// re-parses question.json strictly on every answer submission), and shows
// the raw text to the user with NO options — free text only.
func TestHandleMalformedQuestion_ExhaustedShowsRawTextNoOptions(t *testing.T) {
	broken := `not json at all {{{`
	o, store, stageDir := setupMalformedTestOrch(t, broken)
	qPath := filepath.Join(stageDir, "implementation.q1.question.json")

	// Seed retry state as if maxMalformedRetries nudges already happened and
	// this tick's content is stable — the exact state pollQuestions would have
	// built up through repeated broken rewrites, without needing to simulate
	// each round here.
	malformed := map[string]*malformedQuestionState{
		"s1|implementation|q1": {lastRaw: []byte(broken), retries: maxMalformedRetries},
	}

	o.pollQuestions(map[string]bool{}, malformed)

	if got := store.Snapshot().Stages["s1"].Status; got != state.StatusAwaitingUserInput {
		t.Fatalf("status = %s, want awaiting_user_input once retries are exhausted", got)
	}
	if _, stillTracked := malformed["s1|implementation|q1"]; stillTracked {
		t.Error("key should be removed from `malformed` once given up on")
	}

	// The persisted stub must be valid, parseable JSON with no options.
	data, err := os.ReadFile(qPath)
	if err != nil {
		t.Fatal(err)
	}
	var stub struct {
		ID          string   `json:"id"`
		Question    string   `json:"question"`
		Options     []string `json:"options"`
		AllowCustom bool     `json:"allow_custom"`
	}
	if err := json.Unmarshal(data, &stub); err != nil {
		t.Fatalf("persisted stub is not valid JSON: %v\nraw: %s", err, data)
	}
	if len(stub.Options) != 0 {
		t.Errorf("stub must offer no options (free text only), got %v", stub.Options)
	}
	if !stub.AllowCustom {
		t.Error("stub must allow a custom (free-text) answer")
	}
	if !strings.Contains(stub.Question, broken) {
		t.Errorf("stub question should include the raw broken content for the user to read, got %q", stub.Question)
	}

	entries, err := mcp.ReadDialog(filepath.Join(stageDir, "implementation.dialog.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 dialog entry, got %+v", entries)
	}
}
