package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

const teamRunStageID = "work"

// newTeamRunOrch opens (or, on resume, reopens) the run journal in runDir and builds a real orchestrator with
// one long script stage and the given team. The store closes after Run has returned (LIFO cleanups).
func newTeamRunOrch(t *testing.T, runDir string, team flow.Team, resumed bool) (*orchestrator.Orchestrator, *state.Store) {
	t.Helper()
	store, err := state.Open(runDir, []string{teamRunStageID})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err := store.EnsurePolicyRecorded(assistants.DepthPolicy{MaxDepth: 3, Source: assistants.DepthSourceDefault, Revision: "rev-1"}, state.Actor{Kind: "system"}); err != nil {
		t.Fatalf("EnsurePolicyRecorded: %v", err)
	}
	reg, err := assistants.BuildRegistry(nil, []assistants.Profile{
		{ID: "qa", Title: "QA", Command: "helper-cmd"},
		{ID: "reviewer", Title: "Reviewer", Command: "helper-cmd"},
	}, nil, "claude", func(string) (bool, string, assistants.ReadonlyEnforcement) {
		return true, "", assistants.ReadonlyEnforcementOS
	})
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, RootDir: t.TempDir(), Store: store,
		Stages: []flow.Stage{{ID: teamRunStageID, Name: "Work", Script: "sleep 30"}},
		Config: config.Default(), Prompts: orchestrator.DefaultPrompts(),
		Clock:             newFakeClockWithPump(t),
		Resumed:           resumed,
		AssistantsEnabled: true, AssistantsRegistry: reg,
		Team: team,
	})
	return orch, store
}

// runAgentAliases lists the @names of the run's run agents.
func runAgentAliases(rs state.RunState) []string {
	var out []string
	for _, p := range rs.Assistants.Mesh.Participants {
		if strings.HasPrefix(string(p.ID), "run:") {
			out = append(out, p.Alias)
		}
	}
	return out
}

// TestTeam_SurvivesInterruptAndResume: the team exists by the time the first stage runs; Ctrl+C and a resume
// with an edited team: (one member changed, one added) neither duplicates nor conflicts — the saved agent is
// kept with a warning and only the new member is created.
func TestTeam_SurvivesInterruptAndResume(t *testing.T) {
	runDir := t.TempDir()
	stateFile := filepath.Join(runDir, "state.json")

	orch, store := newTeamRunOrch(t, runDir, flow.Team{
		{ID: "arch", Profile: "reviewer", Title: "Architecture", Instruction: "Review for simplicity."},
		{ID: "qa", Profile: "qa", Title: "QA"},
	}, false)
	ctx, interrupt := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = orch.Run(ctx); close(done) }()
	waitForStatus(t, stateFile, teamRunStageID, state.StatusRunning, 10*time.Second)
	if got := runAgentAliases(store.Snapshot()); len(got) != 2 {
		t.Fatalf("run agents when the stage started = %v, want the 2 team agents", got)
	}
	interrupt() // Ctrl+C
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after the interrupt")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	resumed, store2 := newTeamRunOrch(t, runDir, flow.Team{
		{ID: "arch", Profile: "reviewer", Title: "Architecture v2", Instruction: "Review for simplicity."},
		{ID: "qa", Profile: "qa", Title: "QA"},
		{ID: "ops", Profile: "qa", Title: "Ops"},
	}, true)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Second)
	runOrchestratorAsync(ctx2, t, resumed, cancel2)
	// The team is created in declaration order and ops comes last: once it exists, the resumed initTeam has
	// handled every member (a duplicate of an earlier one would already be there too).
	deadline := time.Now().Add(10 * time.Second)
	for len(runAgentAliases(store2.Snapshot())) < 3 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}

	rs := store2.Snapshot()
	aliases := runAgentAliases(rs)
	if len(aliases) != 3 || len(rs.Assistants.Tasks) != 3 {
		t.Fatalf("after resume: aliases %v, %d tasks; want architecture, qa, ops once each", aliases, len(rs.Assistants.Tasks))
	}
	for _, want := range []string{"architecture", "qa", "ops"} {
		if !strings.Contains(" "+strings.Join(aliases, " ")+" ", " "+want+" ") {
			t.Errorf("aliases %v: missing @%s", aliases, want)
		}
	}
	notices, err := os.ReadFile(filepath.Join(runDir, "notices.jsonl"))
	if err != nil {
		t.Fatalf("read notices: %v", err)
	}
	if n := strings.Count(string(notices), `"type":"team_warning"`); n != 1 || !strings.Contains(string(notices), `"member_id":"arch"`) {
		t.Fatalf("notices = %s\nwant exactly one team_warning, for arch", notices)
	}
}
