package main

import (
	implorchestrator "github.com/akopichin/afm-engine/testkit/orchestrator"
	implstate "github.com/akopichin/afm-engine/testkit/state"

	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm-engine/testkit/engine"
	"github.com/akopichin/afm/contracts/assistants"
	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/contracts/flow"
	"github.com/akopichin/afm/contracts/state"
	"github.com/akopichin/afm/pkg/server"
)

// postAttachStatus POSTs an attach to the dashboard handler and returns the HTTP status, so a refusal can be
// asserted too.
func postAttachStatus(t *testing.T, handler http.Handler, stageID string, body map[string]string) int {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+stageID+"/assistants", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://"+req.Host)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w.Code
}

// The on_message helper through the real dashboard API and the agent mesh adapter: refused before its stage
// starts, accepted on a running stage, resting in watching; a stage agent's spawn answers without a message id.
func TestOnMessage_AttachAndSpawnSurfacesOverTheRealStack(t *testing.T) {
	binDir := writeFakeCodexHelper(t, "pass", "unused: no message is ever sent")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	runDir := t.TempDir()
	const stageID = "work"
	store, err := implstate.Open(runDir, []string{stageID})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	enabled := true
	cfg := config.Default()
	cfg.Collaboration = config.CollaborationConfig{
		Enabled:  &enabled,
		Profiles: map[string]config.ProfileConfig{"reviewer": {Title: "Reviewer", Command: "codex-as-claude", Instructions: "Answer questions."}},
	}
	collab := engine.StartCollaboration(store, cfg, &flow.Flow{Name: "on-message"})
	runner := &fakeAutoRunner{acquired: make(chan string, 1), release: make(chan struct{})}
	orch := implorchestrator.New(implorchestrator.Options{
		RunDir: runDir, Stages: []flow.Stage{{ID: stageID, Name: "Work", Agents: []flow.AgentType{flow.AgentAuto}}},
		Store: store, Config: cfg, Prompts: implorchestrator.DefaultPrompts(), Runner: runner,
		AssistantsEnabled: collab.Policy.Enabled, AssistantsRegistry: collab.Registry,
	})
	control := engine.StageControlFor(orch, runDir, store)
	srv := server.New(server.Config{
		RunDir: runDir, Store: store, UIBus: orch.UIBus(), Actions: control, Secondary: control, FlowActions: orch,
		Stages: registryStageProvider{reader: orchStages{orch}}, AssistantActions: assistantActions(collab, orch), AssistantsReader: assistantsReader(collab, orch),
	})
	handler := srv.Handler()

	attach := map[string]string{"request_id": "req-attach", "profile_id": "reviewer", "title": "Cart Q&A", "instruction": "Answer questions.", "trigger": "on_message"}
	if code := postAttachStatus(t, handler, stageID, attach); code != http.StatusConflict {
		t.Fatalf("attach to a stage that has not started: HTTP %d, want 409", code)
	}
	if n := len(store.Snapshot().Assistants.Tasks); n != 0 {
		t.Fatalf("a refused attach left %d tasks", n)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- orch.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-runDone:
		case <-time.After(10 * time.Second):
			t.Error("timed out waiting for orch.Run to return")
		}
	})
	select {
	case <-runner.acquired:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the stage to start")
	}

	if code := postAttachStatus(t, handler, stageID, attach); code != http.StatusOK {
		t.Fatalf("attach to a running stage: HTTP %d, want 200", code)
	}
	spawned, err := agentMeshActions{Mesh: orch}.Spawn(ctx, "req-spawn", "stage:"+stageID, server.MeshSpawnRequest{
		ProfileID: "reviewer", Title: "Spawned Q&A", Instruction: "Answer questions.", Trigger: "on_message",
	})
	if err != nil {
		t.Fatalf("Spawn on_message: %v", err)
	}
	if spawned.MessageID != "" || spawned.TaskID == "" {
		t.Fatalf("spawn receipt = %+v, want a task and no message id", spawned)
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/stages/"+stageID+"/assistants", nil))
	var out struct {
		Tasks []server.AssistantTaskView `json:"tasks"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != http.StatusOK {
		t.Fatalf("GET assistants: HTTP %d, %v, body=%s", w.Code, err, w.Body.String())
	}
	if len(out.Tasks) != 2 {
		t.Fatalf("tasks = %+v, want the attached and the spawned helper", out.Tasks)
	}
	for _, tv := range out.Tasks {
		if tv.Trigger != string(assistants.TriggerOnMessage) || tv.Status != string(assistants.StatusWatching) {
			t.Fatalf("task %s = trigger %q status %q, want on_message resting in watching", tv.ID, tv.Trigger, tv.Status)
		}
		if tv.ActiveRequestID != "" || tv.PendingRequests != 0 || tv.LastRequestID != "" || tv.LastOutcome != "" || tv.Turn != 0 {
			t.Fatalf("task %s = %+v, want an empty request queue before any message", tv.ID, tv)
		}
	}

	_, err = agentMeshActions{Mesh: orch}.Spawn(ctx, "req-bad", "stage:"+stageID, server.MeshSpawnRequest{
		ProfileID: "reviewer", Title: "T", Instruction: "I", Trigger: "checkpoints",
	})
	var rejected *state.OperationRejectedError
	if !errors.As(err, &rejected) || rejected.Code != assistants.CodeInvalidRequest || !strings.Contains(rejected.Reason, "trigger") {
		t.Fatalf("spawn with a watcher trigger: %v, want invalid_request naming the trigger", err)
	}
}
