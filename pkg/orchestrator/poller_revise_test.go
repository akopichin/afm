package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/mcp"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/state"
)

// A question the interrupted agent left open belongs to the turn the Revise replaced: the question poller must not
// hand the revised stage to the user, or the Revise's feedback run finds it taken and never runs.
func TestPoller_DoesNotHandARevisedStageToTheUser(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	stages := []flow.Stage{{ID: "s1", Interactive: true}}
	o.opts.Stages = stages
	o.graph = graph.NewStore(graph.NewRegistry(stages))
	if err := os.MkdirAll(filepath.Join(runDir, "s1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "s1", "autonomous_execution.q1.question.json"), []byte(`{"id":"q1","question":"which one?"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if applied, _, err := o.Revise(context.Background(), "s1", "do it differently"); err != nil || !applied {
		t.Fatalf("Revise = %v, %v", applied, err)
	}

	o.pollQuestions(map[string]bool{}, map[string]*malformedQuestionState{})

	if got := o.currentStatus("s1"); got != state.StatusRevising {
		t.Fatalf("status after the poller's tick = %s, want still revising: the feedback run's claim must win", got)
	}
}

// The poller found the question open, and the answer was written before it moved the stage: the stage is not handed
// to the user for an answered question (nothing would ever resume it), the live agent reads the answer.
func TestPoller_AnswerBeforeTheMoveKeepsTheStageRunning(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "autonomous_execution.q1.question.json"), []byte(`{"id":"q1","question":"which one?"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mcp.WriteAnswerFile(stageDir, phaseAutonomous, "q1", "the first", false); err != nil {
		t.Fatal(err)
	}

	if _, move := o.surfaceQuestion("s1", phaseAutonomous, "q1"); move != questionAnswered {
		t.Fatalf("move = %d, want questionAnswered: the poller surfaced a question answered before its move", move)
	}
	if got := o.currentStatus("s1"); got != state.StatusRunning {
		t.Fatalf("status = %s, want running", got)
	}
}

// The question the interrupted agent left open is not lost either: nothing asks the user while the stage is revising,
// and the question is asked once the feedback run has the stage. The malformed-question fallback decides the same.
func TestPoller_AsksARevisedStagesQuestionOnceTheFeedbackRunHasIt(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(map[bool]string{false: "question", true: "malformed question"}[malformed], func(t *testing.T) {
			o, runDir := setupHookOrch(t, "s1")
			stages := []flow.Stage{{ID: "s1", Interactive: true}}
			o.opts.Stages = stages
			o.graph = graph.NewStore(graph.NewRegistry(stages))
			stageDir := filepath.Join(runDir, "s1")
			if err := os.MkdirAll(stageDir, 0o755); err != nil {
				t.Fatal(err)
			}
			qPath := filepath.Join(stageDir, "autonomous_execution.q1.question.json")
			if err := os.WriteFile(qPath, []byte(`{"id":"q1","question":"which one?"}`), 0o644); err != nil {
				t.Fatal(err)
			}
			if applied, _, err := o.Revise(context.Background(), "s1", "do it differently"); err != nil || !applied {
				t.Fatalf("Revise = %v, %v", applied, err)
			}
			_, events := o.ui.Subscribe(64)
			processed := map[string]bool{}
			poll := func() {
				if malformed {
					o.giveUpOnMalformedQuestion("s1", stageDir, mcp.QuestionFile{ID: "q1", Phase: phaseAutonomous}, qPath, []byte("{broken"))
					malformed = false // from here the stub flows through the normal scan
					return
				}
				o.pollQuestions(processed, map[string]*malformedQuestionState{})
			}

			poll()
			if asks := askEvents(events); len(asks) != 0 {
				t.Fatalf("asks while revising = %+v, want none: the stage is not the user's", asks)
			}
			if _, ok := o.Trigger("s1", bus.EvStartRun, bus.GuardCtx{ExpectFrom: state.StatusRevising}, ""); !ok {
				t.Fatal("the feedback run's claim failed")
			}
			poll()

			asks := askEvents(events)
			if len(asks) != 1 || asks[0].Seq == 0 {
				t.Fatalf("asks once the feedback run has the stage = %+v, want one, with its transition's seq", asks)
			}
			if got := o.currentStatus("s1"); got != state.StatusAwaitingUserInput {
				t.Fatalf("status = %s, want awaiting_user_input", got)
			}
		})
	}
}

// askEvents drains what was published so far and returns the asks.
func askEvents(events <-chan bus.Event) []bus.Event {
	var asks []bus.Event
	for {
		select {
		case ev := <-events:
			if ev.Type == bus.EventAskUser {
				asks = append(asks, ev)
			}
		default:
			return asks
		}
	}
}
