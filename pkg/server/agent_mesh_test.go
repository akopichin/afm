package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
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

func (f *fakeAgentMeshActions) Inbox(ctx context.Context, actor string, cursor uint64, limit int) (MeshMessagePage, error) {
	return MeshMessagePage{}, nil
}

func (f *fakeAgentMeshActions) Thread(ctx context.Context, actor, threadID string, cursor uint64, limit int) (MeshMessagePage, error) {
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
