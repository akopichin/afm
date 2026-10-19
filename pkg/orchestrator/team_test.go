package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// teamAgent returns the run agent a team declaration created: its task and its mesh participant.
func teamAgent(t *testing.T, rs state.RunState, declID string) (*state.TaskState, *agentcomm.Participant) {
	t.Helper()
	taskID := deriveTaskID(teamOperationID(declID))
	tk := rs.Assistants.Tasks[taskID]
	if tk == nil {
		t.Fatalf("team member %q: no task %s", declID, taskID)
	}
	p := rs.Assistants.Mesh.Participants[meshRunParticipantID(taskID)]
	if p == nil {
		t.Fatalf("team member %q: no participant for task %s", declID, taskID)
	}
	return tk, p
}

func TestInitTeam_CreatesIdleRunAgentsInDeclarationOrder(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	stubPassingHelper(o)
	o.opts.Team = flow.Team{
		{ID: "arch", Profile: "reviewer", Title: "Architecture", Instruction: "Review for simplicity."},
		{ID: "qa", Profile: "qa", Title: "QA"},
		{ID: "qa-second", Profile: "qa", Title: "QA"}, // same title: the later one gets the suffix
	}

	mustInitTeam(t, o)

	rs := store.Snapshot()
	if n := len(rs.Assistants.Tasks); n != 3 {
		t.Fatalf("Tasks = %d, want 3", n)
	}
	wantAlias := map[string]string{"arch": "architecture", "qa": "qa", "qa-second": "qa-2"}
	for id, alias := range wantAlias {
		tk, p := teamAgent(t, rs, id)
		if p.Kind != agentcomm.KindRunAgent || p.Alias != alias {
			t.Errorf("%s: participant = %+v, want run agent @%s", id, p, alias)
		}
		if tk.Scope != assistants.ScopeRun || tk.Status != assistants.StatusQueued {
			t.Errorf("%s: task scope/status = %s/%s, want an idle run agent", id, tk.Scope, tk.Status)
		}
	}
	if tk, _ := teamAgent(t, rs, "arch"); tk.Instruction != "Review for simplicity." || tk.ProfileID != "reviewer" {
		t.Errorf("arch task = %+v, want the declared profile and instruction", tk.Task)
	}
}

// TestInitTeam_ResumeKeepsSavedAgents simulates Ctrl+C → edit flow.yaml → resume: a fresh process over the
// same run journal, with one member changed, one removed and one added. The saved agents are reused (no
// duplicate, no conflict), the changed one keeps its saved version with a durable warning, the removed one
// stays, and only the new id is created.
func TestInitTeam_ResumeKeepsSavedAgents(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	stubPassingHelper(o)
	o.opts.Team = flow.Team{
		{ID: "arch", Profile: "reviewer", Title: "Architecture", Instruction: "Review for simplicity."},
		{ID: "qa", Profile: "qa", Title: "QA"},
	}
	mustInitTeam(t, o)
	runDir := o.opts.RunDir
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	resumed, store2 := reopenAssistantsTestOrchestrator(t, o)
	resumed.opts.Team = flow.Team{
		{ID: "arch", Profile: "qa", Title: "Arch v2", Instruction: "Something else."},
		{ID: "ops", Profile: "qa", Title: "QA"},
	}
	mustInitTeam(t, resumed)
	mustInitTeam(t, resumed) // a second pass (replay) changes nothing either

	rs := store2.Snapshot()
	if n := len(rs.Assistants.Tasks); n != 3 {
		t.Fatalf("Tasks = %d, want 3 (arch, qa kept; ops added)", n)
	}
	arch, archP := teamAgent(t, rs, "arch")
	if arch.Title != "Architecture" || arch.ProfileID != "reviewer" || arch.Instruction != "Review for simplicity." || archP.Alias != "architecture" {
		t.Errorf("arch = %+v (@%s), want the saved version", arch.Task, archP.Alias)
	}
	teamAgent(t, rs, "qa") // removed from flow.yaml, still there until the run ends
	if _, p := teamAgent(t, rs, "ops"); p.Alias != "qa-2" {
		t.Errorf("ops alias = %q, want qa-2 (qa is taken by the kept agent)", p.Alias)
	}

	notices, err := os.ReadFile(filepath.Join(runDir, "notices.jsonl"))
	if err != nil {
		t.Fatalf("read notices: %v", err)
	}
	var warnings []string
	for _, line := range strings.Split(string(notices), "\n") {
		if strings.Contains(line, `"type":"team_warning"`) {
			warnings = append(warnings, line)
		}
	}
	if len(warnings) != 2 || !strings.Contains(warnings[0], `"member_id":"arch"`) || !strings.Contains(warnings[0], "using the saved version") {
		t.Fatalf("team warnings = %q, want one per pass for arch only", warnings)
	}
}

func TestInitTeam_DisabledCollaborationAddsNothing(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, false)
	o.opts.Team = flow.Team{{ID: "qa", Profile: "qa", Title: "QA"}}

	mustInitTeam(t, o)

	if n := len(store.Snapshot().Assistants.Tasks); n != 0 {
		t.Fatalf("Tasks = %d, want 0 with collaboration disabled", n)
	}
}

func TestInitTeam_UnknownProfileIsSkipped(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	o.opts.Team = flow.Team{
		{ID: "ghost", Profile: "no-such-profile", Title: "Ghost"},
		{ID: "qa", Profile: "qa", Title: "QA"},
	}

	mustInitTeam(t, o)

	rs := store.Snapshot()
	if n := len(rs.Assistants.Tasks); n != 1 {
		t.Fatalf("Tasks = %d, want only qa", n)
	}
	teamAgent(t, rs, "qa")
	if o.loadFatal() != nil {
		t.Fatalf("an unknown profile must not end the run: %v", o.loadFatal())
	}
}

func mustInitTeam(t *testing.T, o *Orchestrator) {
	t.Helper()
	if err := o.initTeam(context.Background()); err != nil {
		t.Fatalf("initTeam: %v", err)
	}
}

// A journal that cannot be written while adding a member stops startup instead of being skipped.
func TestInitTeam_StorageFailureOnAddStopsStartup(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	o.opts.Team = flow.Team{{ID: "qa", Profile: "qa", Title: "QA"}}
	store.CloseEventsLogForTest()

	err := o.initTeam(context.Background())
	var se *state.StorageError
	if !errors.As(err, &se) {
		t.Fatalf("initTeam = %v, want a storage error", err)
	}
}

// A saved member whose committed operation cannot be read back stops startup too.
func TestInitTeam_StorageFailureOnReadStopsStartup(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	o.opts.Team = flow.Team{{ID: "qa", Profile: "qa", Title: "QA"}}
	mustInitTeam(t, o)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	resumed, store2 := reopenAssistantsTestOrchestrator(t, o)
	store2.CloseEventsLogForTest()

	if err := resumed.initTeam(context.Background()); err == nil {
		t.Fatal("initTeam with an unreadable journal: want an error, got nil")
	}
	if _, err := TeamMembersToCreate(store2, resumed.opts.Team); err == nil {
		t.Fatal("TeamMembersToCreate with an unreadable journal: want an error, got nil")
	}
}

// TestTeamMembersToCreate: preflight checks only the members that still need an agent — a saved one keeps
// its snapshot even if its profile is gone from config now.
func TestTeamMembersToCreate(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	o.opts.Team = flow.Team{{ID: "qa", Profile: "qa", Title: "QA"}}
	mustInitTeam(t, o)

	got, err := TeamMembersToCreate(store, flow.Team{
		{ID: "qa", Profile: "removed-profile", Title: "QA"},
		{ID: "ops", Profile: "qa", Title: "Ops"},
	})
	if err != nil {
		t.Fatalf("TeamMembersToCreate: %v", err)
	}
	if len(got) != 1 || got[0].ID != "ops" {
		t.Fatalf("TeamMembersToCreate = %+v, want only ops", got)
	}
}

// TestInitTeam_RetriesTakenAlias: a dashboard "Add run agent" that takes the same @name between the team
// member's read of the roster and its commit does not drop the member — it is added again with a free name.
func TestInitTeam_RetriesTakenAlias(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	o.opts.Team = flow.Team{{ID: "arch", Profile: "reviewer", Title: "Architecture"}}
	raced := false // the hook also runs inside the dashboard's own attach: race only once
	o.testAfterAttachSnapshot = func() {
		if raced {
			return
		}
		raced = true
		if _, err := o.AddRunAgent(context.Background(), AddRunAgentRequest{
			OperationID: "dashboard-op", ProfileID: "qa", Title: "Architecture", Actor: state.Actor{Kind: "user"},
		}); err != nil {
			t.Errorf("dashboard AddRunAgent: %v", err)
		}
	}

	mustInitTeam(t, o)

	_, p := teamAgent(t, store.Snapshot(), "arch")
	if p.Alias != "architecture-2" {
		t.Fatalf("team alias = %q, want architecture-2 (the dashboard agent won @architecture)", p.Alias)
	}
}

// teamCountingRunner records how many tasks the run had when the first stage agent was spawned.
type teamCountingRunner struct {
	countingBlockingRunner
	store        *state.Store
	tasksAtSpawn chan int
}

func (r *teamCountingRunner) RunAgent(ctx context.Context, cmd, stageName, prompt, logFile string) error {
	select {
	case r.tasksAtSpawn <- len(r.store.Snapshot().Assistants.Tasks):
	default:
	}
	return r.countingBlockingRunner.RunAgent(ctx, cmd, stageName, prompt, logFile)
}

// TestRun_TeamExistsBeforeReviewPauseResume: a review pause saved in "resuming" respawns its owner stage at
// startup; the team must already exist by then, like for every other way a stage starts.
func TestRun_TeamExistsBeforeReviewPauseResume(t *testing.T) {
	base, store := newAssistantsTestOrchestrator(t, true)
	runner := &teamCountingRunner{store: store, tasksAtSpawn: make(chan int, 1)}
	o := New(Options{
		RunDir: base.opts.RunDir, Stages: []flow.Stage{{ID: assistantsTestStageID, Name: "s", Agents: []flow.AgentType{flow.AgentAuto}}},
		Store: store, Config: config.Default(), Prompts: DefaultPrompts(), Runner: runner, Resumed: true,
		AssistantsEnabled: true, AssistantsRegistry: base.opts.AssistantsRegistry,
		Team: flow.Team{{ID: "qa", Profile: "qa", Title: "QA"}},
	})
	setPausedFrom(t, store, assistantsTestStageID, state.StatusRunning)
	stageDir := filepath.Join(o.opts.RunDir, assistantsTestStageID)
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "autonomous.flag"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteNotesPauseMarker(o.opts.RunDir, state.PauseMarker{
		Version: 1, OperationID: "op-review", State: state.PauseStateResuming, Mode: state.PauseModeCancel,
		Owned: []state.PauseOwner{{ID: assistantsTestStageID, ResumeKind: kindAutonomous}},
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = o.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	select {
	case n := <-runner.tasksAtSpawn:
		if n != 1 {
			t.Fatalf("tasks when the resumed owner spawned = %d, want the team agent already there", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the review-pause owner was never respawned")
	}
}
