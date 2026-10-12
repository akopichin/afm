package orchestrator

// Task 11: не-FSM lifecycle-события stage_question_asked/answered при авто-ответе неинтерактивной стадии.

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/lifecyclehooks"
	"github.com/akopichin/afm/pkg/state"
)

// drainCaptured non-blockingly drains every currently-buffered Payload from capture.events into an ordered
// slice of event types.
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

// firstAndLastIndex locates the first before and last after event to check ordering.
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
