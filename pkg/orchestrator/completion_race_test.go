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

func TestIntegration_PollerRaceDoesNotStrandCompletedStage(t *testing.T) {
	runDir := t.TempDir()
	stageDir := filepath.Join(runDir, "s1")
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Leave a stray question for the real poller to detect before the agent writes completion and exits.
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

	// First confirm the poller actually won the race and parked the stage in awaiting_user_input (proving
	// this test exercises the intended scenario, not a no-op).
	waitForStatus(t, stateFile, "s1", state.StatusAwaitingUserInput, 3*time.Second)

	// Headless approval may skip observable intermediate states; assert the stage leaves awaiting_user_input.
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
