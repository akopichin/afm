package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/server"
)

// loopAdapterFixture is the two mesh adapters over one orchestrator, and the run agent ("Standing QA") whose
// token the agent adapter is called with.
func loopAdapterFixture(t *testing.T) (human meshActions, agent agentMeshActions, qa string) {
	t.Helper()
	human, _ = newMeshAdapterTestActions(t, "impl", "docs")
	res, err := human.AttachAndSend(context.Background(), "req-qa", server.MeshAttachAndSendRequest{
		ProfileID: "reviewer", Scope: "run", Title: "Standing QA", Instruction: "Watch the tests.", OriginStage: "impl",
	})
	if err != nil {
		t.Fatalf("AttachAndSend: %v", err)
	}
	return human, agentMeshActions(human), res.ParticipantID
}

// The human adds a loop for an agent from a Feed; the agent adds its own for itself. Each caller is the loop's
// creator, an agent's body origin is ignored, and the agent lists only its own loops while the human lists all.
func TestLoopAdapters_BothSurfacesReachOneService(t *testing.T) {
	human, agent, qa := loopAdapterFixture(t)
	ctx := context.Background()

	byHuman, err := human.Loop(ctx, "req-loop-human", server.MeshLoopCommand{
		Action: server.MeshLoopAdd, Target: "@standing-qa", Every: 5 * time.Minute, Prompt: "check the tests", OriginStage: "impl",
	})
	if err != nil {
		t.Fatalf("human Loop add: %v", err)
	}
	if byHuman.CreatorID != agentcomm.UserParticipantID || string(byHuman.TargetID) != qa || byHuman.OriginStage != "impl" || byHuman.Every != "5m" {
		t.Fatalf("human's loop = %+v, want a loop from the human to %s made from impl", byHuman, qa)
	}

	byAgent, err := agent.Loop(ctx, "req-loop-agent", qa, server.MeshLoopCommand{
		Action: server.MeshLoopAdd, Every: time.Hour, Prompt: "look again", UntilStage: "docs", OriginStage: "impl",
	})
	if err != nil {
		t.Fatalf("agent Loop add: %v", err)
	}
	if string(byAgent.CreatorID) != qa || string(byAgent.TargetID) != qa || byAgent.UntilStage != "docs" {
		t.Fatalf("agent's loop = %+v, want its own loop for itself until docs", byAgent)
	}
	if byAgent.OriginStage == "impl" {
		t.Fatalf("agent's loop origin = %q: the body's origin_stage must not decide an agent's Feed", byAgent.OriginStage)
	}

	if page, err := human.Loops(ctx); err != nil || len(page.Loops) != 2 {
		t.Fatalf("human list = %+v (err %v), want both loops", page, err)
	}
	page, err := agent.Loops(ctx, qa)
	if err != nil || len(page.Loops) != 1 || page.Loops[0].ID != byAgent.ID {
		t.Fatalf("agent list = %+v (err %v), want only its own %s", page, err, byAgent.ID)
	}
	if page, err := agent.Loops(ctx, "run:someone-else"); err != nil || len(page.Loops) != 0 {
		t.Fatalf("another agent's list = %+v (err %v), want none", page, err)
	}
}

// An agent cannot stop a loop it did not create; the human can stop anyone's, and a retry of the same request
// is the same stopped loop.
func TestLoopAdapters_StopIsTheCreatorsOrTheHumans(t *testing.T) {
	human, agent, qa := loopAdapterFixture(t)
	ctx := context.Background()
	view, err := human.Loop(ctx, "req-loop", server.MeshLoopCommand{Action: server.MeshLoopAdd, Target: qa, Every: 5 * time.Minute, Prompt: "p", OriginStage: "impl"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	stop := server.MeshLoopCommand{Action: server.MeshLoopStop, LoopID: view.ID}
	if _, err := agent.Loop(ctx, "req-stop-agent", qa, stop); err == nil {
		t.Fatal("an agent stopped a loop the human created")
	}
	stopped, err := human.Loop(ctx, "req-stop-human", stop)
	if err != nil || stopped.Status != orchestrator.LoopStopped {
		t.Fatalf("human stop = %+v (err %v), want a stopped loop", stopped, err)
	}
	again, err := human.Loop(ctx, "req-stop-human", stop)
	if err != nil || again.ID != stopped.ID || again.Status != orchestrator.LoopStopped {
		t.Fatalf("retry = %+v (err %v), want the same stopped loop", again, err)
	}
}

// Over HTTP, on the real adapter: a retry of an add under the same request_id makes one loop and answers the
// committed view; the list is the run's loops.
func TestLoopAdapters_RetryOverHTTPMakesOneLoop(t *testing.T) {
	human, _, qa := loopAdapterFixture(t)
	handler := server.New(server.Config{Mesh: human}).Handler()
	post := func() *httptest.ResponseRecorder {
		raw, _ := json.Marshal(map[string]any{
			"request_id": "req-retry", "action": "add", "target": qa, "every": "5m", "prompt": "p", "origin_stage": "impl",
		})
		req := httptest.NewRequest(http.MethodPost, "/api/mesh/loop", bytes.NewReader(raw))
		req.Header.Set("Origin", "http://"+req.Host)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	first, second := post(), post()
	if first.Code != http.StatusOK || second.Code != http.StatusOK || first.Body.String() != second.Body.String() {
		t.Fatalf("add then retry: HTTP %d %s / HTTP %d %s, want the same 200 answer", first.Code, first.Body, second.Code, second.Body)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/mesh/loops", nil))
	var page orchestrator.LoopPage
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || len(page.Loops) != 1 {
		t.Fatalf("list = %s (err %v), want exactly one loop", w.Body.String(), err)
	}
}
