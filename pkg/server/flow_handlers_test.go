package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/orchestrator"
)

// fakeFlowActions is the FlowActions test double: each method returns a
// preconfigured result/error, letting tests drive both the success path and
// every mapped orchestrator sentinel without a real orchestrator.
type fakeFlowActions struct {
	pauseIDs  []string
	pauseErr  error
	injectErr error
	cancelErr error
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
