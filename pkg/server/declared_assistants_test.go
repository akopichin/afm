package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	implstate "github.com/akopichin/afm-engine/testkit/state"
	"github.com/akopichin/afm/contracts/assistants"
	"github.com/akopichin/afm/contracts/state"
)

type fakeStageProvider map[string]StageConfig

func (p fakeStageProvider) StageConfigs() map[string]StageConfig { return p }

func rawStageStatus(t *testing.T, cfg Config) map[string]any {
	t.Helper()
	srv := newTestServer(t, cfg)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/status: %d", w.Code)
	}
	var body struct {
		Stages []map[string]any `json:"stages"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Stages) != 1 {
		t.Fatalf("decode: %v, stages=%d", err, len(body.Stages))
	}
	return body.Stages[0]
}

var declaredFixture = []DeclaredAssistant{
	{Profile: "reviewer", Name: "Reviewer", Trigger: assistants.TriggerOneShot, Mode: assistants.ModeAdvisory},
	{Profile: "watcher", Name: "Watch", Trigger: assistants.TriggerOnMessage, Mode: assistants.ModeRequired, LoopEvery: "5m"},
}

func TestStatus_DeclaredAssistantsExactKeysAndOrder(t *testing.T) {
	stage := rawStageStatus(t, Config{
		AssistantProfiles: fakeAssistantProfiles{},
		Stages:            fakeStageProvider{testStageID: {Assistants: declaredFixture}},
	})
	raw, ok := stage["declared_assistants"].([]any)
	if !ok || len(raw) != 2 {
		t.Fatalf("declared_assistants = %#v", stage["declared_assistants"])
	}
	want := []map[string]any{
		{"profile": "reviewer", "name": "Reviewer", "trigger": "one_shot", "mode": "advisory"},
		{"profile": "watcher", "name": "Watch", "trigger": "on_message", "mode": "required", "loop_every": "5m"},
	}
	for i, item := range raw {
		if !reflect.DeepEqual(item, any(want[i])) {
			t.Fatalf("declared_assistants[%d] = %#v, want %#v", i, item, want[i])
		}
	}
}

func TestStatus_DeclaredAssistantsHiddenWithoutCollaboration(t *testing.T) {
	stage := rawStageStatus(t, Config{
		Stages: fakeStageProvider{testStageID: {Assistants: declaredFixture}},
	})
	if _, present := stage["declared_assistants"]; present {
		t.Fatalf("declared_assistants leaked with collaboration off: %#v", stage["declared_assistants"])
	}
}

func TestStatus_DeclaredAssistantsOmittedWhenEmpty(t *testing.T) {
	stage := rawStageStatus(t, Config{
		AssistantProfiles: fakeAssistantProfiles{},
		Stages:            fakeStageProvider{testStageID: {}},
	})
	if _, present := stage["declared_assistants"]; present {
		t.Fatalf("empty declarations must omit the key: %#v", stage["declared_assistants"])
	}
}

// timing.started is the durable "stage has ever started" flag the orbit hides on: Retry keeps it, replay restores it.
func TestStageTimingStarted_SurvivesRetryAndReplay(t *testing.T) {
	runDir := t.TempDir()
	store, err := implstate.Open(runDir, []string{testStageID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	started := func(rs state.RunState) bool {
		t.Helper()
		rs.StageOrder = []string{testStageID}
		views := BuildStageViews(rs, runDir, map[string]StageConfig{testStageID: {}}, nil)
		if len(views) != 1 || views[0].Timing == nil {
			t.Fatalf("views = %+v", views)
		}
		return views[0].Timing.Started
	}
	if err := store.BeginRun(); err != nil {
		t.Fatal(err)
	}
	if started(store.Snapshot()) {
		t.Fatal("never-started stage reports timing.started")
	}
	steps := []struct {
		from, to state.StageStatus
		event    string
	}{
		{state.StatusPending, state.StatusRunning, "start_run"},
		{state.StatusRunning, state.StatusFailed, "fail"},
		{state.StatusFailed, state.StatusPending, "retry"},
	}
	for _, step := range steps {
		if err := store.Apply(&state.Transition{StageID: testStageID, From: step.from, To: step.to, Event: step.event}); err != nil {
			t.Fatalf("%s: %v", step.event, err)
		}
		if !started(store.Snapshot()) {
			t.Fatalf("timing.started false after %s", step.event)
		}
	}
	replayed, err := implstate.LoadRunState(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if !started(replayed) {
		t.Fatal("timing.started lost on replay")
	}
}
