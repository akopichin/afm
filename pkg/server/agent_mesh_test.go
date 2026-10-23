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
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
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

	lastWait       MeshWaitRequest
	lastWaitActor  string
	waitReceipt    MeshWaitReceipt
	lastCancel     MeshCancelWaitRequest
	lastCancelUser string
	statusActor    string
	statusBatchID  string
	batches        []MeshWaitReceipt
	statusErr      error

	lastSpawnReqID string
	lastSpawnActor string
	lastSpawn      MeshSpawnRequest
	spawnCalls     int
	spawnErr       error
	helperActor    string
	helperTaskID   string
	helperResult   MeshHelperResult
	helperErr      error
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
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastWait, f.lastWaitActor = req, actor
	if f.waitReceipt.Status != "" {
		return f.waitReceipt, nil
	}
	return MeshWaitReceipt{WaitID: "wait-" + requestID, Status: "pending"}, nil
}

func (f *fakeAgentMeshActions) WaitStatus(ctx context.Context, actor, batchID string) ([]MeshWaitReceipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusActor, f.statusBatchID = actor, batchID
	return f.batches, f.statusErr
}

func (f *fakeAgentMeshActions) Spawn(ctx context.Context, requestID, actor string, req MeshSpawnRequest) (MeshSpawnReceipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastSpawnReqID, f.lastSpawnActor, f.lastSpawn = requestID, actor, req
	f.spawnCalls++
	if f.spawnErr != nil {
		return MeshSpawnReceipt{}, f.spawnErr
	}
	return MeshSpawnReceipt{TaskID: "t-" + requestID, ParticipantID: "helper:t-" + requestID, MessageID: "mm-" + requestID}, nil
}

func (f *fakeAgentMeshActions) HelperResult(ctx context.Context, actor, taskID string) (MeshHelperResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.helperActor, f.helperTaskID = actor, taskID
	return f.helperResult, f.helperErr
}

func (f *fakeAgentMeshActions) CancelWait(ctx context.Context, requestID, actor string, req MeshCancelWaitRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastCancel, f.lastCancelUser = req, actor
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
	types := []any{MeshSendRequest{}, MeshWaitRequest{}, MeshCancelWaitRequest{}, MeshPublishRequest{}, MeshSpawnRequest{}}
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
		Action: "done", Key: "task/api", ExpectedVersion: 3, AssignmentID: "as-1", To: "docs", Title: new("T"),
		Description: new("D"), Coordinator: new("user"), Text: "finished", ArtifactRefs: &[]string{"a.md"},
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

// ---- Batch waits: one wait for several requests, read back by the batch's own waiter only. ----

// postAgentMesh posts body to /api/agent-mesh/<action> as the participant of token.
func postAgentMesh(t *testing.T, srv *Server, token, action string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/agent-mesh/"+action, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	return w
}

// A batch wait carries the list of request ids to the Service and the token's participant as the waiter; a
// waiter or actor in the body is ignored. Its receipt shows the batch id and one outcome per request.
func TestAgentMesh_Wait_BatchReachesServiceAndShowsEveryOutcome(t *testing.T) {
	fake := newFakeAgentMeshActions()
	fake.tokens["tok-A"] = "stage:s1"
	fake.waitReceipt = MeshWaitReceipt{BatchID: "mb-1", Status: "pending", Items: []MeshWaitItem{
		{MessageID: "m1", WaitID: "mw-1", Status: "ready"},
		{MessageID: "m2", WaitID: "mw-2", Status: "pending"},
	}}
	srv := newTestServer(t, Config{AgentMesh: fake})

	w := postAgentMesh(t, srv, "tok-A", "wait", map[string]any{
		"request_id": "req-1", "condition": map[string]any{"kind": "reply_to"}, "message_ids": []string{"m1", "m2"},
		"waiter": "stage:other", "actor": "stage:other",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d (body=%s)", w.Code, w.Body.String())
	}
	if fake.lastWaitActor != "stage:s1" || !reflect.DeepEqual(fake.lastWait.MessageIDs, []string{"m1", "m2"}) || fake.lastWait.Condition.Kind != agentcomm.WaitOnReplyTo {
		t.Fatalf("Service got %q %+v, want the token's stage:s1 and the two ids", fake.lastWaitActor, fake.lastWait)
	}
	want := `{"status":"pending","batch_id":"mb-1","items":[{"message_id":"m1","wait_id":"mw-1","status":"ready"},{"message_id":"m2","wait_id":"mw-2","status":"pending"}]}`
	if got := strings.TrimSpace(w.Body.String()); got != want {
		t.Fatalf("receipt = %s, want %s", got, want)
	}
}

// 16 requests with long ids still fit the body limit by a wide margin, so the limit never cuts a batch.
func TestAgentMesh_Wait_SixteenLongIDsFitTheBodyLimit(t *testing.T) {
	fake := newFakeAgentMeshActions()
	fake.tokens["tok-A"] = "stage:s1"
	srv := newTestServer(t, Config{AgentMesh: fake})
	ids := make([]string, orchestrator.MaxBatchWaits)
	for i := range ids {
		ids[i] = strings.Repeat("m", 200) + string(rune('a'+i))
	}

	w := postAgentMesh(t, srv, "tok-A", "wait", map[string]any{"request_id": "req-1", "condition": map[string]any{"kind": "reply_to"}, "message_ids": ids})
	if w.Code != http.StatusOK || len(fake.lastWait.MessageIDs) != orchestrator.MaxBatchWaits {
		t.Fatalf("HTTP %d, ids = %d, want 200 with all %d ids", w.Code, len(fake.lastWait.MessageIDs), orchestrator.MaxBatchWaits)
	}
}

// A plain wait keeps its receipt exactly: wait id and status, nothing of a batch.
func TestAgentMesh_Wait_SingleReceiptIsUnchanged(t *testing.T) {
	fake := newFakeAgentMeshActions()
	fake.tokens["tok-A"] = "stage:s1"
	srv := newTestServer(t, Config{AgentMesh: fake})

	w := postAgentMesh(t, srv, "tok-A", "wait", map[string]any{"request_id": "req-1", "condition": map[string]any{"kind": "reply_to", "message_id": "m1"}})
	if got, want := strings.TrimSpace(w.Body.String()), `{"wait_id":"wait-req-1","status":"pending"}`; got != want {
		t.Fatalf("receipt = %s, want %s", got, want)
	}
	if len(fake.lastWait.MessageIDs) != 0 {
		t.Fatalf("a single wait reached the Service with ids %v", fake.lastWait.MessageIDs)
	}
}

// GET wait-status reads for the token's participant only: the actor comes from the token, the batch id from
// the query, and a refusal of the Service (a batch that is not the caller's) reaches the client as not found.
func TestAgentMesh_WaitStatus_ReadsForTheTokenActorOnly(t *testing.T) {
	fake := newFakeAgentMeshActions()
	fake.tokens["tok-A"] = "stage:s1"
	fake.batches = []MeshWaitReceipt{{BatchID: "mb-1", Status: "ready", Items: []MeshWaitItem{{MessageID: "m1", WaitID: "mw-1", Status: "cancelled", Reason: "gone"}}}}
	srv := newTestServer(t, Config{AgentMesh: fake})
	get := func(query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/agent-mesh/wait-status"+query, nil)
		req.Header.Set("Authorization", "Bearer tok-A")
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		return w
	}

	w := get("")
	if w.Code != http.StatusOK || fake.statusActor != "stage:s1" || fake.statusBatchID != "" {
		t.Fatalf("list: HTTP %d, actor %q, batch %q", w.Code, fake.statusActor, fake.statusBatchID)
	}
	want := `{"batches":[{"status":"ready","batch_id":"mb-1","items":[{"message_id":"m1","wait_id":"mw-1","status":"cancelled","reason":"gone"}]}]}`
	if got := strings.TrimSpace(w.Body.String()); got != want {
		t.Fatalf("body = %s, want %s", got, want)
	}

	if w := get("?wait_id=mb-1"); w.Code != http.StatusOK || fake.statusBatchID != "mb-1" {
		t.Fatalf("one batch: HTTP %d, batch %q, want 200 and mb-1", w.Code, fake.statusBatchID)
	}

	fake.statusErr = &state.OperationRejectedError{Code: assistants.CodeNotFound, Reason: "unknown batch mb-9"}
	if w := get("?wait_id=mb-9"); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "not_found") {
		t.Fatalf("a batch of someone else: HTTP %d (%s), want 404 not_found", w.Code, w.Body.String())
	}
}

// Without a batch the list is an empty array, never null.
func TestAgentMesh_WaitStatus_NoBatchesIsAnEmptyList(t *testing.T) {
	fake := newFakeAgentMeshActions()
	fake.tokens["tok-A"] = "stage:s1"
	srv := newTestServer(t, Config{AgentMesh: fake})

	req := httptest.NewRequest(http.MethodGet, "/api/agent-mesh/wait-status", nil)
	req.Header.Set("Authorization", "Bearer tok-A")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if got, want := strings.TrimSpace(w.Body.String()), `{"batches":[]}`; got != want {
		t.Fatalf("body = %s, want %s", got, want)
	}
}

func TestAgentMesh_WaitStatus_RequiresAToken(t *testing.T) {
	fake := newFakeAgentMeshActions()
	srv := newTestServer(t, Config{AgentMesh: fake})

	req := httptest.NewRequest(http.MethodGet, "/api/agent-mesh/wait-status", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized || fake.statusActor != "" {
		t.Fatalf("HTTP %d, actor %q, want 401 and no Service call", w.Code, fake.statusActor)
	}
}

// cancel-wait takes one wait or a whole batch; a body naming neither, or both, is refused before the Service.
func TestAgentMesh_CancelWait_TakesAWaitOrABatch(t *testing.T) {
	fake := newFakeAgentMeshActions()
	fake.tokens["tok-A"] = "stage:s1"
	srv := newTestServer(t, Config{AgentMesh: fake})

	if w := postAgentMesh(t, srv, "tok-A", "cancel-wait", map[string]any{"request_id": "r1", "batch_id": "mb-1", "reason": "not needed"}); w.Code != http.StatusOK {
		t.Fatalf("batch: HTTP %d (%s)", w.Code, w.Body.String())
	}
	if want := (MeshCancelWaitRequest{BatchID: "mb-1", Reason: "not needed"}); fake.lastCancel != want || fake.lastCancelUser != "stage:s1" {
		t.Fatalf("Service got %q %+v, want %+v for the token's stage:s1", fake.lastCancelUser, fake.lastCancel, want)
	}

	if w := postAgentMesh(t, srv, "tok-A", "cancel-wait", map[string]any{"request_id": "r2", "wait_id": "mw-1"}); w.Code != http.StatusOK {
		t.Fatalf("wait: HTTP %d (%s)", w.Code, w.Body.String())
	}
	if want := (MeshCancelWaitRequest{WaitID: "mw-1"}); fake.lastCancel != want {
		t.Fatalf("Service got %+v, want %+v", fake.lastCancel, want)
	}

	for name, body := range map[string]map[string]any{
		"neither": {"request_id": "r3"},
		"both":    {"request_id": "r4", "wait_id": "mw-1", "batch_id": "mb-1"},
	} {
		fake.lastCancel = MeshCancelWaitRequest{}
		if w := postAgentMesh(t, srv, "tok-A", "cancel-wait", body); w.Code != http.StatusBadRequest {
			t.Fatalf("%s: HTTP %d, want 400", name, w.Code)
		}
		if fake.lastCancel != (MeshCancelWaitRequest{}) {
			t.Fatalf("%s: a cancel that is not of exactly one wait or batch reached the Service: %+v", name, fake.lastCancel)
		}
	}
}

// ---- Spawn: a stage agent starts a helper; it names only the profile, the title and the instruction. ----

func TestAgentMesh_Spawn_NeedsRequestIDProfileTitleAndInstruction(t *testing.T) {
	fake := newFakeAgentMeshActions()
	fake.tokens["tok-A"] = "stage:s1"
	srv := newTestServer(t, Config{AgentMesh: fake})
	full := map[string]any{"request_id": "r1", "profile_id": "reviewer", "title": "Review", "instruction": "look"}

	for _, missing := range []string{"request_id", "profile_id", "title", "instruction"} {
		body := map[string]any{}
		for k, v := range full {
			if k != missing {
				body[k] = v
			}
		}
		if w := postAgentMesh(t, srv, "tok-A", "spawn", body); w.Code != http.StatusBadRequest {
			t.Fatalf("without %s: HTTP %d, want 400", missing, w.Code)
		}
	}
	if fake.spawnCalls != 0 {
		t.Fatalf("an incomplete spawn reached the Service %d times", fake.spawnCalls)
	}

	w := postAgentMesh(t, srv, "tok-A", "spawn", full)
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d (%s)", w.Code, w.Body.String())
	}
	want := `{"task_id":"t-r1","participant_id":"helper:t-r1","message_id":"mm-r1"}`
	if got := strings.TrimSpace(w.Body.String()); got != want {
		t.Fatalf("body = %s, want %s", got, want)
	}
}

// The stage is the token's, and nothing else about the helper is the client's to choose: a stage, scope, mode,
// command, model or actor in the body is ignored.
func TestAgentMesh_Spawn_StageComesFromTheTokenAndExtraFieldsAreIgnored(t *testing.T) {
	fake := newFakeAgentMeshActions()
	fake.tokens["tok-A"] = "stage:s1"
	srv := newTestServer(t, Config{AgentMesh: fake})

	w := postAgentMesh(t, srv, "tok-A", "spawn", map[string]any{
		"request_id": "r1", "profile_id": "reviewer", "title": "Review", "instruction": "look",
		"stage_id": "s2", "scope": "run", "mode": "required", "trigger": "watcher", "command": "rm -rf /", "model": "x",
		"actor": "user", "sender_id": "user", "capabilities": []string{"write"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d (%s)", w.Code, w.Body.String())
	}
	if want := (MeshSpawnRequest{ProfileID: "reviewer", Title: "Review", Instruction: "look"}); fake.lastSpawn != want {
		t.Fatalf("Service got %+v, want only %+v", fake.lastSpawn, want)
	}
	if fake.lastSpawnActor != "stage:s1" || fake.lastSpawnReqID != "r1" {
		t.Fatalf("Service got actor %q request %q, want stage:s1 and r1", fake.lastSpawnActor, fake.lastSpawnReqID)
	}
}

func TestAgentMesh_Spawn_ServiceRefusalReachesTheClient(t *testing.T) {
	fake := newFakeAgentMeshActions()
	fake.tokens["tok-H"] = "helper:t-1"
	fake.spawnErr = &state.OperationRejectedError{Code: assistants.CodeActorNotAllowed, Reason: "only a stage agent can spawn a helper"}
	srv := newTestServer(t, Config{AgentMesh: fake})

	w := postAgentMesh(t, srv, "tok-H", "spawn", map[string]any{"request_id": "r1", "profile_id": "p", "title": "t", "instruction": "i"})
	if w.Code == http.StatusOK || !strings.Contains(w.Body.String(), "actor_not_allowed") {
		t.Fatalf("HTTP %d (%s), want a refusal naming actor_not_allowed", w.Code, w.Body.String())
	}
}

func TestAgentMesh_Spawn_RequiresAToken(t *testing.T) {
	fake := newFakeAgentMeshActions()
	srv := newTestServer(t, Config{AgentMesh: fake})
	if w := postAgentMesh(t, srv, "nope", "spawn", map[string]any{"request_id": "r1", "profile_id": "p", "title": "t", "instruction": "i"}); w.Code != http.StatusUnauthorized || fake.spawnCalls != 0 {
		t.Fatalf("HTTP %d, calls %d, want 401 and no Service call", w.Code, fake.spawnCalls)
	}
}

// ---- helper: the status and the full committed result of one helper of the caller's stage. ----

func getAgentMesh(srv *Server, token, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/agent-mesh/"+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	return w
}

func TestAgentMesh_HelperResult_ReadsForTheTokenActorByTaskID(t *testing.T) {
	fake := newFakeAgentMeshActions()
	fake.tokens["tok-A"] = "stage:s1"
	fake.helperResult = MeshHelperResult{TaskID: "t-1", Title: "Review", Status: "completed", Result: json.RawMessage(`{"verdict":"pass"}`)}
	srv := newTestServer(t, Config{AgentMesh: fake})

	w := getAgentMesh(srv, "tok-A", "helper?task_id=t-1")
	if w.Code != http.StatusOK || fake.helperActor != "stage:s1" || fake.helperTaskID != "t-1" {
		t.Fatalf("HTTP %d, actor %q, task %q", w.Code, fake.helperActor, fake.helperTaskID)
	}
	want := `{"task_id":"t-1","title":"Review","status":"completed","result":{"verdict":"pass"}}`
	if got := strings.TrimSpace(w.Body.String()); got != want {
		t.Fatalf("body = %s, want %s", got, want)
	}

	// A helper that has not finished is a status with no result, not an error.
	fake.helperResult = MeshHelperResult{TaskID: "t-2", Title: "Other", Status: "running"}
	w = getAgentMesh(srv, "tok-A", "helper?task_id=t-2")
	if got, want := strings.TrimSpace(w.Body.String()), `{"task_id":"t-2","title":"Other","status":"running"}`; w.Code != http.StatusOK || got != want {
		t.Fatalf("HTTP %d, body = %s, want %s", w.Code, got, want)
	}
}

func TestAgentMesh_HelperResult_NeedsATaskIDAndAToken(t *testing.T) {
	fake := newFakeAgentMeshActions()
	fake.tokens["tok-A"] = "stage:s1"
	srv := newTestServer(t, Config{AgentMesh: fake})

	if w := getAgentMesh(srv, "tok-A", "helper"); w.Code != http.StatusBadRequest || fake.helperActor != "" {
		t.Fatalf("no task id: HTTP %d, actor %q, want 400 and no Service call", w.Code, fake.helperActor)
	}
	if w := getAgentMesh(srv, "", "helper?task_id=t-1"); w.Code != http.StatusUnauthorized || fake.helperActor != "" {
		t.Fatalf("no token: HTTP %d, actor %q, want 401 and no Service call", w.Code, fake.helperActor)
	}
}

func TestAgentMesh_HelperResult_AnotherStagesTaskIsNotFound(t *testing.T) {
	fake := newFakeAgentMeshActions()
	fake.tokens["tok-A"] = "stage:s1"
	fake.helperErr = &state.OperationRejectedError{Code: assistants.CodeNotFound, Reason: "unknown helper t-9"}
	srv := newTestServer(t, Config{AgentMesh: fake})

	if w := getAgentMesh(srv, "tok-A", "helper?task_id=t-9"); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "not_found") {
		t.Fatalf("HTTP %d (%s), want 404 not_found", w.Code, w.Body.String())
	}
}
