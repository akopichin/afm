package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
)

// sameRunner reports whether two runner funcs are the same method: method values of one method share a code
// pointer whatever the receiver, and different methods never do.
func sameRunner(a, b func(context.Context, flow.Stage)) bool {
	return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
}

// Пустой и неизвестный kind обязаны выбирать implementation — на этом держится recovery старых run'ов.
func TestRunnerKinds_MapEachKindToItsRunners(t *testing.T) {
	o := new(Orchestrator)
	tests := []struct {
		kind                        string
		fresh, withFeedback, fbTurn func(context.Context, flow.Stage)
	}{
		{kindPlanning, o.runPlanningAgent, o.runPlanningWithFeedback, o.runPlanningFeedbackTurn},
		{kindImplementation, o.runImplementationAgent, o.runImplementationWithFeedback, o.runImplementationFeedbackTurn},
		{kindReview, o.runReviewAgent, o.runReviewWithFeedback, o.runReviewFeedbackTurn},
		{kindAutonomous, o.runAutonomousAgent, o.runAutonomousWithFeedback, o.runAutonomousFeedbackTurn},
		{"", o.runImplementationAgent, o.runImplementationWithFeedback, o.runImplementationFeedbackTurn},
		{"unknown", o.runImplementationAgent, o.runImplementationWithFeedback, o.runImplementationFeedbackTurn},
	}
	for _, tc := range tests {
		t.Run("kind="+tc.kind, func(t *testing.T) {
			if !sameRunner(o.plainRunner(tc.kind), tc.fresh) {
				t.Error("plainRunner is not the fresh runner of the kind")
			}
			if !sameRunner(o.withFeedbackRunner(tc.kind), tc.withFeedback) {
				t.Error("withFeedbackRunner is not the Revise runner of the kind")
			}
			if !sameRunner(o.feedbackTurnRunner(tc.kind), tc.fbTurn) {
				t.Error("feedbackTurnRunner is not the feedback-turn runner of the kind")
			}
			if !sameRunner(o.turnRunner(tc.kind, false), tc.fresh) {
				t.Error("turnRunner(feedback=false) is not the fresh runner of the kind")
			}
			if !sameRunner(o.turnRunner(tc.kind, true), tc.fbTurn) {
				t.Error("turnRunner(feedback=true) is not the feedback-turn runner of the kind")
			}
		})
	}
}

// phaseRecorder is a runner that only records the phase each agent turn was started for.
type phaseRecorder struct {
	fakeAgentRunner
	mu     sync.Mutex
	phases []string
}

func (r *phaseRecorder) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	r.mu.Lock()
	r.phases = append(r.phases, agentType)
	r.mu.Unlock()
	return r.fakeAgentRunner.RunAgent(ctx, agentType, stageName, prompt, logFile)
}

func (r *phaseRecorder) firstPhase() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.phases) == 0 {
		return ""
	}
	return r.phases[0]
}

// Revise из awaiting_assistants запускает feedback-раннер трека стадии: executionKind выбирает autonomous
// для auto-стадии и для legacy autonomous.flag, иначе implementation.
func TestRevise_FromAwaitingAssistantsRunsTheFeedbackRunnerOfTheStageTrack(t *testing.T) {
	tests := []struct {
		name      string
		stage     flow.Stage
		legacyFlg bool
		wantPhase string
	}{
		{"implementation", flow.Stage{ID: assistantsTestStageID}, false, phaseImplementation},
		{"auto stage", flow.Stage{ID: assistantsTestStageID, Agents: []flow.AgentType{flow.AgentAuto}}, false, phaseAutonomous},
		{"legacy autonomous flag", flow.Stage{ID: assistantsTestStageID}, true, phaseAutonomous},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o, _, _ := enterAwaitingAssistantsForRevise(t, tc.stage)
			rec := &phaseRecorder{}
			o.opts.Runner = rec
			o.runner = rec
			stageDir := filepath.Join(o.opts.RunDir, assistantsTestStageID)
			// Implementation turn starts only from an existing plan.
			if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("## Tasks\n- x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if tc.legacyFlg {
				if err := os.WriteFile(filepath.Join(stageDir, "autonomous.flag"), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}

			applied, _, err := o.Revise(context.Background(), assistantsTestStageID, "try again")
			if err != nil || !applied {
				t.Fatalf("Revise: applied=%v err=%v, want applied", applied, err)
			}
			o.concurrency.WaitAgents()

			if got := rec.firstPhase(); got != tc.wantPhase {
				t.Fatalf("first agent turn after Revise ran phase %q, want %q", got, tc.wantPhase)
			}
		})
	}
}
