package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// A turn that ends with a question for the user and a wait on a colleague is not complete: the stage stays with
// the user, and the wait parks it when the user's answer has been handled.
func TestRunWithRetry_OpenQuestionWithAPendingWaitIsNotACompletion(t *testing.T) {
	o, runDir := setupHookOrch(t, "s1")
	o.absRunDir = runDir
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	events := o.critical.Recv()

	o.runWithRetry(context.Background(), flow.Stage{ID: "s1", Interactive: true}, phaseAutonomous,
		func(string) error {
			registerTestStageWait(t, o, "s1", "op-question-wait")
			question := filepath.Join(stageDir, "autonomous_execution.q1.question.json")
			if err := os.WriteFile(question, []byte(`{"id":"q1","question":"which one?"}`), 0o644); err != nil {
				t.Fatal(err)
			}
			// The question poller moved the stage to the user while the agent was still running.
			o.Trigger("s1", bus.EvAskUser, bus.GuardCtx{Phase: phaseAutonomous}, "")
			return nil
		},
		nil,
		func() { t.Error("onUserInterrupted should not be called") },
	)

	for {
		select {
		case ev := <-events:
			if ev.Type == bus.EventAgentCompleted {
				t.Fatalf("the stage was reported complete with a wait still pending: %+v", ev)
			}
			continue
		default:
		}
		break
	}
	if got := o.opts.Store.Get("s1"); got != state.StatusAwaitingUserInput {
		t.Fatalf("status = %v, want awaiting_user_input", got)
	}
}
