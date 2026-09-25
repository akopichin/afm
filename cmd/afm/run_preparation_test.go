package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/lifecyclehooks"
)

func TestNormalizeInteractiveStagesDoesNotMutateFlow(t *testing.T) {
	stages := []flow.Stage{{ID: "interactive", Interactive: true}, {ID: "regular"}}
	headless := normalizeInteractiveStages(stages, false)
	if headless[0].Interactive || headless[1].Interactive {
		t.Fatal("headless stages must be non-interactive")
	}
	withDashboard := normalizeInteractiveStages(stages, true)
	if !withDashboard[0].Interactive || withDashboard[1].Interactive || !stages[0].Interactive {
		t.Fatal("normalization changed the parsed flow or dashboard configuration")
	}
}

func TestRunLifecycleTransportUsesCombinedHookOrder(t *testing.T) {
	t.Setenv("AFM_IN_DOCKER", "1")
	cfg := config.Config{Hooks: []lifecyclehooks.Hook{{ID: "notify"}}}
	f := &flow.Flow{
		Hooks: []lifecyclehooks.Hook{{ID: "notify", Env: map[string]lifecyclehooks.SecretRef{"TOKEN": "file:/host-only/token"}}}, //nolint:gosec // references to nonexistent test sources, not credentials
		Stages: []flow.Stage{{ID: "build", Hooks: []lifecyclehooks.Hook{{
			ID: "audit", Env: map[string]lifecyclehooks.SecretRef{"TOKEN": "file:/host-only/audit", "CHAT": "env:HOST_ONLY_CHAT"}, //nolint:gosec // references to nonexistent test sources
		}}}},
	}
	hooks := combineRunHooks(cfg, f)
	if len(hooks) != 2 || hooks[0].Hook.ID != "notify" || hooks[1].StageID != "build" {
		t.Fatalf("unexpected combined hooks: %+v", hooks)
	}
	t.Setenv(lifecyclehooks.TransportName(0, 0), "notify-value")
	t.Setenv(lifecyclehooks.TransportName(1, 0), "chat-value")
	t.Setenv(lifecyclehooks.TransportName(1, 1), "audit-value")
	t.Setenv(lifecyclehooks.TransportName(99, 0), "unused-value")
	if err := resolveLifecycleEnv(t.TempDir(), hooks); err != nil {
		t.Fatal(err)
	}
	if hooks[0].Hook.ResolvedEnv["TOKEN"] != "notify-value" ||
		hooks[1].Hook.ResolvedEnv["CHAT"] != "chat-value" || hooks[1].Hook.ResolvedEnv["TOKEN"] != "audit-value" {
		t.Fatal("resolved hook values do not match the combined transport indices")
	}
	for _, name := range []string{
		lifecyclehooks.TransportName(0, 0), lifecyclehooks.TransportName(1, 0),
		lifecyclehooks.TransportName(1, 1), lifecyclehooks.TransportName(99, 0),
	} {
		if _, exists := os.LookupEnv(name); exists {
			t.Errorf("transport variable %s still available to child processes", name)
		}
	}
}

func TestRunLifecycleMissingTransportFailsBeforeDispatcher(t *testing.T) {
	t.Setenv("AFM_IN_DOCKER", "1")
	t.Setenv(lifecyclehooks.TransportName(0, 0), "")
	hooks := []lifecyclehooks.RegisteredHook{{Hook: lifecyclehooks.Hook{
		ID: "notify", Env: map[string]lifecyclehooks.SecretRef{"TOKEN": "env:HOST_ONLY_TOKEN"}, //nolint:gosec // test environment variable name
	}}}
	d, err := prepareLifecycleDispatcher(t.TempDir(), lifecyclehooks.DispatcherConfig{}, hooks, nil)
	if err == nil || d != nil {
		t.Fatal("missing secret must fail before constructing a dispatcher")
	}
}

func TestRunLifecycleWithoutEnvSkipsSecretsFile(t *testing.T) {
	t.Setenv("AFM_IN_DOCKER", "")
	root := t.TempDir()
	// A directory cannot be read as secrets.env; hooks without env need not open it.
	if err := os.MkdirAll(filepath.Join(root, ".afm", "secrets.env"), 0755); err != nil {
		t.Fatal(err)
	}
	hooks := []lifecyclehooks.RegisteredHook{{Hook: lifecyclehooks.Hook{ID: "notify"}}}
	if err := resolveLifecycleEnv(root, hooks); err != nil {
		t.Fatal(err)
	}
}
