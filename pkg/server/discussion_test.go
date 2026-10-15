package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// fakeDiscussionService backs the discussion HTTP layer for tests.
type fakeDiscussionService struct {
	mu        sync.Mutex
	messages  []DiscussionMessageView
	postCalls int
	lastStage string
	lastBody  string
	lastReqID string
	postErr   error
}

func (f *fakeDiscussionService) Messages(stageID string, cursor uint64, limit int) ([]DiscussionMessageView, uint64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	start := int(cursor)
	if start > len(f.messages) {
		start = len(f.messages)
	}
	end := start + limit
	if end > len(f.messages) {
		end = len(f.messages)
	}
	page := f.messages[start:end]
	next := uint64(end)
	hasMore := end < len(f.messages)
	return page, next, hasMore, nil
}

func (f *fakeDiscussionService) PostMessage(ctx context.Context, stageID, requestID, body string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.postCalls++
	f.lastStage = stageID
	f.lastBody = body
	f.lastReqID = requestID
	if f.postErr != nil {
		return "", f.postErr
	}
	id := "dm-test"
	f.messages = append(f.messages, DiscussionMessageView{ID: id, AuthorKind: "human", Body: body})
	return id, nil
}

// reviseTrackingStageActions wraps fakeStageActions, counting Revise calls — used to prove a discussion
// message never auto-triggers Revise.
type reviseTrackingStageActions struct {
	fakeStageActions
	reviseCalls int
}

func (r *reviseTrackingStageActions) Revise(ctx context.Context, stageID, feedback string) (bool, uint64, error) {
	r.reviseCalls++
	return r.fakeStageActions.Revise(ctx, stageID, feedback)
}

func TestDiscussion_UserMessageIsNotAutoRevise(t *testing.T) {
	tracker := &reviseTrackingStageActions{}
	fake := &fakeDiscussionService{}
	srv := newTestServer(t, Config{Actions: tracker, DiscussionService: fake})

	body, _ := json.Marshal(map[string]string{"request_id": "req-1", "body": "what about the retry loop?"})
	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/discussion", bytes.NewReader(body))
	req.Header.Set("Origin", "http://"+req.Host)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if fake.postCalls != 1 {
		t.Fatalf("expected exactly one PostMessage call, got %d", fake.postCalls)
	}
	if tracker.reviseCalls != 0 {
		t.Fatalf("a discussion message must never auto-trigger Revise, got %d Revise calls", tracker.reviseCalls)
	}
}

func TestDiscussion_Pagination(t *testing.T) {
	fake := &fakeDiscussionService{}
	for i := 0; i < 5; i++ {
		fake.messages = append(fake.messages, DiscussionMessageView{ID: "dm-" + string(rune('a'+i)), Body: "msg"})
	}
	srv := newTestServer(t, Config{DiscussionService: fake})

	req := httptest.NewRequest(http.MethodGet, "/api/stages/"+testStageID+"/discussion?limit=2", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	var page struct {
		Messages   []DiscussionMessageView `json:"messages"`
		NextCursor uint64                  `json:"next_cursor"`
		HasMore    bool                    `json:"has_more"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(page.Messages) != 2 || !page.HasMore {
		t.Fatalf("expected a first page of 2 with has_more=true, got %+v", page)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/api/stages/"+testStageID+"/discussion?cursor="+itoa(page.NextCursor)+"&limit=2", nil)
	w2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w2, req2)
	var page2 struct {
		Messages   []DiscussionMessageView `json:"messages"`
		NextCursor uint64                  `json:"next_cursor"`
		HasMore    bool                    `json:"has_more"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &page2); err != nil {
		t.Fatalf("decode page2: %v", err)
	}
	if len(page2.Messages) != 2 || !page2.HasMore {
		t.Fatalf("expected a second page of 2 with has_more=true, got %+v", page2)
	}

	req3 := httptest.NewRequest(http.MethodGet, "/api/stages/"+testStageID+"/discussion?cursor="+itoa(page2.NextCursor)+"&limit=2", nil)
	w3 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w3, req3)
	var page3 struct {
		Messages []DiscussionMessageView `json:"messages"`
		HasMore  bool                    `json:"has_more"`
	}
	if err := json.Unmarshal(w3.Body.Bytes(), &page3); err != nil {
		t.Fatalf("decode page3: %v", err)
	}
	if len(page3.Messages) != 1 || page3.HasMore {
		t.Fatalf("expected the final page of 1 with has_more=false, got %+v", page3)
	}
}

func itoa(n uint64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestDiscussion_404WhenCapabilityOff(t *testing.T) {
	srv := newTestServer(t, Config{}) // no DiscussionService wired
	req := httptest.NewRequest(http.MethodGet, "/api/stages/"+testStageID+"/discussion", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("GET HTTP %d, want 404", w.Code)
	}

	body, _ := json.Marshal(map[string]string{"request_id": "req-1", "body": "x"})
	req2 := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/discussion", bytes.NewReader(body))
	req2.Header.Set("Origin", "http://"+req2.Host)
	w2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w2, req2)
	if w2.Code != http.StatusNotFound {
		t.Fatalf("POST HTTP %d, want 404", w2.Code)
	}
}
