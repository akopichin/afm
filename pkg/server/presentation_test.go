package server

import (
	implbus "github.com/akopichin/afm-engine/testkit/orchestrator/bus"
	bus "github.com/akopichin/afm/contracts/events"

	orchestrator "github.com/akopichin/afm/contracts/control"

	"github.com/akopichin/afm/contracts/graph"

	"github.com/akopichin/afm/contracts/state"

	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	implstate "github.com/akopichin/afm-engine/testkit/state"
	"github.com/akopichin/afm/contracts/agentcomm"
	"github.com/akopichin/afm/contracts/assistants"
	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/contracts/flow"
	"github.com/gorilla/websocket"
)

// resolveBuiltinNames is the resolver without configured names: a built-in name, then the file name.
func resolveBuiltinNames(command string) string { return config.ResolveCommandName(nil, command) }

var reviewerNames = map[string]string{"codex-as-claude": "Codex Reviewer"}

// newRunServer serves runDir whose store holds stageIDs, with cfg's names and services.
func newRunServer(t *testing.T, runDir string, stageIDs []string, cfg Config) *Server {
	t.Helper()
	store, err := implstate.Open(runDir, stageIDs)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	cfg.RunDir, cfg.Store = runDir, store
	if cfg.UIBus == nil {
		cfg.UIBus = implbus.NewUIBus()
	}
	srv := New(cfg)
	srv.useFakes(fakeStageActions{}, nil)
	return srv
}

func getJSON(t *testing.T, srv *Server, path string) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: HTTP %d (body=%s)", path, w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return resp
}

func TestPresentVerifyData_NamesTheAIStepExecutorOnACopy(t *testing.T) {
	data := map[string]any{"step": 2, "kind": "agent", "command": "codex-as-claude", "name": "Code review"}

	got, ok := presentVerifyData(string(bus.EventVerifyResult), data, resolveBuiltinNames).(map[string]any)

	if !ok || got["command_name"] != "Codex" || got["command"] != "codex-as-claude" || got["name"] != "Code review" {
		t.Fatalf("presented data = %v, want the raw command, the step name and command_name Codex", got)
	}
	if _, mutated := data["command_name"]; mutated {
		t.Fatal("presentVerifyData changed the shared event data")
	}
}

// verify_tree_modified carries no kind: only an AI step fires it, so it is named like one.
func TestPresentVerifyData_TreeModifiedIsAnAIStep(t *testing.T) {
	data := map[string]any{"step": 1, "command": "claude"}

	got, _ := presentVerifyData(string(bus.EventVerifyTreeModified), data, resolveBuiltinNames).(map[string]any)

	if got["command_name"] != "Claude Code" {
		t.Fatalf("command_name = %v, want Claude Code", got["command_name"])
	}
}

func TestPresentVerifyData_LeavesOtherDataUnchanged(t *testing.T) {
	cases := map[string]struct {
		eventType string
		data      any
	}{
		"shell step":         {string(bus.EventVerifyStarted), map[string]any{"step": 1, "kind": "shell", "command": "make test"}},
		"not a verify event": {"agent_note", map[string]any{"command": "claude"}},
		"non-map data":       {string(bus.EventVerifyResult), "text"},
		"no command":         {string(bus.EventVerifyResult), map[string]any{"kind": "agent"}},
	}
	for name, c := range cases {
		if got := presentVerifyData(c.eventType, c.data, resolveBuiltinNames); !reflect.DeepEqual(got, c.data) {
			t.Errorf("%s: presented data = %v, want unchanged %v", name, got, c.data)
		}
	}
}

// unknown_command_name is always sent: the dashboard's fallback labels need a prefix even without config.
func TestStatus_UnknownCommandName(t *testing.T) {
	if got := getJSON(t, newTestServer(t, Config{}), "/api/status")["unknown_command_name"]; got != config.DefaultUnknownCommandName {
		t.Errorf("unknown_command_name without config = %v, want %q", got, config.DefaultUnknownCommandName)
	}
	if got := getJSON(t, newTestServer(t, Config{UnknownCommandName: "test"}), "/api/status")["unknown_command_name"]; got != "test" {
		t.Errorf("configured unknown_command_name = %v, want test", got)
	}
}

func TestMesh_ParticipantsCarryCommandName(t *testing.T) {
	fake := newFakeMeshActions()
	fake.participants = []agentcomm.Participant{
		{ID: "user", Kind: agentcomm.KindUser, Scope: agentcomm.ScopeRun, Reachable: true},
		{ID: "stage:qa", Kind: agentcomm.KindStageAgent, Scope: agentcomm.ScopeStage, Alias: "qa", DisplayName: "QA",
			StageID: "qa", Reachable: true, Command: "codex-as-claude"},
		{ID: "side:run", Kind: agentcomm.KindSideAgent, Scope: agentcomm.ScopeRun, Alias: "assistant",
			DisplayName: "Assistant", Reachable: true, Command: "claude"},
	}
	srv := newTestServer(t, Config{Mesh: fake, CommandNames: reviewerNames})

	participants, _ := getJSON(t, srv, "/api/mesh/participants")["participants"].([]any)
	if len(participants) != 3 {
		t.Fatalf("participants = %v, want 3 entries", participants)
	}
	user, _ := participants[0].(map[string]any)
	if _, ok := user["command_name"]; ok {
		t.Errorf("user entry = %v, want no command_name without a command", user)
	}
	qa, _ := participants[1].(map[string]any)
	if qa["command_name"] != "Codex Reviewer" || qa["command"] != "codex-as-claude" || qa["display_name"] != "QA" ||
		qa["alias"] != "qa" || qa["id"] != "stage:qa" {
		t.Errorf("qa entry = %v, want the participant fields plus command_name Codex Reviewer", qa)
	}
	side, _ := participants[2].(map[string]any)
	if side["command_name"] != "Claude Code" || side["display_name"] != "Assistant" {
		t.Errorf("side entry = %v, want Assistant run by Claude Code", side)
	}
}

func TestMesh_AttachAndSend_ParticipantCarriesCommandName(t *testing.T) {
	fake := newFakeMeshActions()
	fake.attachParticipant = &agentcomm.Participant{ID: "helper:task-r1", Kind: agentcomm.KindHelper, Scope: agentcomm.ScopeStage,
		Alias: "architecture", DisplayName: "Architecture", Reachable: true, Command: "codex-as-claude"}
	srv := newTestServer(t, Config{Mesh: fake, CommandNames: reviewerNames})

	w, resp := doMeshJSON(t, srv, http.MethodPost, "/api/mesh/attach-and-send",
		map[string]any{"request_id": "r1", "profile_id": "architecture", "stage_id": testStageID}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	participant, _ := resp["participant"].(map[string]any)
	if participant["command_name"] != "Codex Reviewer" || participant["display_name"] != "Architecture" || participant["alias"] != "architecture" {
		t.Fatalf("participant = %v, want Architecture run by Codex Reviewer", participant)
	}
}

func TestProfiles_CommandNameUsesConfiguredNames(t *testing.T) {
	svc := fakeAssistantProfiles{profiles: []assistants.ProfileView{
		{ID: "reviewer", Title: "Reviewer", Command: "codex-as-claude", CommandSource: assistants.CommandSourceProfile, Available: true},
	}}
	srv := newTestServer(t, Config{AssistantProfiles: svc, CommandNames: reviewerNames})

	profiles, _ := getJSON(t, srv, "/api/assistant-profiles")["profiles"].([]any)
	reviewer, _ := profiles[0].(map[string]any)
	if reviewer["command_name"] != "Codex Reviewer" || reviewer["command"] != "codex-as-claude" {
		t.Fatalf("reviewer = %v, want command codex-as-claude named Codex Reviewer", reviewer)
	}
}

func TestSideAgent_StateCarriesCommandName(t *testing.T) {
	cases := map[string]struct {
		names map[string]string
		want  string
	}{
		"built-in":   {nil, "Claude Code"},
		"configured": {map[string]string{"claude": "House Claude"}, "House Claude"},
	}
	for name, c := range cases {
		srv := newTestServer(t, Config{SideAgent: &fakeSideAgent{convID: "run-1", command: "claude"}, CommandNames: c.names})
		resp := getJSON(t, srv, "/api/side-agent")
		if resp["command_name"] != c.want || resp["command"] != "claude" {
			t.Errorf("%s: side agent state = %v, want command claude named %q", name, resp, c.want)
		}
	}
}

func TestGraph_PreviewAndCommitCarryCommandName(t *testing.T) {
	fake := &fakeGraphActions{preview: orchestrator.AddStagePreview{
		Plan: graph.InsertionPlan{Stage: flow.Stage{ID: "n", Command: "codex-as-claude", Prompt: "p"}, Order: []string{"n"}},
	}}
	srv := newTestServer(t, Config{GraphActions: fake, CommandNames: reviewerNames})

	for _, path := range []string{"/api/run/graph/preview", "/api/run/graph/commit"} {
		w := postGraph(t, srv, path, addStageBody("append", "n", "codex-as-claude", map[string]any{"request_id": "r1"}))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: HTTP %d (body=%s)", path, w.Code, w.Body.String())
		}
		var resp struct {
			Stage map[string]any `json:"stage"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("%s: decode: %v", path, err)
		}
		if resp.Stage["command_name"] != "Codex Reviewer" || resp.Stage["command"] != "codex-as-claude" {
			t.Errorf("%s: stage = %v, want command codex-as-claude named Codex Reviewer", path, resp.Stage)
		}
	}
}

// Each stage's verify view keeps its own step name; only an AI step names an executor.
func TestStatus_VerifyViewCarriesStepNameAndExecutor(t *testing.T) {
	runDir := t.TempDir()
	appendVerifyNotice(t, runDir, "impl", string(bus.EventVerifyResult),
		map[string]any{"step": 2, "kind": "agent", "command": "codex-as-claude", "name": "Code review", "verdict": "pass"})
	appendVerifyNotice(t, runDir, "qa", string(bus.EventVerifyStarted),
		map[string]any{"step": 1, "kind": "shell", "command": "make test", "name": "Unit tests"})
	srv := newRunServer(t, runDir, []string{"impl", "qa"}, Config{CommandNames: reviewerNames})

	verifyByStage := map[string]map[string]any{}
	stages, _ := getJSON(t, srv, "/api/status")["stages"].([]any)
	for _, raw := range stages {
		stage, _ := raw.(map[string]any)
		verifyByStage[stage["id"].(string)], _ = stage["verify"].(map[string]any)
	}

	want := map[string]map[string]any{
		"impl": {"step": float64(2), "kind": "agent", "command": "codex-as-claude", "command_name": "Codex Reviewer", "name": "Code review", "phase": "pass"},
		"qa":   {"step": float64(1), "kind": "shell", "command": "make test", "name": "Unit tests", "phase": "running"},
	}
	if !reflect.DeepEqual(verifyByStage, want) {
		t.Fatalf("verify views = %v, want %v", verifyByStage, want)
	}
}

// A legacy notice has no name: the view omits it and the dashboard falls back to "Verify step N".
func TestBuildStageViews_LegacyVerifyNoticeHasNoName(t *testing.T) {
	runDir := t.TempDir()
	appendVerifyNotice(t, runDir, "build", string(bus.EventVerifyStarted), map[string]any{"step": 1, "kind": "agent", "command": "codex"})
	rs := state.RunState{StageOrder: []string{"build"}, Stages: map[string]state.StageState{"build": {Status: state.StatusRunning}}}

	v := findStage(buildStageViews(rs, runDir, nil, nil), "build").Verify

	if v == nil || v.Name != "" || v.Kind != "agent" {
		t.Fatalf("verify view = %+v, want kind agent without a name", v)
	}
}

// The live stream and the history replay present one verify notice identically, and neither rewrites the
// journal nor the event the bus shares between subscribers.
func TestVerifyEvents_LiveAndHistoryCarryTheSameNames(t *testing.T) {
	runDir := t.TempDir()
	data := map[string]any{"verification_id": "v1", "step": 2, "kind": "agent", "command": "codex-as-claude",
		"name": "Code review", "verdict": "pass", "reason": "ok"}
	appendVerifyNotice(t, runDir, "impl", string(bus.EventVerifyResult), data)
	noticesPath := filepath.Join(runDir, "notices.jsonl")
	before, err := os.ReadFile(noticesPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := newRunServer(t, runDir, []string{"impl"}, Config{CommandNames: reviewerNames})

	history := historyVerifyData(t, srv)
	live := liveVerifyData(t, srv, bus.Event{Type: bus.EventVerifyResult, StageID: "impl", Data: data})

	for _, key := range []string{"command_name", "name", "command"} {
		if history[key] != live[key] {
			t.Errorf("%s: history %v, live %v", key, history[key], live[key])
		}
	}
	if live["command_name"] != "Codex Reviewer" || live["name"] != "Code review" {
		t.Errorf("live data = %v, want Code review by Codex Reviewer", live)
	}
	if after, _ := os.ReadFile(noticesPath); !bytes.Equal(before, after) {
		t.Errorf("notices.jsonl changed:\nbefore %s\nafter  %s", before, after)
	}
	if _, mutated := data["command_name"]; mutated {
		t.Error("the published event data gained command_name")
	}
}

func historyVerifyData(t *testing.T, srv *Server) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/events?stage=impl", nil))
	var events []feedEvent
	if err := json.Unmarshal(w.Body.Bytes(), &events); err != nil {
		t.Fatalf("decode /api/events: %v", err)
	}
	for _, e := range events {
		if e.Type == string(bus.EventVerifyResult) {
			data, _ := e.Data.(map[string]any)
			return data
		}
	}
	t.Fatalf("no verify_result in history %s", w.Body.String())
	return nil
}

// liveVerifyData publishes ev until the WebSocket client receives it: the server subscribes to the bus only
// after the handshake the client waits for.
func liveVerifyData(t *testing.T, srv *Server, ev bus.Event) map[string]any {
	t.Helper()
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	frames := make(chan []byte, 1)
	go func() {
		_, msg, err := conn.ReadMessage()
		if err == nil {
			frames <- msg
		}
	}()
	deadline := time.After(5 * time.Second)
	for {
		writableBus(srv).Publish(ev)
		select {
		case msg := <-frames:
			var got struct {
				Type string         `json:"type"`
				Data map[string]any `json:"data"`
			}
			if err := json.Unmarshal(msg, &got); err != nil || got.Type != string(ev.Type) {
				t.Fatalf("live frame = %s, want %s", msg, ev.Type)
			}
			return got.Data
		case <-deadline:
			t.Fatal("no live frame within 5s")
		case <-time.After(20 * time.Millisecond):
		}
	}
}
