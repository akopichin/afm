package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

func TestRecovery_StartupApproveBeforeRunSuccessorStartsOnce(t *testing.T) {
	stages := []flow.Stage{
		{ID: "plan", Name: "Plan", Agents: []flow.AgentType{flow.AgentPlanning}},
		{ID: "after", Name: "After", Agents: []flow.AgentType{flow.AgentAuto}, DependsOn: []string{"plan"}},
	}
	runner := newStartupCountRunner()
	o, _ := startupBarrierOrch(t, stages, map[string]state.StageStatus{"plan": state.StatusAwaitingApproval}, runner)
	// Exercise the public pre-Run action. runContext intentionally uses its fallback here; drain releases
	// the test runner independently of that context, so the uncancellable fallback cannot leak a goroutine.
	if err := o.Approve(context.Background(), "plan"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return runner.count(runner.agent, "After") >= 1 })
	if got := o.currentStatus("after"); got != state.StatusRunning {
		t.Fatalf("successor before startup = %s, want running", got)
	}
	o.startPlanningForPending(context.Background())
	runner.drain(o)
	if got := runner.count(runner.agent, "After"); got != 1 {
		t.Errorf("pre-approved successor runs = %d, want exactly 1", got)
	}
}

func TestRecovery_StartupApproveBeforeRunImplementationStartsOnce(t *testing.T) {
	stage := flow.Stage{ID: "work", Name: "Work", Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}}
	runner := newStartupCountRunner()
	o, runDir := startupBarrierOrch(t, []flow.Stage{stage}, map[string]state.StageStatus{"work": state.StatusAwaitingApproval}, runner)
	stageDir := filepath.Join(runDir, "work")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("approved plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := o.Approve(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return runner.count(runner.agent, "Work") >= 1 })
	if got := o.currentStatus("work"); got != state.StatusRunning {
		t.Fatalf("approved stage before startup = %s, want running", got)
	}
	o.startPlanningForPending(context.Background())
	runner.drain(o)
	if got := runner.count(runner.agent, "Work"); got != 1 {
		t.Errorf("pre-approved implementation runs = %d, want exactly 1", got)
	}
}

func TestRecovery_StartupApproveBeforeRunFinishedScriptStartsOnce(t *testing.T) {
	runsLog := filepath.Join(t.TempDir(), "successor.log")
	stages := []flow.Stage{
		{ID: "plan", Name: "Plan", Agents: []flow.AgentType{flow.AgentPlanning}},
		{ID: "after", Name: "After", Script: "echo ran >> " + runsLog, DependsOn: []string{"plan"}},
	}
	runner := newStartupCountRunner()
	o, _ := startupBarrierOrch(t, stages, map[string]state.StageStatus{"plan": state.StatusAwaitingApproval}, runner)
	if err := o.Approve(context.Background(), "plan"); err != nil {
		t.Fatal(err)
	}
	// The script has exited, but its queued completion has not been consumed by Run. IsActive must be
	// false while the status remains running: only a lasting process-local spawn record can recognize it.
	o.concurrency.WaitAgents()
	if o.concurrency.IsActive("after") {
		t.Fatal("script executor is still active")
	}
	if got := o.currentStatus("after"); got != state.StatusRunning {
		t.Fatalf("finished script before startup = %s, want running", got)
	}
	assertRuns := func() {
		t.Helper()
		data, err := os.ReadFile(runsLog)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Count(string(data), "ran\n"); got != 1 {
			t.Errorf("script runs = %d, want exactly 1", got)
		}
	}
	assertRuns()
	o.startPlanningForPending(context.Background())
	o.concurrency.WaitAgents()
	assertRuns()
}

func TestRecovery_StartupReviewResumedOwnersStartOnce(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status state.StageStatus
		kind   string
		agents []flow.AgentType
	}{
		{"planning", state.StatusPlanning, kindPlanning, []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}},
		{"retrying_auto", state.StatusRetrying, kindAutonomous, []flow.AgentType{flow.AgentAuto}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stage := flow.Stage{ID: "owner", Name: "Owner", Agents: tc.agents}
			runner := newStartupCountRunner()
			o, runDir := startupBarrierOrch(t, []flow.Stage{stage}, nil, runner)
			setPausedFrom(t, o.opts.Store, "owner", tc.status)
			marker := state.PauseMarker{
				Version: 1, OperationID: "startup-registry", State: state.PauseStateResuming,
				Mode: state.PauseModeCancel, Owned: []state.PauseOwner{{ID: "owner", ResumeKind: tc.kind}},
			}
			if err := state.WriteNotesPauseMarker(runDir, marker); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			stopAgents(t, o, cancel)
			if err := o.recoverReviewPause(ctx); err != nil {
				t.Fatal(err)
			}
			counts := runner.agent
			if tc.kind == kindPlanning {
				counts = runner.planning
			}
			waitFor(t, func() bool { return runner.count(counts, "Owner") >= 1 })
			if got := o.currentStatus("owner"); got != tc.status {
				t.Fatalf("resumed owner before startup = %s, want %s", got, tc.status)
			}
			o.startPlanningForPending(ctx)
			runner.drain(o)
			if got := runner.count(counts, "Owner"); got != 1 {
				t.Errorf("review-resumed %s owner runs = %d, want exactly 1", tc.status, got)
			}
			if got := o.currentStatus("owner"); got != tc.status {
				t.Errorf("startup changed resumed owner: got %s, want %s", got, tc.status)
			}
			if tc.status == state.StatusRetrying {
				history, err := o.opts.Store.History()
				if err != nil {
					t.Fatal(err)
				}
				for _, tr := range history {
					if tr.StageID == "owner" && tr.To == state.StatusReady {
						t.Error("startup moved a review-resumed retrying owner through ready")
					}
				}
			}
		})
	}
}
