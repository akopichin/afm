package orchestrator

import (
	"testing"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
)

// execConfigReasoning reads the reasoning level a runner carries into its executor.Config.
func execConfigReasoning(t *testing.T, r executor.Runner) string {
	t.Helper()
	e, ok := r.(*executor.Executor)
	if !ok {
		t.Fatalf("runner is not *executor.Executor: %T", r)
	}
	return e.ReasoningConfig()
}

func orchWithReasoning(t *testing.T, reasoning string) *Orchestrator {
	t.Helper()
	return &Orchestrator{
		opts: Options{RunDir: t.TempDir(), Config: config.Config{Client: config.ClientConfig{Reasoning: reasoning}}},
		ui:   bus.NewUIBus(),
	}
}

// client.reasoning must reach every author stage runner — the production path for all stage agents. These
// build a real executor.New(cfg); without the forward, reasoning would silently drop for stage agents.
func TestRunnerFor_ForwardsReasoning(t *testing.T) {
	o := orchWithReasoning(t, "high")
	if got := execConfigReasoning(t, o.runnerFor(normalClaudeStage("build"), "implementation")); got != "high" {
		t.Errorf("non-interactive author runner reasoning = %q, want high", got)
	}
	if got := execConfigReasoning(t, o.runnerFor(interactiveClaudeStage("build"), "implementation")); got != "high" {
		t.Errorf("interactive author runner reasoning = %q, want high", got)
	}
	if got := execConfigReasoning(t, o.nonInteractiveRunner(flow.Stage{ID: "b2"}, "implementation")); got != "high" {
		t.Errorf("default-command runner reasoning = %q, want high", got)
	}
}
