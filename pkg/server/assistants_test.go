package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/state"
)

// fakeAssistantActions/fakeAssistantsReader back the HTTP layer under test with an in-memory, idempotent
// implementation good enough to exercise the handlers without a real orchestrator.
type fakeAssistantActions struct {
	mu              sync.Mutex
	byRequestID     map[string]string // request_id -> task_id (idempotency)
	nextID          int
	lastAttach      AttachRequest
	lastStage       string
	attachErr       error
	attachCalls     int
	paused          []string
	continued       []string
	cancelled       []string
	waived          []string
	lastWaiveReason string
	actionErr       error
	policyRevision  string
	maxDepth        int
}

func (f *fakeAssistantActions) PolicyRevision() (string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.policyRevision, f.maxDepth
}

func (f *fakeAssistantActions) Attach(ctx context.Context, stageID string, req AttachRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attachCalls++
	f.lastAttach = req
	f.lastStage = stageID
	if f.attachErr != nil {
		return "", f.attachErr
	}
	if f.byRequestID == nil {
		f.byRequestID = map[string]string{}
	}
	if id, ok := f.byRequestID[req.RequestID]; ok {
		return id, nil
	}
	f.nextID++
	id := fmt.Sprintf("t-%d", f.nextID)
	f.byRequestID[req.RequestID] = id
	return id, nil
}

func (f *fakeAssistantActions) PauseTask(ctx context.Context, taskID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.actionErr != nil {
		return f.actionErr
	}
	f.paused = append(f.paused, taskID)
	return nil
}

func (f *fakeAssistantActions) ContinueTask(ctx context.Context, taskID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.actionErr != nil {
		return f.actionErr
	}
	f.continued = append(f.continued, taskID)
	return nil
}

func (f *fakeAssistantActions) CancelTask(ctx context.Context, taskID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.actionErr != nil {
		return f.actionErr
	}
	f.cancelled = append(f.cancelled, taskID)
	return nil
}

func (f *fakeAssistantActions) WaiveTask(ctx context.Context, taskID string, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.actionErr != nil {
		return f.actionErr
	}
	f.waived = append(f.waived, taskID)
	f.lastWaiveReason = reason
	return nil
}

type fakeAssistantsReader struct {
	tree      []AssistantTaskView
	treeErr   error
	events    []AssistantEventView
	hasMore   bool
	eventsErr error
}

func (f *fakeAssistantsReader) TaskTree(stageID string) ([]AssistantTaskView, error) {
	if f.treeErr != nil {
		return nil, f.treeErr
	}
	return f.tree, nil
}

func (f *fakeAssistantsReader) TaskEvents(taskID string, cursor uint64, limit int) ([]AssistantEventView, uint64, bool, error) {
	if f.eventsErr != nil {
		return nil, 0, false, f.eventsErr
	}
	var page []AssistantEventView
	for _, e := range f.events {
		if e.Seq <= cursor {
			continue
		}
		if len(page) >= limit {
			break
		}
		page = append(page, e)
	}
	next := cursor
	if len(page) > 0 {
		next = page[len(page)-1].Seq
	}
	return page, next, f.hasMore, nil
}

func attachBody(requestID, profileID string) []byte {
	b, _ := json.Marshal(map[string]any{
		"request_id": requestID, "profile_id": profileID, "instruction": "do it",
	})
	return b
}

func TestAttach_RequiresRequestId(t *testing.T) {
	fake := &fakeAssistantActions{}
	srv := newTestServer(t, Config{AssistantActions: fake})
	body, _ := json.Marshal(map[string]string{"profile_id": "reviewer"})
	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/assistants", bytes.NewReader(body))
	req.Header.Set("Origin", "http://"+req.Host)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("HTTP %d, want 400 (body=%s)", w.Code, w.Body.String())
	}
}

func TestAttach_RejectsOversizedBody(t *testing.T) {
	fake := &fakeAssistantActions{}
	srv := newTestServer(t, Config{AssistantActions: fake})
	huge := make([]byte, maxAttachBodyBytes+1024)
	for i := range huge {
		huge[i] = 'a'
	}
	body, _ := json.Marshal(map[string]string{"request_id": "req-1", "profile_id": "reviewer", "instruction": string(huge)})
	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/assistants", bytes.NewReader(body))
	req.Header.Set("Origin", "http://"+req.Host)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("HTTP %d, want 413 (body=%s)", w.Code, w.Body.String())
	}
}

func TestAttach_DuplicateRequestIdReturnsSameTask(t *testing.T) {
	fake := &fakeAssistantActions{}
	srv := newTestServer(t, Config{AssistantActions: fake})

	post := func() (int, string) {
		req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/assistants", bytes.NewReader(attachBody("req-1", "reviewer")))
		req.Header.Set("Origin", "http://"+req.Host)
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		var resp map[string]string
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return w.Code, resp["task_id"]
	}

	code1, id1 := post()
	code2, id2 := post()
	if code1 != http.StatusOK || code2 != http.StatusOK {
		t.Fatalf("codes = %d, %d, want 200, 200", code1, code2)
	}
	if id1 == "" || id1 != id2 {
		t.Fatalf("task ids = %q, %q, want the same non-empty id for a duplicate request_id", id1, id2)
	}
}

func TestAttach_RejectsClientSuppliedCommand(t *testing.T) {
	fake := &fakeAssistantActions{}
	srv := newTestServer(t, Config{AssistantActions: fake})
	body, _ := json.Marshal(map[string]any{
		"request_id": "req-1", "profile_id": "reviewer", "instruction": "x",
		"command": "rm", "args": []string{"-rf", "/"}, "env": map[string]string{"A": "B"}, "cwd": "/etc",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/assistants", bytes.NewReader(body))
	req.Header.Set("Origin", "http://"+req.Host)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if fake.lastAttach.ProfileID != "reviewer" {
		t.Fatalf("ProfileID = %q, want reviewer", fake.lastAttach.ProfileID)
	}
	// AttachRequest structurally has no Command/Args/Env/Cwd field — there is nowhere for the client-supplied
	// values to have landed; this assertion documents that invariant rather than re-deriving it.
}

func TestAttach_RejectsClientSuppliedDepthOrParentRoot(t *testing.T) {
	fake := &fakeAssistantActions{}
	srv := newTestServer(t, Config{AssistantActions: fake})
	body, _ := json.Marshal(map[string]any{
		"request_id": "req-1", "profile_id": "reviewer", "instruction": "x",
		"depth": 99, "root_stage_id": "some-other-stage", "parent_task_id": "t-not-mine",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/assistants", bytes.NewReader(body))
	req.Header.Set("Origin", "http://"+req.Host)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if fake.lastStage != testStageID {
		t.Fatalf("stage passed to Attach = %q, want the PATH's stage id %q, not anything from the body", fake.lastStage, testStageID)
	}
}

func TestAttach_404WhenCapabilityOff(t *testing.T) {
	srv := newTestServer(t, Config{}) // no AssistantActions wired
	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/assistants", bytes.NewReader(attachBody("req-1", "reviewer")))
	req.Header.Set("Origin", "http://"+req.Host)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("HTTP %d, want 404", w.Code)
	}

	// GET (list) must also 404 when the reader capability is off.
	req2 := httptest.NewRequest(http.MethodGet, "/api/stages/"+testStageID+"/assistants", nil)
	w2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w2, req2)
	if w2.Code != http.StatusNotFound {
		t.Fatalf("GET HTTP %d, want 404", w2.Code)
	}
}

// TestPauseTask_404WhenActionsOffButReaderOn is a regression test: AssistantsReader wired but AssistantActions
// nil (read-only collaboration) is an explicitly valid, already-tested config (see TestAttach_404WhenCapabilityOff's
// analog on the attach path) — a POST to pause/continue/cancel under exactly this config must 404, not panic.
// Before the fix, routeAssistants formed the method value `s.assistantActions.PauseTask` (etc.) BEFORE
// handleAssistantTaskAction's nil check ever ran: in Go, forming a method value on a nil interface panics
// immediately at the point of formation, not when called — so this config crashed the handler instead of
// reporting 404 as documented.
func TestPauseTask_404WhenActionsOffButReaderOn(t *testing.T) {
	srv := newTestServer(t, Config{AssistantsReader: &fakeAssistantsReader{}}) // reader on, actions off

	for _, action := range []string{"pause", "continue", "cancel"} {
		req := httptest.NewRequest(http.MethodPost, "/api/assistants/t-1/"+action, nil)
		req.Header.Set("Origin", "http://"+req.Host)
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("POST .../%s with AssistantActions=nil: HTTP %d, want 404", action, w.Code)
		}
	}
}

func TestAttach_OriginCheckedForBrowserMutations(t *testing.T) {
	fake := &fakeAssistantActions{}
	srv := newTestServer(t, Config{AssistantActions: fake})

	req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/assistants", bytes.NewReader(attachBody("req-1", "reviewer")))
	req.Header.Set("Origin", "http://evil.example.com")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("foreign origin: HTTP %d, want 403", w.Code)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/assistants", bytes.NewReader(attachBody("req-1", "reviewer")))
	w2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w2, req2)
	if w2.Code != http.StatusForbidden {
		t.Fatalf("missing origin: HTTP %d, want 403", w2.Code)
	}
}

// TestList_DepthPassesThrough is a regression test for T4a.8's breadcrumb wiring: the dashboard needs each
// task's Depth (1 for top-level, parent.Depth+1 for each delegated level) to compute "depth N of max_depth" —
// this must survive the HTTP round trip unchanged, not get dropped by the view's JSON tags.
func TestList_DepthPassesThrough(t *testing.T) {
	reader := &fakeAssistantsReader{tree: []AssistantTaskView{
		{ID: "t-1", ProfileID: "reviewer", Status: "completed", Depth: 1},
		{ID: "t-2", ParentTaskID: "t-1", ProfileID: "security", Status: "running", Depth: 2},
	}}
	srv := newTestServer(t, Config{AssistantsReader: reader})
	req := httptest.NewRequest(http.MethodGet, "/api/stages/"+testStageID+"/assistants", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Tasks []AssistantTaskView `json:"tasks"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byID := map[string]AssistantTaskView{}
	for _, tk := range resp.Tasks {
		byID[tk.ID] = tk
	}
	if byID["t-1"].Depth != 1 || byID["t-2"].Depth != 2 {
		t.Fatalf("depths = %+v, want t-1:1 t-2:2", byID)
	}
}

func TestList_NormalizedTreeNoDuplicateSubtrees(t *testing.T) {
	reader := &fakeAssistantsReader{tree: []AssistantTaskView{
		{ID: "t-1", ProfileID: "reviewer", Status: "completed"},
		{ID: "t-2", ParentTaskID: "t-1", ProfileID: "security", Status: "running"},
	}}
	srv := newTestServer(t, Config{AssistantsReader: reader})
	req := httptest.NewRequest(http.MethodGet, "/api/stages/"+testStageID+"/assistants", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Tasks []AssistantTaskView `json:"tasks"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	seen := map[string]int{}
	for _, tk := range resp.Tasks {
		seen[tk.ID]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("task %s appears %d times, want exactly once", id, n)
		}
	}
	if len(resp.Tasks) != 2 {
		t.Fatalf("got %d tasks, want 2", len(resp.Tasks))
	}
}

func TestEvents_CursorPagination(t *testing.T) {
	reader := &fakeAssistantsReader{events: []AssistantEventView{
		{Seq: 1, Type: "assistant_task_status"},
		{Seq: 2, Type: "assistant_task_status"},
		{Seq: 3, Type: "assistant_task_status"},
	}}
	srv := newTestServer(t, Config{AssistantsReader: reader})

	req := httptest.NewRequest(http.MethodGet, "/api/assistants/t-1/events?cursor=1&limit=1", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Events     []AssistantEventView `json:"events"`
		NextCursor uint64               `json:"next_cursor"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Events) != 1 || resp.Events[0].Seq != 2 {
		t.Fatalf("events = %+v, want exactly [{Seq:2}] (bounded page after cursor=1)", resp.Events)
	}
	if resp.NextCursor != 2 {
		t.Fatalf("next_cursor = %d, want 2", resp.NextCursor)
	}
}

func TestStatusCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"stale_revision", &state.OperationRejectedError{Code: assistants.CodeStaleRevision}, http.StatusConflict},
		{"invalid_status", &state.OperationRejectedError{Code: assistants.CodeInvalidStatus}, http.StatusConflict},
		{"capability_unavailable", &state.OperationRejectedError{Code: assistants.CodeCapabilityUnavailable}, http.StatusUnprocessableEntity},
		{"delegation_depth_exceeded", &state.OperationRejectedError{Code: assistants.CodeDelegationDepthExceeded}, http.StatusUnprocessableEntity},
		{"unavailable_service", ErrAssistantsUnavailable, http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeAssistantActions{attachErr: tc.err}
			srv := newTestServer(t, Config{AssistantActions: fake})
			req := httptest.NewRequest(http.MethodPost, "/api/stages/"+testStageID+"/assistants", bytes.NewReader(attachBody("req-1", "reviewer")))
			req.Header.Set("Origin", "http://"+req.Host)
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("HTTP %d, want %d (body=%s)", w.Code, tc.want, w.Body.String())
			}
		})
	}
}
