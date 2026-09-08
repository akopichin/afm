package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

// fakeFlowActions is the FlowActions test double: each method returns a
// preconfigured result/error, letting tests drive both the success path and
// every mapped orchestrator sentinel without a real orchestrator.
type fakeFlowActions struct {
	pauseIDs  []string
	pauseErr  error
	injectErr error
	cancelErr error

	// reviewState backs the review() method value below, wired into
	// Config.ReviewState by tests that need handleNotesAdd/Update/Delete's
	// currentReviewState() gate to see something other than the "" zero
	// value (which review() reports as "none", matching the real nil-func
	// default in handleStatus).
	reviewState string

	addNoteResult state.ReviewNote
	addNoteRev    int
	addNoteErr    error

	updateNoteRev int
	updateNoteErr error

	deleteNoteRev int
	deleteNoteErr error

	listNotesResult state.ReviewNotes
	listNotesErr    error
}

func (f *fakeFlowActions) PauseFlow(context.Context) ([]string, error) {
	return f.pauseIDs, f.pauseErr
}

func (f *fakeFlowActions) InjectNotesAndResume(context.Context, string) error {
	return f.injectErr
}

func (f *fakeFlowActions) CancelNotesAndResume(context.Context) error {
	return f.cancelErr
}

// review is the Config.ReviewState method value for this fake.
func (f *fakeFlowActions) review() (string, []string) {
	if f.reviewState == "" {
		return "none", nil
	}
	return f.reviewState, nil
}

func (f *fakeFlowActions) AddNote(root, path string, line *int, text, contentSHA string, expectedRev int) (state.ReviewNote, int, error) {
	return f.addNoteResult, f.addNoteRev, f.addNoteErr
}

func (f *fakeFlowActions) UpdateNote(id, text string, expectedRev int) (int, error) {
	return f.updateNoteRev, f.updateNoteErr
}

func (f *fakeFlowActions) DeleteNote(id string, expectedRev int) (int, error) {
	return f.deleteNoteRev, f.deleteNoteErr
}

func (f *fakeFlowActions) ListNotes() (state.ReviewNotes, error) {
	return f.listNotesResult, f.listNotesErr
}

func TestFlowPause_Success(t *testing.T) {
	fa := &fakeFlowActions{pauseIDs: []string{"s1"}}
	srv := newTestServer(t, Config{FlowActions: fa})
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/flow/pause", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ids, ok := got["paused_stages"].([]any)
	if !ok || len(ids) != 1 {
		t.Fatalf("paused_stages missing or wrong length: %v", got)
	}
}

func TestFlowInject_ErrorCodes(t *testing.T) {
	fa := &fakeFlowActions{injectErr: orchestrator.ErrNoNotes}
	srv := newTestServer(t, Config{FlowActions: fa})
	rr := httptest.NewRecorder()
	body := strings.NewReader(`{"stage_id":"s1"}`)
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/flow/notes/inject", body))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("code=%d", rr.Code)
	}
	var got map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["error"] != codeNoNotes {
		t.Fatalf("error=%q", got["error"])
	}
}

func TestFlowCancel_Success(t *testing.T) {
	fa := &fakeFlowActions{}
	srv := newTestServer(t, Config{FlowActions: fa})
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/flow/notes/cancel", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body)
	}
}

func TestFlowRoutes_NilFlowActionsNotFound(t *testing.T) {
	srv := newTestServer(t, Config{})
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/flow/pause", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("code=%d", rr.Code)
	}
}

func TestFlowInject_InvalidBody(t *testing.T) {
	fa := &fakeFlowActions{}
	srv := newTestServer(t, Config{FlowActions: fa})
	rr := httptest.NewRecorder()
	body := strings.NewReader(`{}`)
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/flow/notes/inject", body))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("code=%d", rr.Code)
	}
	var got map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["error"] != "invalid_body" {
		t.Fatalf("error=%q", got["error"])
	}
}

// TestFlowNotes_AddRequiresPaused verifies the handler-level pause gate:
// adding a note while the flow isn't paused for review must 409
// flow_not_paused, even though the fake's AddNote itself would happily
// succeed (addNoteErr is unset) — the check has to happen in the handler via
// currentReviewState(), not rely on the orchestrator/fake to reject it.
func TestFlowNotes_AddRequiresPaused(t *testing.T) {
	fa := &fakeFlowActions{reviewState: "none"}
	srv := newTestServer(t, Config{FlowActions: fa, ReviewState: fa.review})
	rr := httptest.NewRecorder()
	body := strings.NewReader(`{"root":"project","path":"a.go","line":42,"text":"fix","content_sha":"sha256:x","expected_rev":0}`)
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/flow/notes", body))
	if rr.Code != http.StatusConflict {
		t.Fatalf("adding a note while not paused must 409, got %d body=%s", rr.Code, rr.Body)
	}
	var got map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["error"] != "flow_not_paused" {
		t.Fatalf("error=%q", got["error"])
	}
}

// TestFlowNotes_AddSuccess drives the happy path: flow paused, fake AddNote
// returns a note + rev, handler must echo both back as 200.
func TestFlowNotes_AddSuccess(t *testing.T) {
	line := 42
	fa := &fakeFlowActions{
		reviewState:   "paused",
		addNoteResult: state.ReviewNote{ID: "n1", Root: "project", Path: "a.go", Line: &line, Text: "fix"},
		addNoteRev:    1,
	}
	srv := newTestServer(t, Config{FlowActions: fa, ReviewState: fa.review})
	rr := httptest.NewRecorder()
	body := strings.NewReader(`{"root":"project","path":"a.go","line":42,"text":"fix","content_sha":"sha256:x","expected_rev":0}`)
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/flow/notes", body))
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body)
	}
	var got struct {
		Note state.ReviewNote `json:"note"`
		Rev  int              `json:"rev"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Note.ID != "n1" || got.Rev != 1 {
		t.Fatalf("got=%+v", got)
	}
}

// TestFlowNotes_AddRevConflict checks the ErrRevConflict -> 409 rev_conflict
// mapping (added by this task alongside stale_content/stale_line/empty_text).
func TestFlowNotes_AddRevConflict(t *testing.T) {
	fa := &fakeFlowActions{reviewState: "paused", addNoteErr: orchestrator.ErrRevConflict}
	srv := newTestServer(t, Config{FlowActions: fa, ReviewState: fa.review})
	rr := httptest.NewRecorder()
	body := strings.NewReader(`{"root":"project","path":"a.go","text":"fix","content_sha":"sha256:x","expected_rev":5}`)
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/flow/notes", body))
	if rr.Code != http.StatusConflict {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body)
	}
	var got map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["error"] != "rev_conflict" {
		t.Fatalf("error=%q", got["error"])
	}
}

// TestFlowNotes_List verifies GET /api/flow/notes returns rev+notes from the
// lock-free ListNotes path (no pause gate).
func TestFlowNotes_List(t *testing.T) {
	fa := &fakeFlowActions{listNotesResult: state.ReviewNotes{Rev: 3, Notes: []state.ReviewNote{{ID: "n1"}}}}
	srv := newTestServer(t, Config{FlowActions: fa})
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/flow/notes", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body)
	}
	var got struct {
		Rev   int                `json:"rev"`
		Notes []state.ReviewNote `json:"notes"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Rev != 3 || len(got.Notes) != 1 {
		t.Fatalf("got=%+v", got)
	}
}

// TestFlowNotes_UpdateAndDelete smoke-tests the {id}-path routes end to end
// (PUT/DELETE), including the id-extraction from the URL and the
// flow_not_paused gate mirrored from Add.
func TestFlowNotes_UpdateAndDelete(t *testing.T) {
	fa := &fakeFlowActions{reviewState: "paused", updateNoteRev: 2, deleteNoteRev: 3}
	srv := newTestServer(t, Config{FlowActions: fa, ReviewState: fa.review})

	rr := httptest.NewRecorder()
	body := strings.NewReader(`{"text":"updated","expected_rev":1}`)
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPut, "/api/flow/notes/n1", body))
	if rr.Code != http.StatusOK {
		t.Fatalf("update code=%d body=%s", rr.Code, rr.Body)
	}
	var got map[string]int
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["rev"] != 2 {
		t.Fatalf("update rev=%v", got)
	}

	rr = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, "/api/flow/notes/n1?expected_rev=2", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("delete code=%d body=%s", rr.Code, rr.Body)
	}
	got = nil
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["rev"] != 3 {
		t.Fatalf("delete rev=%v", got)
	}
}
