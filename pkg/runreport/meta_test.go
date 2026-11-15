package runreport

import (
	"testing"
	"time"

	"github.com/akopichin/afm/contracts/accounting"
	"github.com/akopichin/afm/contracts/agentcomm"
	"github.com/akopichin/afm/contracts/agentjobs"
	"github.com/akopichin/afm/contracts/assistants"
	"github.com/akopichin/afm/contracts/state"
)

func task(id, profile string, scope assistants.Scope, title string) *state.TaskState {
	return &state.TaskState{Task: assistants.Task{
		ID: assistants.TaskID(id), Scope: scope, ProfileID: assistants.ProfileID(profile), Title: title,
	}}
}

func agentsByID(meta accounting.AnalyticsMeta) map[string]accounting.AgentMeta {
	out := map[string]accounting.AgentMeta{}
	for _, a := range meta.Agents {
		out[a.ID] = a
	}
	return out
}

func TestOfflineMetaStagesIncludeRuntimeStageAndUnknownStart(t *testing.T) {
	src := &Sources{
		RunID: "r1",
		State: state.RunState{
			StageOrder: []string{"build", "added-later"},
			Stages: map[string]state.StageState{
				"build":       {Timing: state.StageTiming{Incomplete: true}},
				"added-later": {},
			},
		},
		FirstStart: map[string]time.Time{"build": fixtureStart},
	}

	meta := OfflineMeta(src)

	if len(meta.Stages) != 2 || meta.Stages[1].ID != "added-later" || meta.Stages[1].Title != "added-later" {
		t.Fatalf("stages = %+v, want the registry order including the runtime stage", meta.Stages)
	}
	if !meta.Stages[0].StartedAt.Equal(fixtureStart) || !meta.Stages[0].StartUnknown {
		t.Errorf("build = %+v, want the first start and StartUnknown", meta.Stages[0])
	}
	if !meta.Stages[1].StartedAt.IsZero() || meta.Stages[1].StartUnknown {
		t.Errorf("a never-started complete stage = %+v", meta.Stages[1])
	}
	if !meta.LiveSince.IsZero() {
		t.Errorf("LiveSince = %v, an offline read has nothing alive", meta.LiveSince)
	}
	if a := agentsByID(meta)["stage:added-later"]; a.Kind != accounting.AgentKindStage || a.Title != "added-later" {
		t.Errorf("stage agent = %+v", a)
	}
}

func TestOfflineMetaParticipantsByTask(t *testing.T) {
	rs := state.RunState{StageOrder: []string{"build"}, Stages: map[string]state.StageState{"build": {}}}
	rs.Assistants.Tasks = map[assistants.TaskID]*state.TaskState{
		"t-helper": task("t-helper", "reviewer", assistants.ScopeStage, "Review"),
		"t-nested": task("t-nested", "tester", assistants.ScopeStage, ""),
		"t-run":    task("t-run", "qa", assistants.ScopeRun, "Standing QA"),
		"t-legacy": task("t-legacy", "ops", assistants.ScopeRun, "Ops"),
	}
	// t-run registered under its own id; t-legacy only under the pre-task-id name run:<profile>.
	rs.Assistants.Mesh.Participants = map[agentcomm.ParticipantID]*agentcomm.Participant{
		"run:t-run": {Kind: agentcomm.KindRunAgent},
		"run:ops":   {Kind: agentcomm.KindRunAgent},
	}

	agents := agentsByID(OfflineMeta(&Sources{RunID: "r1", State: rs}))

	want := map[string]struct {
		kind, title, task string
	}{
		"helper:t-helper": {accounting.AgentKindHelper, "Review", "t-helper"},
		"helper:t-nested": {accounting.AgentKindHelper, "tester", "t-nested"}, // no title: the profile
		"run:t-run":       {accounting.AgentKindRunAgent, "Standing QA", "t-run"},
		"run:ops":         {accounting.AgentKindRunAgent, "Ops", "t-legacy"},
	}
	for id, w := range want {
		got, ok := agents[id]
		if !ok || got.Kind != w.kind || got.Title != w.title || got.TaskID != w.task {
			t.Errorf("%s = %+v (present %v), want %+v", id, got, ok, w)
		}
	}
	if _, wrong := agents["run:t-legacy"]; wrong {
		t.Error("the legacy run agent must keep its run:<profile> identity")
	}
}

func TestOfflineMetaAssistantIdentity(t *testing.T) {
	absent := OfflineMeta(&Sources{RunID: "r1", State: state.RunState{}})
	if a, ok := agentsByID(absent)["side:r1"]; !ok || a.Kind != accounting.AgentKindAssistant || a.Title != "Assistant" {
		t.Errorf("without a registered side agent: %+v", absent.Agents)
	}

	rs := state.RunState{}
	rs.Assistants.Mesh.Participants = map[agentcomm.ParticipantID]*agentcomm.Participant{
		"side:custom": {Kind: agentcomm.KindSideAgent},
	}
	registered := OfflineMeta(&Sources{RunID: "r1", State: rs})
	agents := agentsByID(registered)
	if _, ok := agents["side:custom"]; !ok {
		t.Errorf("a registered side agent must be used: %+v", registered.Agents)
	}
	if _, dup := agents["side:r1"]; dup {
		t.Error("the Assistant must appear once")
	}
}

func TestBuildFactsKeepsAssistantOutsideRunTotal(t *testing.T) {
	_, runDir, sideDir := newAfmRoot(t, "flow-20261101-100000", "build")
	writeJobs(t, runDir, agentjobs.JobMeta{JobID: "j1", StageID: "build", AgentID: "stage:build", AgentKind: accounting.AgentKindStage})
	writeJobs(t, sideDir, agentjobs.JobMeta{JobID: "a1", AgentID: "side:flow-20261101-100000", AgentKind: accounting.AgentKindAssistant})
	src, err := Load(runDir, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}

	sel := buildFacts(src).Select(accounting.Filter{Scope: accounting.FilterAll}, src.AsOf)

	if got := sel.Totals().Jobs; got != 1 {
		t.Errorf("run total jobs = %d, want only the stage job", got)
	}
	if out := sel.Outside(); out == nil || out.Jobs != 1 {
		t.Errorf("outside the run total = %+v, want the Assistant job", out)
	}
}
