package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/server"
	"github.com/akopichin/afm/pkg/state"
)

// newMeshAdapterTestActions is the dashboard's mesh adapter over a real (not running) orchestrator with
// collaboration on and one run-agent role, "reviewer".
func newMeshAdapterTestActions(t *testing.T, stageIDs ...string) (meshActions, *state.Store) {
	t.Helper()
	binDir := writeFakeCodexHelper(t, "pass", "ok")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	runDir := t.TempDir()
	store, err := state.Open(runDir, stageIDs)
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	stages := make([]flow.Stage, len(stageIDs))
	for i, id := range stageIDs {
		stages[i] = flow.Stage{ID: id, Name: id, Agents: []flow.AgentType{flow.AgentAuto}}
	}
	enabled := true
	cfg := config.Default()
	cfg.Collaboration = config.CollaborationConfig{
		Enabled: &enabled,
		Profiles: map[string]config.ProfileConfig{
			"reviewer": {Title: "Reviewer", Command: "codex-as-claude", Instructions: "Review the work."},
		},
	}
	collab := startCollaboration(store, cfg, &flow.Flow{Name: "mesh"})
	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: stages, Store: store, Config: cfg, Prompts: orchestrator.DefaultPrompts(),
		AssistantsEnabled: collab.policy.Enabled, AssistantsRegistry: collab.registry,
	})
	return meshActions{orch}, store
}

// The human's send without a thread names its recipients by alias: one message to all of them in a
// conversation opened in the same operation from the feed it was written in; a retry under the same request
// id is the same message.
func TestMeshActions_SendToSetByAlias(t *testing.T) {
	a, store := newMeshAdapterTestActions(t, "impl", "docs")
	req := server.MeshUserSendRequest{
		MeshSendRequest: server.MeshSendRequest{Recipients: []string{"@impl", "docs"}, Body: "both of you"},
		OriginStage:     "impl",
	}
	r, err := a.Send(context.Background(), "req-set", req)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	th := store.Snapshot().Assistants.Mesh.Threads[agentcomm.ThreadID(r.ThreadID)]
	if r.ThreadID == "" || th == nil || th.OriginStage != "impl" || len(th.Members) != 3 {
		t.Fatalf("receipt %+v, thread %+v: want a new conversation of the human and both stages, shown in impl", r, th)
	}
	again, err := a.Send(context.Background(), "req-set", req)
	if err != nil || again != r {
		t.Fatalf("retry = %+v, %v; want the original receipt %+v", again, err, r)
	}
	if n := len(store.Snapshot().Assistants.Mesh.Messages); n != 1 {
		t.Fatalf("messages = %d, want 1", n)
	}
}

func TestMeshActions_UpdateThreadTitleHTTP_IdempotentAfterLaterEdit(t *testing.T) {
	a, store := newMeshAdapterTestActions(t, "impl")
	thread, err := a.OpenThread(context.Background(), "req-open-topic", server.MeshOpenThreadRequest{
		Scope: "run", Title: "Original", Members: []string{"stage:impl"},
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	if err := a.CloseThread(context.Background(), "req-close-topic", string(thread.ID)); err != nil {
		t.Fatalf("CloseThread: %v", err)
	}
	srv := server.New(server.Config{Mesh: a})
	post := func(requestID, title string, expected uint64) (int, orchestrator.UpdateThreadTitleReceipt, map[string]any) {
		t.Helper()
		body, err := json.Marshal(map[string]any{
			"request_id": requestID, "title": title, "expected_title_seq": expected,
			"actor": map[string]any{"kind": "agent", "id": "stage:impl"},
		})
		if err != nil {
			t.Fatalf("marshal topic: %v", err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/mesh/threads/"+string(thread.ID)+"/topic", bytes.NewReader(body))
		req.Header.Set("Origin", "http://"+req.Host)
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		var receipt orchestrator.UpdateThreadTitleReceipt
		var response map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if w.Code == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil {
				t.Fatalf("decode receipt: %v", err)
			}
		}
		return w.Code, receipt, response
	}
	firstStatus, first, response := post("req-topic-first", "First", 0)
	if firstStatus != http.StatusOK || first.Title != "First" || first.TitleSeq == 0 {
		t.Fatalf("first edit: HTTP %d, receipt %+v, body %v", firstStatus, first, response)
	}
	secondStatus, second, response := post("req-topic-second", "Second", first.TitleSeq)
	if secondStatus != http.StatusOK || second.Title != "Second" || second.TitleSeq <= first.TitleSeq {
		t.Fatalf("second edit: HTTP %d, receipt %+v, body %v", secondStatus, second, response)
	}
	clearStatus, cleared, response := post("req-topic-clear", "", second.TitleSeq)
	if clearStatus != http.StatusOK || cleared.Title != "" || cleared.TitleSeq <= second.TitleSeq {
		t.Fatalf("clear: HTTP %d, receipt %+v, body %v", clearStatus, cleared, response)
	}
	staleStatus, _, response := post("req-topic-stale", "Stale", first.TitleSeq)
	if staleStatus != http.StatusConflict || response["error"] != "stale_revision" {
		t.Fatalf("stale edit: HTTP %d, body %v", staleStatus, response)
	}
	beforeRetry := store.Snapshot().LastSeq
	retryStatus, retry, response := post("req-topic-first", "First", 0)
	if retryStatus != http.StatusOK || retry != first {
		t.Fatalf("retry: HTTP %d, receipt %+v, body %v; want %+v", retryStatus, retry, response, first)
	}
	snap := store.Snapshot()
	if snap.LastSeq != beforeRetry || snap.Assistants.Mesh.ThreadTitles[thread.ID].Title != "" || snap.Assistants.Mesh.ThreadTitles[thread.ID].Seq != cleared.TitleSeq {
		t.Fatalf("retry changed state: seq %d -> %d, title %+v", beforeRetry, snap.LastSeq, snap.Assistants.Mesh.ThreadTitles[thread.ID])
	}
}

// A just-added run agent comes back with its roster entry — its alias included — so the dashboard can show
// it as @alias before the next roster poll (it used to show as @run:t-… for a few seconds).
func TestMeshActions_AttachAndSendReturnsTheParticipant(t *testing.T) {
	a, _ := newMeshAdapterTestActions(t, "impl")
	res, err := a.AttachAndSend(context.Background(), "req-add", server.MeshAttachAndSendRequest{
		ProfileID: "reviewer", Scope: "run", Title: "Standing QA", Instruction: "Watch the tests.", OriginStage: "impl",
	})
	if err != nil {
		t.Fatalf("AttachAndSend: %v", err)
	}
	p := res.Participant
	if p == nil || string(p.ID) != res.ParticipantID || p.Alias == "" || p.DisplayName != "Standing QA" {
		t.Fatalf("participant = %+v, want the new run agent's roster entry with its alias", p)
	}
}

// The stage conversation log and the stage feed's mesh messages carry the same journal seq for a post, so the
// dashboard orders the two readers by it, not by timestamps.
func TestDiscussionAdapter_MessagesCarryTheJournalSeq(t *testing.T) {
	a, _ := newMeshAdapterTestActions(t, "impl")
	d := collaborationDiscussion(a)
	id, err := d.PostMessage(context.Background(), "impl", server.DiscussionPost{RequestID: "req-post", Body: "status?"})
	if err != nil {
		t.Fatalf("PostMessage: %v", err)
	}
	log, _, _, err := d.Messages("impl", 0, 10)
	if err != nil || len(log) != 1 || log[0].ID != id || log[0].Seq == 0 {
		t.Fatalf("log = %+v, err = %v; want the post with its journal seq", log, err)
	}
	page, err := a.StageMessages(context.Background(), "impl", 0, 10)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Seq != log[0].Seq {
		t.Fatalf("mesh page = %+v, err = %v; want the mirror with seq %d", page.Messages, err, log[0].Seq)
	}
}
