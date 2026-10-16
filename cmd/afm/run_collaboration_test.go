package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

func writeCollabConfigYAML(t *testing.T, dir, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestResolveCollaboration_EnvOffBeatsFlowDepth — T1.4: AFM_COLLABORATION=0 must win even when flow.yaml
// tries to raise max_depth. A flow cannot re-enable a feature switched off by env/config.
func TestResolveCollaboration_EnvOffBeatsFlowDepth(t *testing.T) {
	t.Setenv("AFM_COLLABORATION", "0")
	five := 5
	cfg := config.Config{} // config says nothing; env wins
	f := &flow.Flow{Collaboration: &flow.FlowCollaboration{MaxDepth: &five}}
	got := resolveCollaboration(cfg, f)
	if got.Enabled {
		t.Error("Enabled = true, want false: AFM_COLLABORATION=0 must beat flow.collaboration.max_depth")
	}
}

// TestResolveCollaboration_DepthSourceReported — the resolved depth names which layer actually set it:
// flow > project > global > default.
func TestResolveCollaboration_DepthSourceReported(t *testing.T) {
	three, five, two := 3, 5, 2

	t.Run("default when nothing set", func(t *testing.T) {
		got := resolveCollaboration(config.Config{}, nil)
		if got.Depth.Source != assistants.DepthSourceDefault {
			t.Errorf("Source = %q, want %q", got.Depth.Source, assistants.DepthSourceDefault)
		}
	})

	t.Run("global layer", func(t *testing.T) {
		base := t.TempDir()
		g := writeCollabConfigYAML(t, filepath.Join(base, "g"), "collaboration:\n  max_depth: 3\n")
		cfg, err := config.LoadFrom(g, "")
		if err != nil {
			t.Fatal(err)
		}
		got := resolveCollaboration(cfg, nil)
		if got.Depth.Source != assistants.DepthSourceGlobal || got.Depth.MaxDepth != three {
			t.Errorf("got %+v, want source=global, depth=3", got.Depth)
		}
	})

	t.Run("project layer", func(t *testing.T) {
		base := t.TempDir()
		p := writeCollabConfigYAML(t, filepath.Join(base, "p"), "collaboration:\n  max_depth: 5\n")
		cfg, err := config.LoadFrom("", p)
		if err != nil {
			t.Fatal(err)
		}
		got := resolveCollaboration(cfg, nil)
		if got.Depth.Source != assistants.DepthSourceProject || got.Depth.MaxDepth != five {
			t.Errorf("got %+v, want source=project, depth=5", got.Depth)
		}
	})

	t.Run("flow layer wins over config layers", func(t *testing.T) {
		base := t.TempDir()
		g := writeCollabConfigYAML(t, filepath.Join(base, "g"), "collaboration:\n  max_depth: 3\n")
		p := writeCollabConfigYAML(t, filepath.Join(base, "p"), "collaboration:\n  max_depth: 5\n")
		cfg, err := config.LoadFrom(g, p)
		if err != nil {
			t.Fatal(err)
		}
		f := &flow.Flow{Collaboration: &flow.FlowCollaboration{MaxDepth: &two}}
		got := resolveCollaboration(cfg, f)
		if got.Depth.Source != assistants.DepthSourceFlow || got.Depth.MaxDepth != two {
			t.Errorf("got %+v, want source=flow, depth=2", got.Depth)
		}
	})
}

// TestResolveCollaboration_IndependentFromSideAgent — collaboration and side_agent are independent
// switches; resolving one must not be influenced by the other (§3.3 table).
func TestResolveCollaboration_IndependentFromSideAgent(t *testing.T) {
	tr, fl := true, false
	cases := []struct {
		name          string
		collaboration *bool
		sideAgent     *bool
		wantCollab    bool
		wantSide      bool
	}{
		{"both on", &tr, &tr, true, true},
		{"both off", &fl, &fl, false, false},
		{"collaboration on, side_agent off", &tr, &fl, true, false},
		{"collaboration off, side_agent on", &fl, &tr, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Config{
				Collaboration: config.CollaborationConfig{Enabled: tc.collaboration},
				SideAgent:     config.SideAgentConfig{Enabled: tc.sideAgent},
			}
			if got := resolveCollaboration(cfg, nil).Enabled; got != tc.wantCollab {
				t.Errorf("resolveCollaboration.Enabled = %v, want %v", got, tc.wantCollab)
			}
			if got := sideAgentEnabled(cfg, nil); got != tc.wantSide {
				t.Errorf("sideAgentEnabled = %v, want %v", got, tc.wantSide)
			}
		})
	}
}

// TestRecovery_TreeRestoredBeforeOrchestratorNew is the cmd/afm half of T2.7: proves startCollaboration's
// policy resolution+durable recording (the one piece of "restore collaboration state" that exists at this
// stage — task tree/quotas/continuations are already restored earlier, by state.Open's replay, which
// executeFlow calls before startCollaboration too) completes and is DURABLE before the point in executeFlow
// where orchestrator.New is constructed.
//
// This cannot call the real orchestrator.New (constructing one needs a full Options fixture disproportionate
// to this test's purpose) — instead it uses the SAME state.SetAppendControlHook seam the pkg/state T2.2/T2.4
// crash-simulation tests use to observe the exact moment the policy record becomes durable, and records a
// stand-in marker for "orchestrator.New runs here" at the point executeFlow's real source calls it (right
// after startCollaboration returns — see run.go). The ordering assertion is real; the "orchestrator.New"
// step itself is a documented stand-in, not a live call into pkg/orchestrator.
func TestRecovery_TreeRestoredBeforeOrchestratorNew(t *testing.T) {
	dir := t.TempDir()
	store, err := state.Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	defer store.Close()

	var order []string
	state.SetAppendControlHook(func(c state.Control) {
		if c.Kind == "policy_set" {
			order = append(order, "policy_recorded")
		}
	})
	defer state.SetAppendControlHook(nil)

	five := 5
	f := &flow.Flow{Collaboration: &flow.FlowCollaboration{MaxDepth: &five}}
	collab := startCollaboration(store, config.Config{}, f)
	// Mirrors run.go: `orch = orchestrator.New(orchOpts)` is the very next meaningful step in executeFlow
	// after `collab := startCollaboration(store, cfg, f)`.
	order = append(order, "orchestrator_new")

	if !collab.policy.Enabled {
		t.Fatal("collab.policy.Enabled = false, want true (default ON)")
	}
	if len(order) != 2 || order[0] != "policy_recorded" || order[1] != "orchestrator_new" {
		t.Fatalf("order = %v, want [policy_recorded, orchestrator_new]", order)
	}
	if rev := store.Snapshot().Assistants.Policy.Revision; rev == "" {
		t.Fatal("policy was not durably recorded by the time startCollaboration returned")
	}
}

// TestAssistantProfileCommands_EmptyWhenDisabled: T8.5's Docker-mount wiring must not try to prepare any
// profile command for a capability that is off.
func TestAssistantProfileCommands_EmptyWhenDisabled(t *testing.T) {
	off := false
	cfg := config.Config{Collaboration: config.CollaborationConfig{
		Enabled:  &off,
		Profiles: map[string]config.ProfileConfig{"reviewer": {Command: "frontend-reviewer"}},
	}}
	if got := assistantProfileCommands(cfg, nil); got != nil {
		t.Fatalf("assistantProfileCommands(disabled) = %v, want nil", got)
	}
}

// TestAssistantProfileCommands_DedupedWhenEnabled: configured profile commands come back deduplicated, and a
// profile with an empty Command (inherits the client default, already scanned separately) is skipped.
func TestAssistantProfileCommands_DedupedWhenEnabled(t *testing.T) {
	cfg := config.Config{Collaboration: config.CollaborationConfig{
		Profiles: map[string]config.ProfileConfig{
			"reviewer":  {Command: "frontend-reviewer"},
			"reviewer2": {Command: "frontend-reviewer"},
			"inherits":  {Command: ""},
		},
	}}
	got := assistantProfileCommands(cfg, nil)
	if len(got) != 1 || got[0] != "frontend-reviewer" {
		t.Fatalf("assistantProfileCommands = %v, want exactly [frontend-reviewer]", got)
	}
}
