package orchestrator_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

// TestIntegration_GlobalPromptReachesAssembledPrompt verifies that
// Options.GlobalPrompt (set from Flow.Prompt at the CLI entrypoint)
// reaches the assembled system prompt of a stage via prompts.Build.
func TestIntegration_GlobalPromptReachesAssembledPrompt(t *testing.T) {
	stages := []flow.Stage{
		{ID: "s1", Name: "S1", Description: "do thing", Agents: []flow.AgentType{flow.AgentPlanning}},
	}

	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	var capturedPrompt string
	base := mockRunner(t, mockPlanningScript)
	runner := &promptCapturingRunner{
		delegate: base,
		onPlanning: func(prompt string) {
			capturedPrompt = prompt
		},
	}

	orch := orchestrator.New(orchestrator.Options{
		RunDir:       runDir,
		Stages:       stages,
		Store:        store,
		Config:       config.Default(),
		Prompts:      orchestrator.DefaultPrompts(),
		Runner:       runner,
		GlobalPrompt: "Always write commit messages in Russian.",
	})

	cancel := autoApprove(orch)
	defer cancel()

	ctx, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()

	if err := orch.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(capturedPrompt, "<global_prompt>") {
		t.Errorf("assembled prompt missing <global_prompt> block:\n%s", capturedPrompt)
	}
	if !strings.Contains(capturedPrompt, "Always write commit messages in Russian.") {
		t.Errorf("assembled prompt missing GlobalPrompt content:\n%s", capturedPrompt)
	}
}
