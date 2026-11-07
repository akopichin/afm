package main

import (
	implorchestrator "github.com/akopichin/afm-engine/testkit/orchestrator"
	implstate "github.com/akopichin/afm-engine/testkit/state"

	orchestrator "github.com/akopichin/afm/contracts/control"

	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/akopichin/afm-engine/testkit/engine"
	"github.com/akopichin/afm/contracts/assistants"
	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/contracts/flow"
	"github.com/akopichin/afm/contracts/state"
	"github.com/akopichin/afm/pkg/server"
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
	got := engine.ResolveCollaboration(cfg, f)
	if got.Enabled {
		t.Error("Enabled = true, want false: AFM_COLLABORATION=0 must beat flow.collaboration.max_depth")
	}
}

// TestResolveCollaboration_DepthSourceReported — the resolved depth names which layer actually set it:
// flow > project > global > default.
func TestResolveCollaboration_DepthSourceReported(t *testing.T) {
	three, five, two := 3, 5, 2

	t.Run("default when nothing set", func(t *testing.T) {
		got := engine.ResolveCollaboration(config.Config{}, nil)
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
		got := engine.ResolveCollaboration(cfg, nil)
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
		got := engine.ResolveCollaboration(cfg, nil)
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
		got := engine.ResolveCollaboration(cfg, f)
		if got.Depth.Source != assistants.DepthSourceFlow || got.Depth.MaxDepth != two {
			t.Errorf("got %+v, want source=flow, depth=2", got.Depth)
		}
	})
}

// TestResolveCollaborationMode — mode precedence is flow > config (already merged: project over global) >
// default team, and it is a pure dashboard value: neither AFM_COLLABORATION=0 nor collaboration.enabled:false
// nor max_depth may change it.
func TestResolveCollaborationMode(t *testing.T) {
	team, assist := assistants.CollaborationModeTeam, assistants.CollaborationModeAssistants
	off, depth := false, 0
	flowMode := func(m *assistants.CollaborationMode) *flow.Flow {
		return &flow.Flow{Collaboration: &flow.FlowCollaboration{Mode: m}}
	}
	cases := []struct {
		name   string
		cfg    config.Config
		flow   *flow.Flow
		envOff bool
		want   assistants.CollaborationMode
	}{
		{"default without config and flow", config.Config{}, nil, false, team},
		{"flow without collaboration section", config.Config{}, &flow.Flow{}, false, team},
		{"flow collaboration without mode", config.Config{}, flowMode(nil), false, team},
		{"config assistants", config.Config{Collaboration: config.CollaborationConfig{Mode: &assist}}, nil, false, assist},
		{"config team", config.Config{Collaboration: config.CollaborationConfig{Mode: &team}}, nil, false, team},
		{"flow team beats config assistants", config.Config{Collaboration: config.CollaborationConfig{Mode: &assist}}, flowMode(&team), false, team},
		{"flow assistants beats config team", config.Config{Collaboration: config.CollaborationConfig{Mode: &team}}, flowMode(&assist), false, assist},
		{"flow assistants without config", config.Config{}, flowMode(&assist), false, assist},
		{"env AFM_COLLABORATION=0 keeps default", config.Config{}, nil, true, team},
		{"env AFM_COLLABORATION=0 keeps config mode", config.Config{Collaboration: config.CollaborationConfig{Mode: &assist}}, nil, true, assist},
		{"env AFM_COLLABORATION=0 keeps flow mode", config.Config{}, flowMode(&assist), true, assist},
		{"enabled false keeps config mode", config.Config{Collaboration: config.CollaborationConfig{Enabled: &off, Mode: &assist}}, nil, false, assist},
		{"enabled false keeps default", config.Config{Collaboration: config.CollaborationConfig{Enabled: &off}}, nil, false, team},
		{"max_depth 0 keeps mode", config.Config{Collaboration: config.CollaborationConfig{MaxDepth: &depth, Mode: &assist}}, nil, false, assist},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.envOff {
				t.Setenv("AFM_COLLABORATION", "0")
			}
			if got := engine.ResolveCollaborationMode(tc.cfg, tc.flow); got != tc.want {
				t.Errorf("resolveCollaborationMode = %q, want %q", got, tc.want)
			}
		})
	}
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
			if got := engine.ResolveCollaboration(cfg, nil).Enabled; got != tc.wantCollab {
				t.Errorf("engine.ResolveCollaboration.Enabled = %v, want %v", got, tc.wantCollab)
			}
			if got := engine.SideAgentEnabled(cfg, nil); got != tc.wantSide {
				t.Errorf("sideAgentEnabled = %v, want %v", got, tc.wantSide)
			}
		})
	}
}

// TestRecovery_TreeRestoredBeforeOrchestratorNew is the cmd/afm half of T2.7: proves startCollaboration's
// policy resolution+durable recording (the one piece of "restore collaboration state" that exists at this
// stage — task tree/quotas/continuations are already restored earlier, by state.Open's replay, which
// executeFlow calls before engine.StartCollaboration too) completes and is DURABLE before the point in executeFlow
// where orchestrator.New is constructed.
//
// This cannot call the real orchestrator.New (constructing one needs a full Options fixture disproportionate
// to this test's purpose) — instead it uses the SAME state.SetAppendControlHook seam the pkg/state T2.2/T2.4
// crash-simulation tests use to observe the exact moment the policy record becomes durable, and records a
// stand-in marker for "orchestrator.New runs here" at the point executeFlow's real source calls it (right
// after engine.StartCollaboration returns — see run.go). The ordering assertion is real; the "orchestrator.New"
// step itself is a documented stand-in, not a live call into pkg/orchestrator.
func TestRecovery_TreeRestoredBeforeOrchestratorNew(t *testing.T) {
	dir := t.TempDir()
	store, err := implstate.Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	defer store.Close()

	var order []string
	implstate.SetAppendControlHook(func(c implstate.Control) {
		if c.Kind == "policy_set" {
			order = append(order, "policy_recorded")
		}
	})
	defer implstate.SetAppendControlHook(nil)

	five := 5
	f := &flow.Flow{Collaboration: &flow.FlowCollaboration{MaxDepth: &five}}
	collab := engine.StartCollaboration(store, config.Config{}, f)
	// Mirrors run.go: `orch = orchestrator.New(orchOpts)` is the very next meaningful step in executeFlow
	// after `collab := engine.StartCollaboration(store, cfg, f)`.
	order = append(order, "orchestrator_new")

	if !collab.Policy.Enabled {
		t.Fatal("collab.Policy.Enabled = false, want true (default ON)")
	}
	if len(order) != 2 || order[0] != "policy_recorded" || order[1] != "orchestrator_new" {
		t.Fatalf("order = %v, want [policy_recorded, orchestrator_new]", order)
	}
	if rev := store.Snapshot().Assistants.Policy.Revision; rev == "" {
		t.Fatal("policy was not durably recorded by the time engine.StartCollaboration returned")
	}
}

// Docker preparation must not prepare any profile or saved task command for a capability that is off: no helper
// is added or woken then.
func TestHelperCommands_EmptyWhenDisabled(t *testing.T) {
	off := false
	cfg := config.Config{Collaboration: config.CollaborationConfig{
		Enabled:  &off,
		Profiles: map[string]config.ProfileConfig{"reviewer": {Command: "frontend-reviewer"}},
	}}
	if got := engine.HelperCommands(cfg, nil, []string{"saved-reviewer"}); got != nil {
		t.Fatalf("engine.HelperCommands(disabled) = %v, want nil", got)
	}
}

// Configured profile commands and saved task commands come back sorted and deduplicated. A profile without a
// command runs its stage's command or client.command, which the projection already has.
func TestHelperCommands_ProfilesAndSavedTasks(t *testing.T) {
	cfg := config.Config{Collaboration: config.CollaborationConfig{
		Profiles: map[string]config.ProfileConfig{
			"reviewer":  {Command: "frontend-reviewer"},
			"reviewer2": {Command: "frontend-reviewer"},
			"inherits":  {Command: ""},
		},
	}}
	got := engine.HelperCommands(cfg, nil, []string{"saved-reviewer", "frontend-reviewer"})
	if want := []string{"frontend-reviewer", "saved-reviewer"}; !slices.Equal(got, want) {
		t.Fatalf("helperCommands = %v, want %v", got, want)
	}
}

// run and validate build one registry: the builtin default is there without any config, and a configured
// profile with its id replaces it as a whole.
func TestBuildProfileRegistry_BuiltinDefault(t *testing.T) {
	reg, err := engine.BuildProfileRegistry(config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := reg.Get(assistants.DefaultProfileID); !ok || p.Command != "" || p.Instructions == "" {
		t.Fatalf("builtin default = %+v, %v; want a role without a command", p, ok)
	}

	reg, err = engine.BuildProfileRegistry(config.Config{Collaboration: config.CollaborationConfig{
		Profiles: map[string]config.ProfileConfig{"default": {Command: "my-cli"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := reg.Get(assistants.DefaultProfileID); p.Command != "my-cli" || p.Instructions != "" || p.Title != "" {
		t.Fatalf("configured default = %+v, want the configured profile only, no builtin field kept", p)
	}
}

// profileOnTheWire is one profile of GET /api/assistant-profiles as the dashboard reads it.
type profileOnTheWire struct {
	ID             string `json:"id"`
	Command        string `json:"command"`
	CommandSource  string `json:"command_source"`
	RuntimeContext string `json:"runtime_context"`
	Available      bool   `json:"available"`
}

// The dashboard lists every profile as it would run where it is attached: a role-only profile runs the stage's
// command, or client.command for a stage without one and for a run agent; a profile's own command wins.
func TestAssistantProfilesAPI_ResolvesWhereAttached(t *testing.T) {
	t.Setenv("AFM_IN_DOCKER", "1") // container trust: every command may run as a helper
	stages := []flow.Stage{{ID: "a"}, stageWithCommand("b", "fake-recipe"), {ID: "s", Script: "true"}}
	store, err := implstate.Open(t.TempDir(), []string{"a", "b", "s"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	wrapper := assistants.RuntimeContextWrapper
	cfg := config.Default()
	cfg.Collaboration.Profiles = map[string]config.ProfileConfig{
		"reviewer": {Instructions: "Review."},
		"external": {Command: "/opt/ext-as-claude.sh", RuntimeContext: &wrapper},
	}
	f := &flow.Flow{Name: "api", Stages: stages}
	collab := engine.StartCollaboration(store, cfg, f)
	orch := implorchestrator.New(implorchestrator.Options{
		RunDir: t.TempDir(), Stages: stages, Store: store, Config: cfg, Prompts: implorchestrator.DefaultPrompts(),
		AssistantsEnabled: collab.Policy.Enabled, AssistantsRegistry: collab.Registry,
	})
	handler := server.New(server.Config{Store: store, AssistantProfiles: profilesService(collab, orch)}).Handler()
	get := func(query string) (int, map[string]string) {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/assistant-profiles"+query, nil))
		var resp struct {
			Profiles []profileOnTheWire `json:"profiles"`
		}
		_ = json.NewDecoder(w.Body).Decode(&resp)
		got := map[string]string{}
		for _, p := range resp.Profiles {
			got[p.ID] = fmt.Sprintf("%s %s %s %t", p.Command, p.CommandSource, p.RuntimeContext, p.Available)
		}
		return w.Code, got
	}

	for query, want := range map[string]map[string]string{
		"?stage_id=a": {"default": "claude client isolated true", "reviewer": "claude client isolated true", "external": "/opt/ext-as-claude.sh profile wrapper true"},
		"?stage_id=b": {"default": "fake-recipe stage isolated true", "reviewer": "fake-recipe stage isolated true", "external": "/opt/ext-as-claude.sh profile wrapper true"},
		"":            {"default": "claude client isolated true", "reviewer": "claude client isolated true", "external": "/opt/ext-as-claude.sh profile wrapper true"},
	} {
		code, got := get(query)
		if code != http.StatusOK || !maps.Equal(got, want) {
			t.Errorf("GET %q = %d %v, want 200 %v", query, code, got, want)
		}
	}
	for query, want := range map[string]int{"?stage_id=nope": http.StatusNotFound, "?stage_id=s": http.StatusBadRequest} {
		if code, _ := get(query); code != want {
			t.Errorf("GET %q = %d, want %d", query, code, want)
		}
	}
}

// ---- T4: preflight of declared assistant profile names + disabled-collaboration warning ----

func newCollabTestStore(t *testing.T) *implstate.Store {
	t.Helper()
	dir := t.TempDir()
	store, err := implstate.Open(dir, []string{"s1"})
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
	collab := engine.StartCollaboration(newCollabTestStore(t), cfg, f)
	if !collab.Policy.Enabled {
		t.Fatal("setup: collaboration must be enabled by default")
	}

	err := engine.CheckDeclaredAssistants(f, collab)
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
	collab := engine.StartCollaboration(newCollabTestStore(t), cfg, f)

	if err := engine.CheckDeclaredAssistants(f, collab); err != nil {
		t.Fatalf("checkDeclaredAssistants: %v", err)
	}
}

// The builtin default needs no config: a stage and the team may name it in a run without any profiles.
func TestPreflight_BuiltinDefaultProfile(t *testing.T) {
	t.Setenv("AFM_COLLABORATION", "1") // with collaboration off the preflight only warns and would prove nothing
	f := &flow.Flow{
		Stages: []flow.Stage{{ID: "s1", Assistants: flow.StageAssistants{{Profile: "default"}}}},
		Team:   flow.Team{{ID: "helper", Profile: "default", Title: "Helper"}},
	}
	store := newCollabTestStore(t)
	collab := engine.StartCollaboration(store, config.Config{}, f)
	if !collab.Policy.Enabled || collab.Registry == nil {
		t.Fatal("setup: collaboration must be enabled with a registry")
	}

	if err := engine.CheckDeclaredAssistants(f, collab); err != nil {
		t.Fatalf("checkDeclaredAssistants: %v", err)
	}
	if err := engine.CheckFlowTeam(store, f.Team, collab); err != nil {
		t.Fatalf("checkFlowTeam: %v", err)
	}
}

func TestCheckDeclaredAssistants_DisabledCollaborationWarnsNotErrors(t *testing.T) {
	off := false
	cfg := config.Config{Collaboration: config.CollaborationConfig{Enabled: &off}}
	f := &flow.Flow{Stages: []flow.Stage{
		{ID: "s1", Assistants: flow.StageAssistants{{Profile: "reviewer"}}},
	}}
	collab := engine.StartCollaboration(newCollabTestStore(t), cfg, f)
	if collab.Policy.Enabled {
		t.Fatal("setup: collaboration must be disabled")
	}

	var err error
	stderr := captureStderr(t, func() { err = engine.CheckDeclaredAssistants(f, collab) })
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
			collab := engine.StartCollaboration(newCollabTestStore(t), cfg, f)

			var err error
			stderr := captureStderr(t, func() { err = engine.CheckDeclaredAssistants(f, collab) })
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
	collab := engine.StartCollaboration(store, cfg, f)

	err := engine.CheckFlowTeam(store, f.Team, collab)
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
	collab := engine.StartCollaboration(store, cfg, f)
	orch := implorchestrator.New(implorchestrator.Options{
		RunDir: t.TempDir(), Stages: f.Stages, Store: store, Config: config.Default(), Prompts: implorchestrator.DefaultPrompts(),
		AssistantsEnabled: true, AssistantsRegistry: collab.Registry,
	})
	if _, err := orch.AddRunAgent(context.Background(), orchestrator.AddRunAgentRequest{
		OperationID: "team/qa", ProfileID: "qa", Title: "QA", Actor: state.Actor{Kind: "system", ID: "flow"},
	}); err != nil {
		t.Fatalf("AddRunAgent: %v", err)
	}

	removed := config.Config{Collaboration: config.CollaborationConfig{
		Profiles: map[string]config.ProfileConfig{"other": {Command: "codex-as-claude"}},
	}}
	if err := engine.CheckFlowTeam(store, f.Team, engine.StartCollaboration(store, removed, f)); err != nil {
		t.Fatalf("engine.CheckFlowTeam on resume: %v, want nil (the saved agent keeps its snapshot)", err)
	}
	f.Team = append(f.Team, flow.TeamMember{ID: "ops", Profile: "qa", Title: "Ops"})
	if err := engine.CheckFlowTeam(store, f.Team, engine.StartCollaboration(store, removed, f)); err == nil {
		t.Fatal("checkFlowTeam: a new member with a missing profile must still fail")
	}
}

func TestCheckFlowTeam_DisabledCollaborationWarns(t *testing.T) {
	off := false
	cfg := config.Config{Collaboration: config.CollaborationConfig{Enabled: &off}}
	f := &flow.Flow{Team: flow.Team{{ID: "qa", Profile: "ghost", Title: "QA"}}, Stages: []flow.Stage{{ID: "s1"}}}
	store := newCollabTestStore(t)
	collab := engine.StartCollaboration(store, cfg, f)

	var err error
	stderr := captureStderr(t, func() { err = engine.CheckFlowTeam(store, f.Team, collab) })
	if err != nil {
		t.Fatalf("checkFlowTeam: %v, want nil (disabled collaboration must not block the run)", err)
	}
	if !strings.Contains(stderr, `"team"`) || !strings.Contains(stderr, "collaboration is disabled") {
		t.Fatalf("stderr = %q, want a warning about the team", stderr)
	}
}
