package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/server"
)

// An empty stage feed answers with empty lists, not null: the dashboard appends them as they are.
func TestToMeshStagePage_EmptyListsNotNull(t *testing.T) {
	data, err := json.Marshal(toMeshStagePage(orchestrator.StageMessagePage{NextCursor: 7}))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), `{"messages":[],"threads":[],"next_cursor":7,"has_more":false}`; got != want {
		t.Fatalf("page = %s, want %s", got, want)
	}
}

func TestMeshActions_ThreadsCarriesSnapshotSummariesReadOnly(t *testing.T) {
	a, store := newMeshAdapterTestActions(t, "impl", "docs")
	ctx := context.Background()
	sent, err := a.Send(ctx, "req-send", server.MeshUserSendRequest{MeshSendRequest: server.MeshSendRequest{
		Recipients: []string{"@impl"}, Body: "hello   there",
	}})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	other, err := a.orch.OpenThread(ctx, orchestrator.OpenThreadRequest{
		OperationID: "req-other", Scope: agentcomm.ScopeRun,
		Members: []agentcomm.ParticipantID{"stage:impl", "stage:docs"},
	})
	if err != nil {
		t.Fatalf("OpenThread without user member: %v", err)
	}
	before := store.Snapshot().LastSeq
	page, err := a.Threads(ctx)
	if err != nil {
		t.Fatalf("Threads: %v", err)
	}
	if len(page.Threads) != 2 || len(page.Summaries) != 2 {
		t.Fatalf("Threads = %+v, want both the human and other thread with summaries", page)
	}
	summary := page.Summaries[agentcomm.ThreadID(sent.ThreadID)]
	if summary.LastMessage == nil || summary.LastMessage.ID != agentcomm.MessageID(sent.MessageID) || summary.LastMessage.Preview != "hello there" || summary.TitleSeq != 0 {
		t.Fatalf("sent thread summary = %+v, want saved message preview and initial title revision", summary)
	}
	if _, ok := page.Summaries[other.ID]; !ok {
		t.Fatalf("operator cannot see summary for non-member thread %s", other.ID)
	}
	w := httptest.NewRecorder()
	server.New(server.Config{Mesh: a}).Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/mesh/threads", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	var httpPage orchestrator.ThreadPage
	if err := json.Unmarshal(w.Body.Bytes(), &httpPage); err != nil {
		t.Fatalf("decode HTTP page: %v", err)
	}
	if len(httpPage.Threads) != 2 || len(httpPage.Summaries) != 2 || httpPage.Summaries[agentcomm.ThreadID(sent.ThreadID)].LastMessage == nil {
		t.Fatalf("HTTP page = %+v, want both threads and their summaries", httpPage)
	}
	if got := store.Snapshot().LastSeq; got != before {
		t.Fatalf("Threads or HTTP read wrote journal: LastSeq = %d, was %d", got, before)
	}
}
