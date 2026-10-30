package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/state"
)

// A restart finds the summary a correction (a post-exit continuation or the Stop hook reminder) got written: the
// stage completes from the file and the recovery is announced once, as a live turn's completion would.
func TestRecoveredSummary_AnnouncesTheCorrectionOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		step state.SummaryStep
	}{
		{"post-exit correction", state.SummaryStep{Source: state.SummarySourcePostExit, Reason: "missing"}},
		{"stop hook reminder", state.SummaryStep{Source: state.SummarySourceStopHook, Reason: "missing"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stage := flow.Stage{ID: "auto", Agents: []flow.AgentType{flow.AgentAuto}}
			o, store := newAssistantsTestOrchestrator(t, true, stage)
			t.Cleanup(o.concurrency.WaitAgents)
			stageDir := filepath.Join(o.opts.RunDir, stage.ID)
			if err := os.MkdirAll(stageDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(stageDir, stagefiles.SummaryFile), []byte("Done.\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, ok := o.Trigger(stage.ID, bus.EvReady, bus.GuardCtx{}, ""); !ok {
				t.Fatal("EvReady failed")
			}
			if _, ok := o.Trigger(stage.ID, bus.EvStartRun, bus.GuardCtx{}, ""); !ok {
				t.Fatal("EvStartRun failed")
			}
			step := tc.step
			if err := store.Apply(&state.Transition{
				StageID: stage.ID, From: state.StatusRunning, To: state.StatusRunning,
				Event: string(bus.EvReserveSummaryHook), SummaryStep: &step,
			}); err != nil {
				t.Fatal(err)
			}

			o.resumeExecutionStage(context.Background(), stage, state.StatusRunning)
			o.concurrency.WaitAgents()

			if got := store.Get(stage.ID); got != state.StatusDone {
				t.Fatalf("status = %v, want the stage completed from the recovered summary", got)
			}
			if n := countNotices(t, o.opts.RunDir, eventSummaryRecovered); n != 1 {
				t.Fatalf("recovered notices = %d, want exactly one", n)
			}
			// A second pass over the same recovery announces nothing new.
			o.noteSummaryRecovered(stage.ID, stageDir)
			if n := countNotices(t, o.opts.RunDir, eventSummaryRecovered); n != 1 {
				t.Fatalf("recovered notices after a repeat = %d, want still one", n)
			}
		})
	}
}

// A stage whose summary was there from the start was never corrected: recovery announces nothing.
func TestRecoveredSummary_NoCorrectionNoNotice(t *testing.T) {
	stage := flow.Stage{ID: "auto", Agents: []flow.AgentType{flow.AgentAuto}}
	o, store := newAssistantsTestOrchestrator(t, true, stage)
	t.Cleanup(o.concurrency.WaitAgents)
	stageDir := filepath.Join(o.opts.RunDir, stage.ID)
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, stagefiles.SummaryFile), []byte("Done.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	activateStage(t, o, stage.ID)

	o.resumeExecutionStage(context.Background(), stage, state.StatusRunning)
	o.concurrency.WaitAgents()

	if got := store.Get(stage.ID); got != state.StatusDone {
		t.Fatalf("status = %v, want done", got)
	}
	if n := countNotices(t, o.opts.RunDir, eventSummaryRecovered); n != 0 {
		t.Fatalf("recovered notices = %d, want none", n)
	}
}

func countNotices(t *testing.T, runDir string, typ bus.EventType) int {
	t.Helper()
	r := &summaryRun{t: t, runDir: runDir}
	return len(r.notices(typ))
}
