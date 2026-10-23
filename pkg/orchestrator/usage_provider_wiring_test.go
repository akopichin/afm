package orchestrator

import (
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
)

// usageProviderCases — как единый resolver конфига определяет провайдера usage-limit команды: bare
// codex-as-claude и любой codex-recipe делят провайдера "codex", команда по умолчанию — "claude".
var usageProviderCases = []struct {
	name string
	cmd  string // "" — команда по умолчанию из client.command
	want string
}{
	{"bare codex-as-claude", "codex-as-claude", "codex"},
	{"codex recipe", "fast-codex", "codex"},
	{"default command", "", "claude"},
}

func orchWithUsageProviders(t *testing.T) *Orchestrator {
	t.Helper()
	cfg := config.Default()
	cfg.Docker.Agents = map[string]config.AgentRecipe{"fast-codex": {Type: config.RecipeTypeCodex}}
	o := orchWithReasoning(t, "")
	o.opts.Config = cfg
	return o
}

func providerOf(t *testing.T, r executor.Runner) string {
	t.Helper()
	e, ok := r.(*executor.Executor)
	if !ok {
		t.Fatalf("runner is not *executor.Executor: %T", r)
	}
	return e.ProviderConfig()
}

// Каждая продакшн-фабрика executor'а обязана передать провайдера в executor.Config: иначе usage-limit,
// пойманный на этом пути, вернётся без Provider и очередь ожидания квоты не сможет сгруппировать запуски.
func TestExecutorFactories_StampUsageProvider(t *testing.T) {
	for _, tc := range usageProviderCases {
		t.Run(tc.name, func(t *testing.T) {
			o := orchWithUsageProviders(t)
			stage := flow.Stage{ID: "build", Command: tc.cmd}
			interactive := stage
			interactive.Interactive = true

			if got := providerOf(t, o.runnerFor(stage, "implementation")); got != tc.want {
				t.Errorf("runnerFor non-interactive provider = %q, want %q", got, tc.want)
			}
			if got := providerOf(t, o.runnerFor(interactive, "implementation")); got != tc.want {
				t.Errorf("runnerFor interactive provider = %q, want %q", got, tc.want)
			}
			if got := providerOf(t, o.runnerForFallback(stage, "implementation")); got != tc.want {
				t.Errorf("runnerForFallback provider = %q, want %q", got, tc.want)
			}
			verifyCmd := tc.cmd
			if verifyCmd == "" {
				verifyCmd = o.opts.Config.Client.Command
			}
			if got := o.runnerForVerify(stage, verifyCmd).ProviderConfig(); got != tc.want {
				t.Errorf("runnerForVerify provider = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHelperExecutorConfig_StampsUsageProvider(t *testing.T) {
	for _, tc := range usageProviderCases {
		t.Run(tc.name, func(t *testing.T) {
			o, _ := newAssistantsTestOrchestrator(t, true)
			cfg := config.Default()
			cfg.Docker.Agents = map[string]config.AgentRecipe{"fast-codex": {Type: config.RecipeTypeCodex}}
			o.opts.Config = cfg
			helperCmd := tc.cmd
			if helperCmd == "" {
				helperCmd = cfg.Client.Command
			}
			if got := o.helperExecutorConfig(assistants.TaskID("t-provider"), helperCmd).Provider; got != tc.want {
				t.Errorf("helper provider = %q, want %q", got, tc.want)
			}
		})
	}
}
