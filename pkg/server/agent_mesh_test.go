package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/orchestrator"
)

// fakeAgentMeshActions is an in-memory AgentMeshActions good enough to exercise the HTTP handlers without a
// real orchestrator — mirrors fakeAssistantActions' role in assistants_test.go.
type fakeAgentMeshActions struct {
	mu          sync.Mutex
	tokens      map[string]string // token -> participant id
	sentByReq   map[string]MeshSendReceipt
	lastSender  string
	lastSend    MeshSendRequest
	sendCalls   int
	participant []agentcomm.Participant
	// threadMembers, when non-nil, makes Thread enforce membership (keyed by thread id) — used only by the
	// T8.4 private-thread isolation test below; every other test leaves it nil (Thread always succeeds), the
	// same zero-value-means-"not exercised" convention lastSender/lastSend already follow.
	threadMembers map[string][]string

	lastTaskReqID string
	lastTaskActor string
	lastTask      MeshBoardTaskRequest
	lastTasksKey  string
	tasks         []agentcomm.BoardTaskView
}

func newFakeAgentMeshActions() *fakeAgentMeshActions {
	return &fakeAgentMeshActions{tokens: map[string]string{}, sentByReq: map[string]MeshSendReceipt{}}
}

func (f *fakeAgentMeshActions) ResolveToken(token string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.tokens[token]
	return p, ok
}

func (f *fakeAgentMeshActions) ListParticipants(ctx context.Context, actor string) ([]agentcomm.Participant, error) {
	return f.participant, nil
}

func (f *fakeAgentMeshActions) Send(ctx context.Context, requestID, actor string, req MeshSendRequest) (MeshSendReceipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendCalls++
	f.lastSender = actor
	f.lastSend = req
	if existing, ok := f.sentByReq[requestID]; ok {
		return existing, nil
	}
	receipt := MeshSendReceipt{MessageID: "msg-" + requestID, Seq: uint64(len(f.sentByReq) + 1)}
	f.sentByReq[requestID] = receipt
	return receipt, nil
}

func (f *fakeAgentMeshActions) Inbox(ctx context.Context, actor string, cursor uint64, limit int, all bool) (MeshMessagePage, error) {
	return MeshMessagePage{}, nil
}

func (f *fakeAgentMeshActions) Thread(ctx context.Context, actor, threadID string, cursor uint64, limit int) (MeshMessagePage, error) {
	if f.threadMembers != nil {
		if actor == "user" {
			return MeshMessagePage{}, nil // the human run owner sees every thread (ReadThread's own carve-out)
		}
		members, ok := f.threadMembers[threadID]
		if !ok {
			return MeshMessagePage{}, errors.New("unknown thread")
		}
		member := false
		for _, m := range members {
			if m == actor {
				member = true
				break
			}
		}
		if !member {
			return MeshMessagePage{}, errors.New("not a member of this thread")
		}
	}
	return MeshMessagePage{}, nil
}

func (f *fakeAgentMeshActions) Ack(ctx context.Context, requestID, actor, messageID string) error {
	return nil
}

func (f *fakeAgentMeshActions) Wait(ctx context.Context, requestID, actor string, req MeshWaitRequest) (MeshWaitReceipt, error) {
	return MeshWaitReceipt{WaitID: "wait-" + requestID, Status: "pending"}, nil
}

func (f *fakeAgentMeshActions) CancelWait(ctx context.Context, requestID, actor, waitID, reason string) error {
	return nil
}

func (f *fakeAgentMeshActions) PublishBoard(ctx context.Context, requestID, actor string, req MeshPublishRequest) (agentcomm.BoardEntry, error) {
	return agentcomm.BoardEntry{AuthorID: agentcomm.ParticipantID(actor), Key: req.Key, Version: 1}, nil
}

func (f *fakeAgentMeshActions) Board(ctx context.Context, key string) ([]agentcomm.BoardEntry, error) {
	return nil, nil
}

func (f *fakeAgentMeshActions) BoardTask(ctx context.Context, requestID, actor string, req MeshBoardTaskRequest) (orchestrator.BoardTaskReceipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastTaskReqID, f.lastTaskActor, f.lastTask = requestID, actor, req
	return orchestrator.BoardTaskReceipt{Key: req.Key, Version: req.ExpectedVersion + 1, Status: agentcomm.BoardTaskTodo, Seq: 7}, nil
}

func (f *fakeAgentMeshActions) BoardTasks(ctx context.Context, key string) ([]agentcomm.BoardTaskView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastTasksKey = key
	return f.tasks, nil
}

func TestAgentMesh_404WhenCapabilityOff(t *testing.T) {
	srv := newTestServer(t, Config{})
	req := httptest.NewRequest(http.MethodGet, "/api/agent-mesh/whoami", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("HTTP %d, want 404 when AgentMesh is nil", w.Code)
	}
}

func TestAgentMesh_MissingOrUnknownTokenRejectedWithoutSideEffects(t *testing.T) {
	fake := newFakeAgentMeshActions()
	srv := newTestServer(t, Config{AgentMesh: fake})

	cases := []struct {
		name string
		auth string
	}{
		{"no header", ""},
		{"garbage", "Bearer "},
		{"unknown token", "Bearer not-a-real-token"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "/api/agent-mesh/whoami", nil)
		if c.auth != "" {
			req.Header.Set("Authorization", c.auth)
		}
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s: HTTP %d, want 401", c.name, w.Code)
		}
	}
	if fake.sendCalls != 0 {
		t.Fatalf("an unauthenticated request must never reach the Service: sendCalls=%d", fake.sendCalls)
	}
}

func TestAgentMesh_WhoamiReturnsTokenResolvedIdentityOnly(t *testing.T) {
	fake := newFakeAgentMeshActions()
	fake.tokens["tok-A"] = "helper:abc"
	srv := newTestServer(t, Config{AgentMesh: fake})

	req := httptest.NewRequest(http.MethodGet, "/api/agent-mesh/whoami", nil)
	req.Header.Set("Authorization", "Bearer tok-A")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d (body=%s)", w.Code, w.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["participant_id"] != "helper:abc" {
		t.Fatalf("participant_id = %q, want helper:abc", body["participant_id"])
	}
}

// TestAgentMesh_TokenIdentityIsNeverClientSupplied proves a token minted for participant A cannot send as B
// even if the body tries: MeshSendRequest has no sender/actor field at all, so there is nothing in the body
// to even attempt impersonation with — the adapter's Send call always receives the TOKEN-resolved actor.
func TestAgentMesh_TokenIdentityIsNeverClientSupplied(t *testing.T) {
	fake := newFakeAgentMeshActions()
	fake.tokens["tok-A"] = "helper:a"
	srv := newTestServer(t, Config{AgentMesh: fake})

	body, _ := json.Marshal(map[string]any{
		"request_id": "req-1", "thread_id": "t1", "recipient_ids": []string{"helper:b"}, "kind": "message", "body": "hi",
		// Even if a malicious client tries to smuggle an actor/sender field, MeshSendRequest has no such
		// field to decode it into.
		"sender_id": "helper:b", "actor": "helper:b",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/agent-mesh/send", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok-A")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d (body=%s)", w.Code, w.Body.String())
	}
	if fake.lastSender != "helper:a" {
		t.Fatalf("actor passed to the Service = %q, want helper:a (the token's own identity)", fake.lastSender)
	}
}

func TestAgentMesh_SendRequiresRequestID(t *testing.T) {
	fake := newFakeAgentMeshActions()
	fake.tokens["tok-A"] = "helper:a"
	srv := newTestServer(t, Config{AgentMesh: fake})

	body, _ := json.Marshal(map[string]any{"thread_id": "t1", "recipient_ids": []string{"helper:b"}, "kind": "message", "body": "hi"})
	req := httptest.NewRequest(http.MethodPost, "/api/agent-mesh/send", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok-A")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("HTTP %d, want 400 without request_id", w.Code)
	}
}

func TestAgentMesh_RejectsOversizedBody(t *testing.T) {
	fake := newFakeAgentMeshActions()
	fake.tokens["tok-A"] = "helper:a"
	srv := newTestServer(t, Config{AgentMesh: fake})

	huge := make([]byte, maxAgentMeshBodyBytes+1024)
	for i := range huge {
		huge[i] = 'a'
	}
	body, _ := json.Marshal(map[string]any{"request_id": "req-1", "thread_id": "t1", "recipient_ids": []string{"helper:b"}, "kind": "message", "body": string(huge)})
	req := httptest.NewRequest(http.MethodPost, "/api/agent-mesh/send", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok-A")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("HTTP %d, want 413", w.Code)
	}
}

func TestAgentMesh_ReplyForcesReplyKind(t *testing.T) {
	fake := newFakeAgentMeshActions()
	fake.tokens["tok-A"] = "helper:a"
	srv := newTestServer(t, Config{AgentMesh: fake})

	body, _ := json.Marshal(map[string]any{
		"request_id": "req-1", "thread_id": "t1", "recipient_ids": []string{"helper:b"}, "kind": "message", "body": "hi", "in_reply_to": "m1",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/agent-mesh/reply", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok-A")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d (body=%s)", w.Code, w.Body.String())
	}
	if fake.lastSend.Kind != "reply" {
		t.Fatalf("kind = %q, want reply even though the body said %q", fake.lastSend.Kind, "message")
	}
}

// ---- Agent Mesh Phase 8, T8.4: limits, authority and isolation ----
//
// See pkg/orchestrator/agentcomm_limits_test.go's own header comment for the full bullet-by-bullet mapping of
// dev-plan.md's T8.4 clauses to named tests across both packages. The four tests below are this file's share:
// forging an author/generation/run, a private thread's isolation at the HTTP adapter (not just the Service),
// and the agent-facing surface never exposing a capability-widening route.

// TestAgentMesh_PublishIgnoresClientSuppliedAuthor mirrors TestAgentMesh_TokenIdentityIsNeverClientSupplied
// for PublishBoard: MeshPublishRequest has no author/actor field at all, so the board entry's AuthorID can
// only ever be the token-resolved actor, regardless of what a malicious body tries to smuggle in.
func TestAgentMesh_PublishIgnoresClientSuppliedAuthor(t *testing.T) {
	fake := newFakeAgentMeshActions()
	fake.tokens["tok-A"] = "helper:a"
	srv := newTestServer(t, Config{AgentMesh: fake})

	body, _ := json.Marshal(map[string]any{
		"request_id": "req-1", "key": "qa.tests", "status": "ready",
		// MeshPublishRequest has nowhere to decode these into — smuggled for nothing.
		"author_id": "helper:b", "actor": "helper:b",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/agent-mesh/publish", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok-A")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d (body=%s)", w.Code, w.Body.String())
	}
	var entry agentcomm.BoardEntry
	if err := json.Unmarshal(w.Body.Bytes(), &entry); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if entry.AuthorID != "helper:a" {
		t.Fatalf("published author_id = %q, want helper:a (the token's own identity)", entry.AuthorID)
	}
}

// TestAgentMesh_RequestBodiesCarryNoSenderActorGenerationOrRunOverride is T8.4's "cannot forge ... a
// generation or a run" clause, proven structurally: none of the agent-facing request DTOs have a field a
// client could even populate for sender/actor/generation/run identity — reflect over every exported field's
// json tag and fail loudly if one is ever added, rather than relying on nobody noticing in review.
func TestAgentMesh_RequestBodiesCarryNoSenderActorGenerationOrRunOverride(t *testing.T) {
	forbidden := map[string]bool{
		"sender_id": true, "sender": true, "actor": true, "actor_id": true,
		"generation": true, "run_id": true, "task_generation": true,
	}
	types := []any{MeshSendRequest{}, MeshWaitRequest{}, MeshPublishRequest{}}
	for _, v := range types {
		rt := reflect.TypeOf(v)
		for i := 0; i < rt.NumField(); i++ {
			tag := rt.Field(i).Tag.Get("json")
			name := strings.Split(tag, ",")[0]
			if forbidden[name] {
				t.Fatalf("%s.%s has json tag %q — the authenticated actor must be the ONLY source of identity/generation/run, never the request body", rt.Name(), rt.Field(i).Name, name)
			}
		}
	}
}

// TestAgentMesh_PrivateThreadNotReadableByNonMemberHumanExcepted complements
// TestMesh_ReadThreadMembershipEnforced (pkg/orchestrator/agentcomm_messages_test.go, the authoritative
// Service-level enforcement) by proving the HTTP adapter actually PROPAGATES that rejection rather than
// swallowing it or returning 200 with empty/wrong data — and that the human run owner's own carve-out survives
// the same round trip.
func TestAgentMesh_PrivateThreadNotReadableByNonMemberHumanExcepted(t *testing.T) {
	fake := newFakeAgentMeshActions()
	fake.tokens["tok-member"] = "helper:a"
	fake.tokens["tok-outsider"] = "helper:b"
	fake.tokens["tok-human"] = "user"
	fake.threadMembers = map[string][]string{"t1": {"helper:a"}}
	srv := newTestServer(t, Config{AgentMesh: fake})

	get := func(token string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/agent-mesh/thread?id=t1", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		return w.Code
	}

	if code := get("tok-member"); code != http.StatusOK {
		t.Fatalf("member read: HTTP %d, want 200", code)
	}
	if code := get("tok-human"); code != http.StatusOK {
		t.Fatalf("human run owner read: HTTP %d, want 200 (the human sees every thread)", code)
	}
	if code := get("tok-outsider"); code == http.StatusOK {
		t.Fatalf("non-member read a private thread: HTTP %d, want an explicit rejection", code)
	}
}

// TestAgentMesh_NoAttachOrCapabilityWideningRouteExposedToAgents is T8.4's "a peer message cannot create a
// new root to dodge max_depth or widen capabilities" clause: AgentMeshActions (this file's own interface)
// has no AttachAndSend/AddRunAgent method at all — the only two things in this codebase that create a NEW
// mesh participant/task — so there is no route for an authenticated agent token to reach one, by construction,
// never merely by convention. routeAgentMesh's own switch (agent_mesh.go) is exhaustive; anything not listed
// there 404s before even touching the Service.
func TestAgentMesh_NoAttachOrCapabilityWideningRouteExposedToAgents(t *testing.T) {
	fake := newFakeAgentMeshActions()
	fake.tokens["tok-A"] = "helper:a"
	srv := newTestServer(t, Config{AgentMesh: fake})

	for _, action := range []string{"attach-and-send", "attach", "add-run-agent", "threads"} {
		req := httptest.NewRequest(http.MethodPost, "/api/agent-mesh/"+action, bytes.NewReader([]byte(`{}`)))
		req.Header.Set("Authorization", "Bearer tok-A")
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s: HTTP %d, want 404 — the agent-facing surface must never expose a capability-widening route", action, w.Code)
		}
	}
}

// A board task change is made by the token's participant: an actor or participant_id in the body is ignored,
// and every other body field reaches the Service as sent.
func TestAgentMesh_BoardTask_ActorFromTokenOnly(t *testing.T) {
	fake := newFakeAgentMeshActions()
	fake.tokens["tok-A"] = "helper:a"
	srv := newTestServer(t, Config{AgentMesh: fake})

	body, _ := json.Marshal(map[string]any{
		"request_id": "req-t1", "action": "done", "key": "task/api", "expected_version": 3, "assignment_id": "as-1",
		"to": "docs", "title": "T", "description": "D", "coordinator": "user", "text": "finished", "artifact_refs": []string{"a.md"},
		"actor": "helper:b", "participant_id": "helper:b",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/agent-mesh/task", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok-A")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d (body=%s)", w.Code, w.Body.String())
	}
	if fake.lastTaskActor != "helper:a" {
		t.Fatalf("actor = %q, want the token's helper:a", fake.lastTaskActor)
	}
	want := MeshBoardTaskRequest{
		Action: "done", Key: "task/api", ExpectedVersion: 3, AssignmentID: "as-1", To: "docs", Title: "T",
		Description: "D", Coordinator: "user", Text: "finished", ArtifactRefs: []string{"a.md"},
	}
	if fake.lastTaskReqID != "req-t1" || !reflect.DeepEqual(fake.lastTask, want) {
		t.Fatalf("Service got %q %+v, want req-t1 %+v", fake.lastTaskReqID, fake.lastTask, want)
	}
	var receipt orchestrator.BoardTaskReceipt
	if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil || receipt.Key != "task/api" || receipt.Version != 4 || receipt.Seq != 7 {
		t.Fatalf("receipt = %+v (%v), want the Service's receipt", receipt, err)
	}
}

func TestAgentMesh_BoardTask_RequiresRequestID(t *testing.T) {
	fake := newFakeAgentMeshActions()
	fake.tokens["tok-A"] = "helper:a"
	srv := newTestServer(t, Config{AgentMesh: fake})

	body, _ := json.Marshal(map[string]any{"action": "claim", "key": "task/api"})
	req := httptest.NewRequest(http.MethodPost, "/api/agent-mesh/task", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok-A")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("HTTP %d, want 400 without request_id", w.Code)
	}
	if fake.lastTask.Action != "" {
		t.Fatalf("a request without request_id must not reach the Service: %+v", fake.lastTask)
	}
}

// GET tasks passes the decoded key through and shows a task without assignments with an empty array, never null.
func TestAgentMesh_BoardTasks_KeyAndEmptyAssignments(t *testing.T) {
	fake := newFakeAgentMeshActions()
	fake.tokens["tok-A"] = "helper:a"
	fake.tasks = []agentcomm.BoardTaskView{{BoardTask: agentcomm.BoardTask{Key: "task/a b", Title: "T", Version: 1}, Status: agentcomm.BoardTaskTodo}}
	srv := newTestServer(t, Config{AgentMesh: fake})

	req := httptest.NewRequest(http.MethodGet, "/api/agent-mesh/tasks?key=task%2Fa+b", nil)
	req.Header.Set("Authorization", "Bearer tok-A")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d (body=%s)", w.Code, w.Body.String())
	}
	if fake.lastTasksKey != "task/a b" {
		t.Fatalf("key = %q, want task/a b", fake.lastTasksKey)
	}
	if !strings.Contains(w.Body.String(), `"assignments":[]`) || !strings.Contains(w.Body.String(), `"status":"todo"`) {
		t.Fatalf("body = %s, want assignments [] and status todo", w.Body.String())
	}
}
