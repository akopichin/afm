package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
)

// errUnknownThreadForTest stands in for the bare error ReadThread returns for an unknown/foreign thread id
// (pkg/orchestrator/agentcomm_messages.go) — the fake does not need the real error type, only something
// writeAssistantError turns into a non-200 status.
var errUnknownThreadForTest = errors.New("agentcomm: unknown thread")

// fakeMeshActions is an in-memory MeshActions good enough to exercise the /api/mesh/* HTTP handlers without
// a real orchestrator — mirrors fakeAgentMeshActions' role in agent_mesh_test.go. Unlike that fake, there is
// no token/actor plumbing here: every call on this surface is implicitly the human, exactly like the real
// adapter (cmd/afm/run_mesh.go) which never reads an actor from the HTTP layer at all.
type fakeMeshActions struct {
	mu sync.Mutex

	participants []agentcomm.Participant

	threads    []agentcomm.Thread
	threadsErr error

	openByReq map[string]agentcomm.Thread
	openCalls int
	lastOpen  MeshOpenThreadRequest

	closedThreads []string
	closeCalls    int

	sentByReq  map[string]MeshSendReceipt
	sendCalls  int
	lastSend   MeshSendRequest
	lastThread string

	threadPage     MeshMessagePage
	threadErr      error
	lastReadThread string

	inboxPage MeshMessagePage
	boardErr  error
	board     []agentcomm.BoardEntry

	attachByReq map[string]MeshAttachAndSendResult
	attachCalls int
	lastAttach  MeshAttachAndSendRequest
	attachErr   error
}

func newFakeMeshActions() *fakeMeshActions {
	return &fakeMeshActions{
		openByReq:   map[string]agentcomm.Thread{},
		sentByReq:   map[string]MeshSendReceipt{},
		attachByReq: map[string]MeshAttachAndSendResult{},
	}
}

func (f *fakeMeshActions) ListParticipants(ctx context.Context) ([]agentcomm.Participant, error) {
	return f.participants, nil
}

func (f *fakeMeshActions) Threads(ctx context.Context) ([]agentcomm.Thread, error) {
	if f.threadsErr != nil {
		return nil, f.threadsErr
	}
	return f.threads, nil
}

func (f *fakeMeshActions) OpenThread(ctx context.Context, requestID string, req MeshOpenThreadRequest) (agentcomm.Thread, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.openCalls++
	f.lastOpen = req
	if existing, ok := f.openByReq[requestID]; ok {
		return existing, nil
	}
	members := make([]agentcomm.ParticipantID, len(req.Members))
	for i, m := range req.Members {
		members[i] = agentcomm.ParticipantID(m)
	}
	th := agentcomm.Thread{
		ID: agentcomm.ThreadID("mt-" + requestID), Title: req.Title, Scope: agentcomm.Scope(req.Scope),
		Members: members, Status: agentcomm.ThreadOpen,
	}
	f.openByReq[requestID] = th
	return th, nil
}

func (f *fakeMeshActions) CloseThread(ctx context.Context, requestID, threadID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeCalls++
	f.closedThreads = append(f.closedThreads, threadID)
	return nil
}

func (f *fakeMeshActions) Send(ctx context.Context, requestID, threadID string, req MeshSendRequest) (MeshSendReceipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendCalls++
	f.lastSend = req
	f.lastThread = threadID
	if existing, ok := f.sentByReq[requestID]; ok {
		return existing, nil
	}
	receipt := MeshSendReceipt{MessageID: "msg-" + requestID, Seq: uint64(len(f.sentByReq) + 1)}
	f.sentByReq[requestID] = receipt
	return receipt, nil
}

func (f *fakeMeshActions) Thread(ctx context.Context, threadID string, cursor uint64, limit int) (MeshMessagePage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastReadThread = threadID
	if f.threadErr != nil {
		return MeshMessagePage{}, f.threadErr
	}
	return f.threadPage, nil
}

func (f *fakeMeshActions) Inbox(ctx context.Context, cursor uint64, limit int) (MeshMessagePage, error) {
	return f.inboxPage, nil
}

func (f *fakeMeshActions) Board(ctx context.Context, key string) ([]agentcomm.BoardEntry, error) {
	if f.boardErr != nil {
		return nil, f.boardErr
	}
	return f.board, nil
}

func (f *fakeMeshActions) AttachAndSend(ctx context.Context, requestID string, req MeshAttachAndSendRequest) (MeshAttachAndSendResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attachCalls++
	f.lastAttach = req
	if f.attachErr != nil {
		return MeshAttachAndSendResult{}, f.attachErr
	}
	if existing, ok := f.attachByReq[requestID]; ok {
		return existing, nil
	}
	res := MeshAttachAndSendResult{
		TaskID: "task-" + requestID, ParticipantID: "helper:task-" + requestID, ThreadID: "mt-" + requestID,
	}
	if req.FirstMessage != nil {
		mid := "msg-" + requestID
		res.MessageID = &mid
	}
	f.attachByReq[requestID] = res
	return res, nil
}

func doMeshJSON(t *testing.T, srv *Server, method, path string, body any, sameOrigin bool) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	if sameOrigin {
		req.Header.Set("Origin", "http://"+req.Host)
	}
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	var resp map[string]any
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
	}
	return w, resp
}

func TestMesh_404WhenCapabilityOff(t *testing.T) {
	srv := newTestServer(t, Config{}) // no Mesh wired
	paths := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/mesh/participants"},
		{http.MethodGet, "/api/mesh/board"},
		{http.MethodGet, "/api/mesh/inbox"},
		{http.MethodGet, "/api/mesh/threads"},
		{http.MethodGet, "/api/mesh/threads/mt-1"},
		{http.MethodPost, "/api/mesh/threads"},
		{http.MethodPost, "/api/mesh/threads/mt-1/messages"},
		{http.MethodPost, "/api/mesh/threads/mt-1/close"},
		{http.MethodPost, "/api/mesh/attach-and-send"},
	}
	for _, c := range paths {
		req := httptest.NewRequest(c.method, c.path, nil)
		req.Header.Set("Origin", "http://"+req.Host)
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s %s: HTTP %d, want 404 when Mesh is nil", c.method, c.path, w.Code)
		}
	}
}

func TestMesh_Participants(t *testing.T) {
	fake := newFakeMeshActions()
	fake.participants = []agentcomm.Participant{{ID: "user", Kind: agentcomm.KindUser, Scope: agentcomm.ScopeRun, Reachable: true}}
	srv := newTestServer(t, Config{Mesh: fake})
	w, resp := doMeshJSON(t, srv, http.MethodGet, "/api/mesh/participants", nil, false)
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	participants, _ := resp["participants"].([]any)
	if len(participants) != 1 {
		t.Fatalf("participants = %v, want 1 entry", resp["participants"])
	}
}

// TestMesh_Threads covers GET /api/mesh/threads (bare, list form) — the T7.3 dependency this surface used to
// 404 on (see mesh.go's package doc comment history). Like TestMesh_Participants/TestMesh_Board, no Origin
// header is sent: this is a read, not a mutation.
func TestMesh_Threads(t *testing.T) {
	fake := newFakeMeshActions()
	fake.threads = []agentcomm.Thread{
		{ID: "mt-1", Title: "chat", Scope: agentcomm.ScopeStage, Members: []agentcomm.ParticipantID{"user", "stage:s1"}, Status: agentcomm.ThreadOpen},
	}
	srv := newTestServer(t, Config{Mesh: fake})
	w, resp := doMeshJSON(t, srv, http.MethodGet, "/api/mesh/threads", nil, false)
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	threads, _ := resp["threads"].([]any)
	if len(threads) != 1 {
		t.Fatalf("threads = %v, want 1 entry", resp["threads"])
	}
}

func TestMesh_Threads_ErrorSurfacesThroughWriteAssistantError(t *testing.T) {
	fake := newFakeMeshActions()
	fake.threadsErr = errUnknownThreadForTest
	srv := newTestServer(t, Config{Mesh: fake})
	w, _ := doMeshJSON(t, srv, http.MethodGet, "/api/mesh/threads", nil, false)
	if w.Code == http.StatusOK {
		t.Fatalf("HTTP %d, want a non-200 status when the Service call fails", w.Code)
	}
}

func TestMesh_OpenThread_RequiresOriginAndRequestID(t *testing.T) {
	fake := newFakeMeshActions()
	srv := newTestServer(t, Config{Mesh: fake})

	// Missing Origin -> 403, no call reaches the Service.
	w, _ := doMeshJSON(t, srv, http.MethodPost, "/api/mesh/threads", map[string]any{
		"request_id": "req-1", "title": "t", "scope": "stage", "member_ids": []string{"stage:s1"},
	}, false)
	if w.Code != http.StatusForbidden {
		t.Fatalf("no Origin: HTTP %d, want 403", w.Code)
	}

	// Foreign Origin -> 403.
	req := httptest.NewRequest(http.MethodPost, "/api/mesh/threads", bytes.NewReader([]byte(`{"request_id":"req-1"}`)))
	req.Header.Set("Origin", "http://evil.example.com")
	w2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w2, req)
	if w2.Code != http.StatusForbidden {
		t.Fatalf("foreign Origin: HTTP %d, want 403", w2.Code)
	}

	// Same-origin but missing request_id -> 400.
	w3, _ := doMeshJSON(t, srv, http.MethodPost, "/api/mesh/threads", map[string]any{
		"title": "t", "scope": "stage", "member_ids": []string{"stage:s1"},
	}, true)
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("missing request_id: HTTP %d, want 400", w3.Code)
	}

	if fake.openCalls != 0 {
		t.Fatalf("a rejected request must never reach the Service: openCalls=%d", fake.openCalls)
	}
}

func TestMesh_OpenThread_IdempotentRequestID(t *testing.T) {
	fake := newFakeMeshActions()
	srv := newTestServer(t, Config{Mesh: fake})

	body := map[string]any{"request_id": "req-open-1", "title": "QA sync", "scope": "stage", "member_ids": []string{"stage:s1"}}
	w1, r1 := doMeshJSON(t, srv, http.MethodPost, "/api/mesh/threads", body, true)
	w2, r2 := doMeshJSON(t, srv, http.MethodPost, "/api/mesh/threads", body, true)
	if w1.Code != http.StatusOK || w2.Code != http.StatusOK {
		t.Fatalf("codes = %d, %d, want 200, 200", w1.Code, w2.Code)
	}
	if r1["id"] != r2["id"] || r1["id"] == "" {
		t.Fatalf("thread ids = %v, %v, want the same non-empty id for a duplicate request_id", r1["id"], r2["id"])
	}
	if fake.openCalls != 2 {
		t.Fatalf("openCalls = %d, want 2 (the HTTP layer always calls through; idempotency is the Service's job)", fake.openCalls)
	}
}

func TestMesh_OpenThread_AlwaysIncludesTheHuman(t *testing.T) {
	// This documents the adapter-level behaviour (cmd/afm/run_mesh.go), not this fake: at the HTTP/interface
	// boundary, the caller-supplied member list is passed through untouched. The real adapter is what injects
	// "user" when absent. Here we only check the request reaches the Service with the members the client sent.
	fake := newFakeMeshActions()
	srv := newTestServer(t, Config{Mesh: fake})
	doMeshJSON(t, srv, http.MethodPost, "/api/mesh/threads", map[string]any{
		"request_id": "req-1", "title": "t", "scope": "stage", "member_ids": []string{"stage:s1"},
	}, true)
	if len(fake.lastOpen.Members) != 1 || fake.lastOpen.Members[0] != "stage:s1" {
		t.Fatalf("members = %v, want [stage:s1]", fake.lastOpen.Members)
	}
}

func TestMesh_Send_PathThreadIDWinsOverBody(t *testing.T) {
	fake := newFakeMeshActions()
	srv := newTestServer(t, Config{Mesh: fake})
	w, resp := doMeshJSON(t, srv, http.MethodPost, "/api/mesh/threads/mt-real/messages", map[string]any{
		"request_id": "req-send-1", "thread_id": "mt-spoofed", "body": "hello",
	}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if fake.lastThread != "mt-real" {
		t.Fatalf("thread passed to Send = %q, want the PATH's thread id %q, not the body's", fake.lastThread, "mt-real")
	}
	if resp["message_id"] == "" || resp["message_id"] == nil {
		t.Fatalf("resp = %v, want a message_id", resp)
	}
}

func TestMesh_Send_RequiresOriginAndRequestID(t *testing.T) {
	fake := newFakeMeshActions()
	srv := newTestServer(t, Config{Mesh: fake})

	w, _ := doMeshJSON(t, srv, http.MethodPost, "/api/mesh/threads/mt-1/messages", map[string]any{"body": "hi"}, false)
	if w.Code != http.StatusForbidden {
		t.Fatalf("no Origin: HTTP %d, want 403", w.Code)
	}
	w2, _ := doMeshJSON(t, srv, http.MethodPost, "/api/mesh/threads/mt-1/messages", map[string]any{"body": "hi"}, true)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("missing request_id: HTTP %d, want 400", w2.Code)
	}
	if fake.sendCalls != 0 {
		t.Fatalf("a rejected send must never reach the Service: sendCalls=%d", fake.sendCalls)
	}
}

func TestMesh_Send_IdempotentRequestID(t *testing.T) {
	fake := newFakeMeshActions()
	srv := newTestServer(t, Config{Mesh: fake})
	body := map[string]any{"request_id": "req-dup", "body": "hello"}
	_, r1 := doMeshJSON(t, srv, http.MethodPost, "/api/mesh/threads/mt-1/messages", body, true)
	_, r2 := doMeshJSON(t, srv, http.MethodPost, "/api/mesh/threads/mt-1/messages", body, true)
	if r1["message_id"] != r2["message_id"] {
		t.Fatalf("message ids = %v, %v, want the same for a duplicate request_id", r1["message_id"], r2["message_id"])
	}
}

func TestMesh_Send_RejectsOversizedBody(t *testing.T) {
	fake := newFakeMeshActions()
	srv := newTestServer(t, Config{Mesh: fake})
	huge := make([]byte, maxAgentMeshBodyBytes+1024)
	for i := range huge {
		huge[i] = 'a'
	}
	w, _ := doMeshJSON(t, srv, http.MethodPost, "/api/mesh/threads/mt-1/messages", map[string]any{
		"request_id": "req-1", "body": string(huge),
	}, true)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("HTTP %d, want 413", w.Code)
	}
}

func TestMesh_CloseThread(t *testing.T) {
	fake := newFakeMeshActions()
	srv := newTestServer(t, Config{Mesh: fake})
	w, resp := doMeshJSON(t, srv, http.MethodPost, "/api/mesh/threads/mt-1/close", map[string]any{"request_id": "req-1"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if resp[keyStatus] != "ok" {
		t.Fatalf("resp = %v, want status ok", resp)
	}
	if len(fake.closedThreads) != 1 || fake.closedThreads[0] != "mt-1" {
		t.Fatalf("closedThreads = %v, want [mt-1]", fake.closedThreads)
	}

	// Origin required.
	req := httptest.NewRequest(http.MethodPost, "/api/mesh/threads/mt-1/close", bytes.NewReader([]byte(`{"request_id":"req-2"}`)))
	w2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w2, req)
	if w2.Code != http.StatusForbidden {
		t.Fatalf("no Origin: HTTP %d, want 403", w2.Code)
	}
}

func TestMesh_ReadThread_CursorAndLimitPassThrough(t *testing.T) {
	fake := newFakeMeshActions()
	fake.threadPage = MeshMessagePage{Messages: []agentcomm.Message{{ID: "m1", ThreadID: "mt-1", Sender: "user", Recipients: []agentcomm.ParticipantID{"stage:s1"}, Kind: agentcomm.MessageKindMessage, Seq: 3}}, NextCursor: 3}
	srv := newTestServer(t, Config{Mesh: fake})
	w, _ := doMeshJSON(t, srv, http.MethodGet, "/api/mesh/threads/mt-1?cursor=2&limit=10", nil, false)
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if fake.lastReadThread != "mt-1" {
		t.Fatalf("thread id = %q, want mt-1", fake.lastReadThread)
	}
	var page MeshMessagePage
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(page.Messages) != 1 || page.NextCursor != 3 {
		t.Fatalf("page = %+v, want the fake's page", page)
	}
}

// TestMesh_ReadThread_MembershipRejectionSurfacesAsError documents that a non-member/unknown thread read
// (the Service's own ReadThread enforces this — pkg/orchestrator/agentcomm_messages.go) is surfaced as an
// HTTP error, never as a 200 with someone else's data: a thread id from a different run simply does not
// exist in THIS run's state, so ReadThread's "unknown thread" rejection is exactly what keeps a stage-scoped
// or foreign id from ever reaching another run's messages through this endpoint.
func TestMesh_ReadThread_MembershipRejectionSurfacesAsError(t *testing.T) {
	fake := newFakeMeshActions()
	fake.threadErr = errUnknownThreadForTest
	srv := newTestServer(t, Config{Mesh: fake})
	w, resp := doMeshJSON(t, srv, http.MethodGet, "/api/mesh/threads/mt-not-mine", nil, false)
	if w.Code == http.StatusOK {
		t.Fatalf("HTTP 200 with resp=%v, want an error status for an unknown/foreign thread", resp)
	}
}

func TestMesh_Board(t *testing.T) {
	fake := newFakeMeshActions()
	fake.board = []agentcomm.BoardEntry{{AuthorID: "user", Key: "status", Version: 1, Status: "ready"}}
	srv := newTestServer(t, Config{Mesh: fake})
	w, resp := doMeshJSON(t, srv, http.MethodGet, "/api/mesh/board?key=status", nil, false)
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	entries, _ := resp["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("entries = %v, want 1", resp["entries"])
	}
}

func TestMesh_Inbox_CursorAndLimitPassThrough(t *testing.T) {
	fake := newFakeMeshActions()
	fake.inboxPage = MeshMessagePage{NextCursor: 7}
	srv := newTestServer(t, Config{Mesh: fake})
	w, _ := doMeshJSON(t, srv, http.MethodGet, "/api/mesh/inbox?cursor=1&limit=5", nil, false)
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d, want 200", w.Code)
	}
	var page MeshMessagePage
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if page.NextCursor != 7 {
		t.Fatalf("NextCursor = %d, want 7", page.NextCursor)
	}
}

func TestMesh_AttachAndSend_RequiresOriginAndRequestID(t *testing.T) {
	fake := newFakeMeshActions()
	srv := newTestServer(t, Config{Mesh: fake})

	w, _ := doMeshJSON(t, srv, http.MethodPost, "/api/mesh/attach-and-send", map[string]any{
		"profile_id": "reviewer", "stage_id": "s1",
	}, false)
	if w.Code != http.StatusForbidden {
		t.Fatalf("no Origin: HTTP %d, want 403", w.Code)
	}

	w2, _ := doMeshJSON(t, srv, http.MethodPost, "/api/mesh/attach-and-send", map[string]any{
		"profile_id": "reviewer", "stage_id": "s1",
	}, true)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("missing request_id: HTTP %d, want 400", w2.Code)
	}
	if fake.attachCalls != 0 {
		t.Fatalf("a rejected attach-and-send must never reach the Service: attachCalls=%d", fake.attachCalls)
	}
}

func TestMesh_AttachAndSend_IgnoresClientSuppliedCommandOrModel(t *testing.T) {
	fake := newFakeMeshActions()
	srv := newTestServer(t, Config{Mesh: fake})
	w, resp := doMeshJSON(t, srv, http.MethodPost, "/api/mesh/attach-and-send", map[string]any{
		"request_id": "req-1", "profile_id": "reviewer", "stage_id": "s1", "instruction": "look at this",
		"command": "rm", "model": "gpt-99", "args": []string{"-rf", "/"},
	}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if fake.lastAttach.ProfileID != "reviewer" {
		t.Fatalf("ProfileID = %q, want reviewer", fake.lastAttach.ProfileID)
	}
	// MeshAttachAndSendRequest structurally has no Command/Model/Args field — there is nowhere for the
	// client-supplied values to have landed; this assertion documents that invariant.
	if resp["task_id"] == "" {
		t.Fatalf("resp = %v, want a task_id", resp)
	}
}

func TestMesh_AttachAndSend_RunScopeNoStageID(t *testing.T) {
	fake := newFakeMeshActions()
	srv := newTestServer(t, Config{Mesh: fake})
	w, _ := doMeshJSON(t, srv, http.MethodPost, "/api/mesh/attach-and-send", map[string]any{
		"request_id": "req-run-1", "profile_id": "qa", "scope": "run",
	}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d, want 200", w.Code)
	}
	if fake.lastAttach.StageID != "" {
		t.Fatalf("StageID = %q, want empty for scope=run", fake.lastAttach.StageID)
	}
}

// TestMesh_AttachAndSend_DuplicateRequestIDNoSecondHelper replays the identical POST twice and asserts the
// HTTP layer passes the SAME request_id through both times and gets back the same ids — the composite's own
// idempotent-per-operation guarantee is proven once, against the real orchestrator, by
// TestAttachAndSend_IdempotentPerOperation (pkg/orchestrator/agentcomm_attach_test.go); here we only check
// the HTTP adapter does not mint a fresh request_id or otherwise defeat that guarantee, and that no second
// call trying a NEW roster entry is implied by the HTTP layer re-sending.
func TestMesh_AttachAndSend_DuplicateRequestIDNoSecondHelper(t *testing.T) {
	fake := newFakeMeshActions()
	srv := newTestServer(t, Config{Mesh: fake})
	body := map[string]any{"request_id": "req-attach-dup", "profile_id": "reviewer", "stage_id": "s1", "instruction": "x"}
	_, r1 := doMeshJSON(t, srv, http.MethodPost, "/api/mesh/attach-and-send", body, true)
	_, r2 := doMeshJSON(t, srv, http.MethodPost, "/api/mesh/attach-and-send", body, true)
	if r1["task_id"] != r2["task_id"] || r1["thread_id"] != r2["thread_id"] {
		t.Fatalf("task/thread ids = %v/%v vs %v/%v, want identical for a replayed request_id",
			r1["task_id"], r1["thread_id"], r2["task_id"], r2["thread_id"])
	}
	if len(fake.attachByReq) != 1 {
		t.Fatalf("attachByReq has %d distinct results, want exactly 1 (no second helper created)", len(fake.attachByReq))
	}
}
