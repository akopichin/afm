package orchestrator

// Task 11: не-FSM lifecycle-события stage_question_asked/answered при
// авто-ответе неинтерактивной стадии (pkg/orchestrator/dialog_poller.go).
// Интерактивный путь (EvAskUser/EvUserAnswered) уже покрыт FSM-мапингом
// Task 8 (fsmLifecycleEvents, lifecycle_emit.go) — здесь проверяется только
// не-FSM ветка авто-ответа и её гейт против дублей для запаркованных стадий.

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/lifecyclehooks"
	"github.com/akopichin/afm/pkg/state"
)

// drainCaptured non-blockingly drains every currently-buffered Payload from
// capture.events into an ordered slice of event types (delivery order == the
// order the dispatcher's per-hook queue processed them, which for a single
// "all events" hook is the emission order — Task 8/9).
func drainCaptured(capture *captureDispatcher) []lifecyclehooks.EventType {
	var out []lifecyclehooks.EventType
	for {
		select {
		case p := <-capture.events:
			out = append(out, p.Event)
		default:
			return out
		}
	}
}

// firstAndLastIndex returns the index of the FIRST occurrence of before and
// the LAST occurrence of after in evs — the same "before strictly earlier
// than after" idiom used by assertOrder in lifecycle_flow_test.go, reimplemented
// here because this file lives in the internal orchestrator package.
func firstAndLastIndex(evs []lifecyclehooks.EventType, before, after lifecyclehooks.EventType) (int, int) {
	bi, ai := -1, -1
	for i, e := range evs {
		if e == before && bi == -1 {
			bi = i
		}
		if e == after {
			ai = i
		}
	}
	return bi, ai
}

// TestPollQuestions_AutoAnswerEmitsLifecycleEvents покрывает основной случай:
// не-interactive стадия жива (Running, агент опрашивает answer.json) — её
// авто-ответ должен эмитить stage_question_asked строго ДО
// stage_question_answered, с Reason = текст вопроса / текст выбранного ответа.
func TestPollQuestions_AutoAnswerEmitsLifecycleEvents(t *testing.T) {
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
	capture := newCaptureDispatcher()
	o.hooks = captureDispatcherAsReal(t, capture)

	o.pollQuestions(map[string]bool{}, map[string]*malformedQuestionState{})

	if !o.hooks.Flush(2 * time.Second) {
		t.Fatal("Flush timed out — delivery never completed")
	}

	var payloads []lifecyclehooks.Payload
	for {
		select {
		case p := <-capture.events:
			payloads = append(payloads, p)
		default:
			goto drained
		}
	}
drained:

	var asked, answered *lifecyclehooks.Payload
	var types []lifecyclehooks.EventType
	for i := range payloads {
		types = append(types, payloads[i].Event)
		switch payloads[i].Event {
		case lifecyclehooks.EventStageQuestionAsked:
			if asked == nil {
				asked = &payloads[i]
			}
		case lifecyclehooks.EventStageQuestionAnswered:
			answered = &payloads[i]
		default:
		}
	}
	if asked == nil || answered == nil {
		t.Fatalf("both stage_question_asked and stage_question_answered must be emitted: %v", types)
	}
	bi, ai := firstAndLastIndex(types, lifecyclehooks.EventStageQuestionAsked, lifecyclehooks.EventStageQuestionAnswered)
	if bi >= ai {
		t.Fatalf("stage_question_asked (idx %d) must come before stage_question_answered (idx %d): %v", bi, ai, types)
	}
	if asked.Reason != "which?" {
		t.Errorf("asked.Reason = %q, want %q (raw question text)", asked.Reason, "which?")
	}
	if answered.Reason != "Вариант B" {
		t.Errorf("answered.Reason = %q, want %q (chosen answer text)", answered.Reason, "Вариант B")
	}
	if asked.Stage == nil || asked.Stage.ID != "s1" {
		t.Fatalf("asked.Stage = %+v, want populated with ID=s1", asked.Stage)
	}
	if asked.Data["phase"] != nil {
		t.Errorf("Data unexpectedly populated for a non-FSM event: %+v", asked.Data)
	}
}

// TestPollQuestions_AutoAnswerParkedStageSkipsLifecycleEvents — гейт против
// дублей: если стадия уже запаркована в awaiting_user_input (агент задал
// вопрос и вышел раньше), stage_question_asked уже случился через EvAskUser,
// а stage_question_answered придёт через EvUserAnswered (Task 8's
// fsmLifecycleEvents), когда resumeAfterAnswer/onUserAnswered прогонит FSM.
// Не-FSM ветка авто-ответа здесь НЕ должна эмитить свою пару — иначе оба
// события задваиваются.
func TestPollQuestions_AutoAnswerParkedStageSkipsLifecycleEvents(t *testing.T) {
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
	if err := store.Apply(&state.Transition{StageID: stage.ID, From: state.StatusRunning, To: state.StatusAwaitingUserInput, Event: "test_park"}); err != nil {
		t.Fatal(err)
	}

	stageDir := filepath.Join(runDir, stage.ID)
	writeQuestionFile(t, stageDir, "autonomous_execution", "q1", []string{"Вариант A (recommended)"})

	o := New(Options{RunDir: runDir, Stages: []flow.Stage{stage}, Store: store, Config: config.Default()})
	capture := newCaptureDispatcher()
	o.hooks = captureDispatcherAsReal(t, capture)

	o.pollQuestions(map[string]bool{}, map[string]*malformedQuestionState{})
	o.hooks.Flush(2 * time.Second)

	got := drainCaptured(capture)
	for _, e := range got {
		if e == lifecyclehooks.EventStageQuestionAsked || e == lifecyclehooks.EventStageQuestionAnswered {
			t.Fatalf("parked stage's non-FSM auto-answer path must not emit %s directly (would double-emit against the FSM EvAskUser/EvUserAnswered path): %v", e, got)
		}
	}
}

// TestPollQuestions_AutoAnswerDistinctQuestionsGetDistinctEventIDs — finding
// #3 финального ревью: два РАЗНЫХ авто-ответа (q1, затем q2) в одной
// стадии/фазе не должны схлопываться в один и тот же event_id
// (<run>:<stage>:stage_question_answered без Key) — идемпотентный по
// event_id получатель хука иначе потерял бы второе событие.
func TestPollQuestions_AutoAnswerDistinctQuestionsGetDistinctEventIDs(t *testing.T) {
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
	o := New(Options{RunDir: runDir, Stages: []flow.Stage{stage}, Store: store, Config: config.Default()})
	capture := newCaptureDispatcher()
	o.hooks = captureDispatcherAsReal(t, capture)

	processed := map[string]bool{}
	malformed := map[string]*malformedQuestionState{}

	writeQuestionFile(t, stageDir, "implementation", "q1", []string{"Вариант A (recommended)"})
	o.pollQuestions(processed, malformed)
	writeQuestionFile(t, stageDir, "implementation", "q2", []string{"Вариант B (recommended)"})
	o.pollQuestions(processed, malformed)

	if !o.hooks.Flush(2 * time.Second) {
		t.Fatal("Flush timed out — delivery never completed")
	}

	var answeredIDs []string
	for {
		select {
		case p := <-capture.events:
			if p.Event == lifecyclehooks.EventStageQuestionAnswered {
				answeredIDs = append(answeredIDs, p.EventID)
			}
		default:
			goto drained
		}
	}
drained:
	if len(answeredIDs) != 2 {
		t.Fatalf("expected exactly 2 stage_question_answered deliveries, got %v", answeredIDs)
	}
	if answeredIDs[0] == answeredIDs[1] {
		t.Fatalf("q1 and q2 auto-answers must not share event_id, got %q twice", answeredIDs[0])
	}
}

// TestPollQuestions_AutoAnswerMalformedFallbackEmitsLifecycleEvents — та же
// пара, но по симметричному пути autoAnswerMalformed (терминальный fallback
// после исчерпания попыток fix-агента для non-interactive стадии).
func TestPollQuestions_AutoAnswerMalformedFallbackEmitsLifecycleEvents(t *testing.T) {
	broken := `not json at all {{{`
	o, store, _ := setupMalformedTestOrchNI(t, broken)
	injectFixStub(t, o, "") // every fix agent fails to repair
	capture := newCaptureDispatcher()
	o.hooks = captureDispatcherAsReal(t, capture)

	processed := map[string]bool{}
	malformed := map[string]*malformedQuestionState{}

	// grace tick + maxJSONFixAttempts spawn/complete cycles + the fallback
	// tick itself, with margin.
	for i := 0; i < maxJSONFixAttempts+3; i++ {
		o.pollQuestions(processed, malformed)
	}

	if got := store.Snapshot().Stages["s1"].Status; got != state.StatusRunning {
		t.Fatalf("status = %s, want running (auto-answered fallback, never awaits)", got)
	}

	o.hooks.Flush(2 * time.Second)
	types := drainCaptured(capture)
	bi, ai := firstAndLastIndex(types, lifecyclehooks.EventStageQuestionAsked, lifecyclehooks.EventStageQuestionAnswered)
	if bi == -1 || ai == -1 {
		t.Fatalf("both stage_question_asked and stage_question_answered must be present: %v", types)
	}
	if bi >= ai {
		t.Fatalf("stage_question_asked (idx %d) must come before stage_question_answered (idx %d): %v", bi, ai, types)
	}
}
