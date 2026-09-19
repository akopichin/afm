package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/orchestrator/verify"
	"github.com/akopichin/afm/pkg/state"
)

// doneWritingRunner is a minimal executor.Runner stub used only by
// TestResumeOwner_RegistersLeaseForVerifierSlotSwap: RunAgent writes .done
// next to the log file (satisfying stagefiles.CheckCompletion's file probe)
// without spawning a real subprocess.
type doneWritingRunner struct{}

func (doneWritingRunner) RunPlanning(ctx context.Context, stageName, prompt, outFile, logFile string) error {
	return nil
}

func (doneWritingRunner) RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error {
	return os.WriteFile(filepath.Join(filepath.Dir(logFile), ".done"), []byte("done"), 0644)
}

func (doneWritingRunner) RunJSONQuery(ctx context.Context, prompt string) ([]byte, error) {
	return nil, nil
}

// TestResumeOwner_RegistersLeaseForVerifierSlotSwap is a regression for a
// review finding: resumeOwner (the ACTIVELY-USED review-pause resume
// dispatcher — any execution stage resuming after a review pause) used to
// call the bare concurrency.SpawnAgent directly, bypassing spawnKind's
// lease-registration wrapper entirely. RunVerification then found no lease
// for the stage (o.stageLeases.Load returned ok=false) and ran the verifier
// subprocess WITHOUT any command-slot accounting — a real max_parallel
// violation for the verify command on this path, not just a missed
// optimization. Fixed by extracting spawnAgentLeased (used by spawnKind AND
// by resumeOwner AND by recovery.go's resumeInteractiveAgent) as the single
// control point that always registers a lease.
//
// This test drives resumeOwner's REAL spawn path (no o.testRunnerHook stub)
// with two real concurrency.ChannelSemaphores standing in for the author's
// command ("" -> resolved to the manager's default "claude") and the
// verifier's command ("codex"): while the verify agent step runs, the codex
// slot must be held (len==1) and the claude slot must be released (len==0) —
// proving the lease was registered by resumeOwner's spawn and really swapped,
// not silently absent.
func TestResumeOwner_RegistersLeaseForVerifierSlotSwap(t *testing.T) {
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte("## Tasks\n- x\n"), 0644); err != nil {
		t.Fatal(err)
	}

	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	// Situation #2 from resumeOwner's own doc comment: the FSM transition out
	// of paused already committed (crash/replay before its spawn ran) — the
	// stage is simply Running with no live agent behind it, resumeOwner just
	// re-spawns. seedStageStatus bypasses the FSM guard (raw Pending->target),
	// same technique the neighboring tests in this file already use.
	seedStageStatus(t, store, "s1", state.StatusRunning)

	// Command "" on the stage resolves to the manager's defaultCmd ("claude")
	// via resolveCmd — this also keeps runnerFor's fake-Runner substitution
	// path active (it only fires for s.Command == "").
	stage := flow.Stage{ID: "s1", Name: "S1",
		Agents: []flow.AgentType{flow.AgentImplementation},
		Verify: flow.VerifySpec{Steps: []flow.VerifyStep{{Kind: flow.VerifyAgent, Command: "codex"}}},
	}

	claudeSem := concurrency.ChannelSemaphore(make(chan struct{}, 1))
	codexSem := concurrency.ChannelSemaphore(make(chan struct{}, 1))
	cb := bus.NewCriticalBus(16)
	mgr := concurrency.NewWithSemaphores(cb, map[string]concurrency.Semaphore{
		"claude": claudeSem,
		"codex":  codexSem,
	}, "claude")

	fake := doneWritingRunner{}
	o := &Orchestrator{
		opts:        Options{RunDir: runDir, Stages: []flow.Stage{stage}, Store: store, Config: config.Default(), Runner: fake},
		graph:       graph.NewGraph([]flow.Stage{stage}),
		runner:      fake,
		critical:    cb,
		ui:          bus.NewUIBus(),
		fsm:         bus.NewFSM(store),
		concurrency: mgr,
	}

	var sawVerifierSlotHeld, sawAuthorSlotReleased bool
	o.runVerifyAgent = func(context.Context, flow.Stage, string, string, string, string) (verify.RunOutcome, error) {
		sawVerifierSlotHeld = len(codexSem) == 1
		sawAuthorSlotReleased = len(claudeSem) == 0
		return verify.RunOutcome{ProcessOK: true, Result: &verify.ModelResult{
			SchemaVersion: verify.SupportedSchemaVersion, Verdict: verify.VerdictPass, Summary: "ok",
		}}, nil
	}

	// o.testRunnerHook is intentionally left nil — this must exercise
	// resumeOwner's REAL spawn call (spawnAgentLeased), not the test stub.
	if ok := o.resumeOwner(context.Background(), state.PauseOwner{ID: "s1", ResumeKind: kindImplementation}, false); !ok {
		t.Fatal("resumeOwner returned false")
	}
	o.concurrency.WaitAgents()

	if !sawVerifierSlotHeld {
		t.Error("expected the codex slot to be held while the verify agent step runs — resumeOwner must register a lease")
	}
	if !sawAuthorSlotReleased {
		t.Error("expected the claude (author) slot to be released while the verify agent step runs — never two slots at once")
	}
}
