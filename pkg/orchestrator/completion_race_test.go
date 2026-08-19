package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

// TestIntegration_PollerRaceDoesNotStrandCompletedStage is a regression test
// for a gap found live while verifying the fix for
// TestRunWithRetry_CompletionMarkerOverridesStaleOpenQuestion: even after
// runWithRetry learned to check completion before an open question, a stage
// could still get stuck if afm's OWN independent question poller won the
// race and moved the FSM to awaiting_user_input WHILE the agent was still
// running — before the agent (unaware of this) finished its real work and
// returned. completeStage's Running/Retrying-only precondition then
// silently dropped the resulting EventAgentCompleted, leaving the stage
// stuck forever with no agent process left alive to ever retry.
//
// Uses a real bash script as the stage Command, not an injected Runner:
// interactive stages (stage.Interactive=true) always ignore the injected
// Runner — runnerFor builds a real executor.New(...) driven by
// stage.Command regardless (see CLAUDE.md's File-Based Dialog Protocol
// section, and poller_test.go's block.sh for the established pattern).
func TestIntegration_PollerRaceDoesNotStrandCompletedStage(t *testing.T) {
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}

	// The agent writes a stray question.json (it never waits for an answer
	// to it — a mistake, or a leftover from earlier work), keeps "running"
	// long enough for the real 1-second question poller to notice the file
	// and independently move the FSM to awaiting_user_input, and only THEN
	// finishes its real work and writes the completion marker before
	// exiting — racing the poller goroutine within a single invocation.
	scriptPath := filepath.Join(runDir, "racyagent.sh")
	script := "#!/bin/bash\n" +
		`printf '{"id":"q_stale","question":"never answered"}' > "$AFM_STAGE_DIR/autonomous_execution.q_stale.question.json"` + "\n" +
		"sleep 1.5\n" +
		`printf '## Summary\ndone\n' > "$AFM_STAGE_DIR/execution_summary.md"` + "\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{{
		ID:          "s1",
		Name:        "S1",
		Agents:      []flow.AgentType{flow.AgentAuto},
		Interactive: true,
		Command:     scriptPath,
	}}
	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stateFile := filepath.Join(runDir, "state.json")

	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	runOrchestratorAsync(ctx, t, orch, cancel)

	waitForStatus(t, stateFile, "s1", state.StatusDone, 5*time.Second)
}

// TestIntegration_PollerRaceDoesNotStrandPlanningStage is the planning-phase
// counterpart of TestIntegration_PollerRaceDoesNotStrandCompletedStage:
// onAgentCompleted's phasePlanning branch has the exact same
// Planning/Retrying-only precondition (and EvPlanReady the same From list in
// the FSM), so the identical poller-vs-agent-exit race strands a stage that
// finished planning successfully, for the same reason. Found by auditing
// for the same shape after fixing the implementation/autonomous case, not
// by a separate live incident.
func TestIntegration_PollerRaceDoesNotStrandPlanningStage(t *testing.T) {
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}

	scriptPath := filepath.Join(runDir, "racyplanner.sh")
	script := "#!/bin/bash\n" +
		`printf '{"id":"q_stale","question":"never answered"}' > "$AFM_STAGE_DIR/planning.q_stale.question.json"` + "\n" +
		"sleep 1.5\n" +
		"cat > \"$AFM_STAGE_DIR/plan.md\" <<'EOF'\n" +
		"## Tasks\n- [ ] do it\n\n## Assumptions\n- none\n\n## Acceptance Criteria\n- [ ] works\nEOF\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{{
		ID:          "s1",
		Name:        "S1",
		Agents:      []flow.AgentType{flow.AgentPlanning},
		Interactive: true,
		Command:     scriptPath,
	}}
	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stateFile := filepath.Join(runDir, "state.json")

	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	runOrchestratorAsync(ctx, t, orch, cancel)

	// First confirm the poller actually won the race and parked the stage in
	// awaiting_user_input (proving this test exercises the intended
	// scenario, not a no-op).
	waitForStatus(t, stateFile, "s1", state.StatusAwaitingUserInput, 3*time.Second)

	// Headless auto-approve (no DashboardURL) synchronously cascades planning
	// straight past AwaitingApproval in the same event-loop tick, so that
	// status may never be observable at 50ms poll granularity — assert on
	// "did NOT get stuck at awaiting_user_input" instead of one exact
	// downstream status.
	deadline := time.Now().Add(5 * time.Second)
	var last state.StageStatus
	for time.Now().Before(deadline) {
		rs, err := tryLoadStateJSON(stateFile)
		if err == nil {
			last = rs.Stages["s1"].Status
			if last != state.StatusAwaitingUserInput {
				return // progressed past the stuck state — fix confirmed
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("stage s1 stuck at %s — planning never progressed despite a stale abandoned question", last)
}
