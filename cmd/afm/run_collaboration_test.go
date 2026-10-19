package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

// captureStderr redirects os.Stderr for the duration of fn and returns everything written to it — shares
// captureStream with captureStdout (check_test.go).
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	return captureStream(t, &os.Stderr, fn)
}

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
// TestProfilesFromConfig_DelegateCapability — the config `delegate: true` flag must reach the built
// registry's Profile.Capabilities.Delegate, otherwise admitChildren rejects every delegate turn ("this
// profile is not permitted to delegate") and recursive delegation is unreachable in production.
func TestProfilesFromConfig_DelegateCapability(t *testing.T) {
	profiles := profilesFromConfig(map[string]config.ProfileConfig{
		"lead":   {Command: "codex-as-claude", Delegate: true},
		"helper": {Command: "codex-as-claude"},
	})
	got := map[assistants.ProfileID]bool{}
	for _, p := range profiles {
		got[p.ID] = p.Capabilities.Delegate
	}
	if !got["lead"] {
		t.Error("profile with delegate:true must have Capabilities.Delegate == true")
	}
	if got["helper"] {
		t.Error("profile without delegate must default Capabilities.Delegate == false")
	}
}

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

// ---- T4: preflight of declared assistant profile names + disabled-collaboration warning ----

func newCollabTestStore(t *testing.T) *state.Store {
	t.Helper()
	dir := t.TempDir()
	store, err := state.Open(dir, []string{"s1"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestCheckDeclaredAssistants_UnknownProfileErrors(t *testing.T) {
	cfg := config.Config{Collaboration: config.CollaborationConfig{
		Profiles: map[string]config.ProfileConfig{"reviewer": {Command: "frontend-reviewer"}},
	}}
	f := &flow.Flow{Stages: []flow.Stage{
		{ID: "s1", Assistants: flow.StageAssistants{{Profile: "reviewer"}, {Profile: "ghost"}}},
	}}
	collab := startCollaboration(newCollabTestStore(t), cfg, f)
	if !collab.policy.Enabled {
		t.Fatal("setup: collaboration must be enabled by default")
	}

	err := checkDeclaredAssistants(f, collab)
	if err == nil {
		t.Fatal("checkDeclaredAssistants: want error for unknown profile, got nil")
	}
	if !strings.Contains(err.Error(), "s1") || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("error = %q, want it to name stage %q and profile %q", err.Error(), "s1", "ghost")
	}
}

func TestCheckDeclaredAssistants_KnownProfilesPass(t *testing.T) {
	cfg := config.Config{Collaboration: config.CollaborationConfig{
		Profiles: map[string]config.ProfileConfig{
			"reviewer": {Command: "frontend-reviewer"},
			"qa":       {Command: "qa-bot"},
		},
	}}
	f := &flow.Flow{Stages: []flow.Stage{
		{ID: "s1", Assistants: flow.StageAssistants{{Profile: "reviewer"}, {Profile: "qa"}}},
	}}
	collab := startCollaboration(newCollabTestStore(t), cfg, f)

	if err := checkDeclaredAssistants(f, collab); err != nil {
		t.Fatalf("checkDeclaredAssistants: %v", err)
	}
}

func TestCheckDeclaredAssistants_DisabledCollaborationWarnsNotErrors(t *testing.T) {
	off := false
	cfg := config.Config{Collaboration: config.CollaborationConfig{Enabled: &off}}
	f := &flow.Flow{Stages: []flow.Stage{
		{ID: "s1", Assistants: flow.StageAssistants{{Profile: "reviewer"}}},
	}}
	collab := startCollaboration(newCollabTestStore(t), cfg, f)
	if collab.policy.Enabled {
		t.Fatal("setup: collaboration must be disabled")
	}

	var err error
	stderr := captureStderr(t, func() { err = checkDeclaredAssistants(f, collab) })
	if err != nil {
		t.Fatalf("checkDeclaredAssistants: %v, want nil (disabled collaboration must not block the run)", err)
	}
	if !strings.Contains(stderr, "s1") {
		t.Fatalf("stderr = %q, want a warning naming stage %q", stderr, "s1")
	}
}

func TestCheckDeclaredAssistants_NoStageDeclaresAssistants(t *testing.T) {
	cases := []struct {
		name    string
		enabled *bool
	}{
		{"enabled", nil},
		{"disabled", func() *bool { f := false; return &f }()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Config{Collaboration: config.CollaborationConfig{Enabled: tc.enabled}}
			f := &flow.Flow{Stages: []flow.Stage{{ID: "s1"}}}
			collab := startCollaboration(newCollabTestStore(t), cfg, f)

			var err error
			stderr := captureStderr(t, func() { err = checkDeclaredAssistants(f, collab) })
			if err != nil {
				t.Fatalf("checkDeclaredAssistants: %v, want nil", err)
			}
			if stderr != "" {
				t.Fatalf("stderr = %q, want empty (no stage declares assistants)", stderr)
			}
		})
	}
}

// ---- Этап 6: team: preflight ----

func TestCheckFlowTeam_UnknownProfileErrors(t *testing.T) {
	cfg := config.Config{Collaboration: config.CollaborationConfig{
		Profiles: map[string]config.ProfileConfig{"reviewer": {Command: "frontend-reviewer"}},
	}}
	f := &flow.Flow{
		Team:   flow.Team{{ID: "arch", Profile: "reviewer", Title: "Architecture"}, {ID: "qa", Profile: "ghost", Title: "QA"}},
		Stages: []flow.Stage{{ID: "s1"}},
	}
	store := newCollabTestStore(t)
	collab := startCollaboration(store, cfg, f)

	err := checkFlowTeam(store, f.Team, collab)
	if err == nil || !strings.Contains(err.Error(), `team member "qa"`) || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("checkFlowTeam = %v, want an error naming team member qa and profile ghost", err)
	}
}

// A member whose agent the run already created keeps its saved profile snapshot: removing that profile from
// config must not stop the resume.
func TestCheckFlowTeam_SavedMemberSkipsProfileCheck(t *testing.T) {
	cfg := config.Config{Collaboration: config.CollaborationConfig{
		Profiles: map[string]config.ProfileConfig{"qa": {Command: "codex-as-claude"}},
	}}
	f := &flow.Flow{Team: flow.Team{{ID: "qa", Profile: "qa", Title: "QA"}}, Stages: []flow.Stage{{ID: "s1"}}}
	store := newCollabTestStore(t)
	collab := startCollaboration(store, cfg, f)
	orch := orchestrator.New(orchestrator.Options{
		RunDir: t.TempDir(), Stages: f.Stages, Store: store, Config: config.Default(), Prompts: orchestrator.DefaultPrompts(),
		AssistantsEnabled: true, AssistantsRegistry: collab.registry,
	})
	if _, err := orch.AddRunAgent(context.Background(), orchestrator.AddRunAgentRequest{
		OperationID: "team/qa", ProfileID: "qa", Title: "QA", Actor: state.Actor{Kind: "system", ID: "flow"},
	}); err != nil {
		t.Fatalf("AddRunAgent: %v", err)
	}

	removed := config.Config{Collaboration: config.CollaborationConfig{
		Profiles: map[string]config.ProfileConfig{"other": {Command: "codex-as-claude"}},
	}}
	if err := checkFlowTeam(store, f.Team, startCollaboration(store, removed, f)); err != nil {
		t.Fatalf("checkFlowTeam on resume: %v, want nil (the saved agent keeps its snapshot)", err)
	}
	f.Team = append(f.Team, flow.TeamMember{ID: "ops", Profile: "qa", Title: "Ops"})
	if err := checkFlowTeam(store, f.Team, startCollaboration(store, removed, f)); err == nil {
		t.Fatal("checkFlowTeam: a new member with a missing profile must still fail")
	}
}

func TestCheckFlowTeam_DisabledCollaborationWarns(t *testing.T) {
	off := false
	cfg := config.Config{Collaboration: config.CollaborationConfig{Enabled: &off}}
	f := &flow.Flow{Team: flow.Team{{ID: "qa", Profile: "ghost", Title: "QA"}}, Stages: []flow.Stage{{ID: "s1"}}}
	store := newCollabTestStore(t)
	collab := startCollaboration(store, cfg, f)

	var err error
	stderr := captureStderr(t, func() { err = checkFlowTeam(store, f.Team, collab) })
	if err != nil {
		t.Fatalf("checkFlowTeam: %v, want nil (disabled collaboration must not block the run)", err)
	}
	if !strings.Contains(stderr, `"team"`) || !strings.Contains(stderr, "collaboration is disabled") {
		t.Fatalf("stderr = %q, want a warning about the team", stderr)
	}
}
