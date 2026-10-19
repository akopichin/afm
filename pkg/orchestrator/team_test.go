package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
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

	o.initTeam(context.Background())

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
	o.initTeam(context.Background())
	runDir := o.opts.RunDir
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	resumed, store2 := reopenAssistantsTestOrchestrator(t, o)
	resumed.opts.Team = flow.Team{
		{ID: "arch", Profile: "qa", Title: "Arch v2", Instruction: "Something else."},
		{ID: "ops", Profile: "qa", Title: "QA"},
	}
	resumed.initTeam(context.Background())
	resumed.initTeam(context.Background()) // a second pass (replay) changes nothing either

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

	o.initTeam(context.Background())

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

	o.initTeam(context.Background())

	rs := store.Snapshot()
	if n := len(rs.Assistants.Tasks); n != 1 {
		t.Fatalf("Tasks = %d, want only qa", n)
	}
	teamAgent(t, rs, "qa")
	if o.loadFatal() != nil {
		t.Fatalf("an unknown profile must not end the run: %v", o.loadFatal())
	}
}
