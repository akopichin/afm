package main

import (
	"os"
	"testing"

	"github.com/akopichin/afm-engine/testkit/engine"
	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/contracts/flow"
	"github.com/akopichin/afm/contracts/lifecyclehooks"
)

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
	if err := engine.ResolveLifecycleEnv(t.TempDir(), hooks); err != nil {
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
