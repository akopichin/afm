package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/state"
)

// fakeProposalActions backs the proposal HTTP layer for tests.
type fakeProposalActions struct {
	mu           sync.Mutex
	proposals    map[string][]ProposalView
	acceptCalls  map[string]int // proposal id -> count of DISTINCT request ids seen
	acceptedReqs map[string]map[string]bool
	acceptErr    error
	dismissErr   error
	lastDismiss  string
}

func (f *fakeProposalActions) Proposals(stageID string) ([]ProposalView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.proposals[stageID], nil
}

func (f *fakeProposalActions) Accept(ctx context.Context, proposalID, requestID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.acceptedReqs == nil {
		f.acceptedReqs = map[string]map[string]bool{}
	}
	if f.acceptedReqs[proposalID] == nil {
		f.acceptedReqs[proposalID] = map[string]bool{}
	}
	if f.acceptErr != nil {
		return f.acceptErr
	}
	if !f.acceptedReqs[proposalID][requestID] {
		f.acceptedReqs[proposalID][requestID] = true
		if f.acceptCalls == nil {
			f.acceptCalls = map[string]int{}
		}
		f.acceptCalls[proposalID]++
	}
	return nil
}

func (f *fakeProposalActions) Dismiss(ctx context.Context, proposalID, requestID, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastDismiss = proposalID
	return f.dismissErr
}

func TestAccept_RequiresExpectedRevision(t *testing.T) {
	fake := &fakeProposalActions{acceptErr: &state.OperationRejectedError{Code: assistants.CodeStaleRevision, Reason: "generation changed"}}
	srv := newTestServer(t, Config{ProposalActions: fake})

	body, _ := json.Marshal(map[string]string{"request_id": "req-1"})
	req := httptest.NewRequest(http.MethodPost, "/api/proposals/p-1/accept", bytes.NewReader(body))
	req.Header.Set("Origin", "http://"+req.Host)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("HTTP %d, want 409 (body=%s)", w.Code, w.Body.String())
	}
}

func TestAccept_IdempotentByRequestId(t *testing.T) {
	fake := &fakeProposalActions{}
	srv := newTestServer(t, Config{ProposalActions: fake})

	post := func() int {
		body, _ := json.Marshal(map[string]string{"request_id": "req-1"})
		req := httptest.NewRequest(http.MethodPost, "/api/proposals/p-1/accept", bytes.NewReader(body))
		req.Header.Set("Origin", "http://"+req.Host)
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		return w.Code
	}
	code1 := post()
	code2 := post()
	if code1 != http.StatusOK || code2 != http.StatusOK {
		t.Fatalf("codes = %d, %d, want 200, 200", code1, code2)
	}
	if fake.acceptCalls["p-1"] != 1 {
		t.Fatalf("expected exactly one DISTINCT accept for request_id req-1, got %d", fake.acceptCalls["p-1"])
	}
}

func TestDismiss_Works(t *testing.T) {
	fake := &fakeProposalActions{}
	srv := newTestServer(t, Config{ProposalActions: fake})
	body, _ := json.Marshal(map[string]string{"request_id": "req-1", "reason": "not needed"})
	req := httptest.NewRequest(http.MethodPost, "/api/proposals/p-1/dismiss", bytes.NewReader(body))
	req.Header.Set("Origin", "http://"+req.Host)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if fake.lastDismiss != "p-1" {
		t.Fatalf("lastDismiss = %q, want p-1", fake.lastDismiss)
	}
}

func TestProposals_404WhenCapabilityOff(t *testing.T) {
	srv := newTestServer(t, Config{}) // no ProposalActions wired

	req := httptest.NewRequest(http.MethodGet, "/api/stages/"+testStageID+"/proposals", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("GET proposals HTTP %d, want 404", w.Code)
	}

	body, _ := json.Marshal(map[string]string{"request_id": "req-1"})
	req2 := httptest.NewRequest(http.MethodPost, "/api/proposals/p-1/accept", bytes.NewReader(body))
	req2.Header.Set("Origin", "http://"+req2.Host)
	w2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w2, req2)
	if w2.Code != http.StatusNotFound {
		t.Fatalf("POST accept HTTP %d, want 404", w2.Code)
	}

	req3 := httptest.NewRequest(http.MethodPost, "/api/proposals/p-1/dismiss", bytes.NewReader(body))
	req3.Header.Set("Origin", "http://"+req3.Host)
	w3 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w3, req3)
	if w3.Code != http.StatusNotFound {
		t.Fatalf("POST dismiss HTTP %d, want 404", w3.Code)
	}
}
