package server

// Этап 9 — pkg/server/graph.go shipped with zero tests. Mirrors proposals_test.go/assistants_test.go's
// conventions: a fake GraphActions, newTestServer(t, Config{...}), table-driven status-code checks.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/state"
)

// fakeGraphActions backs the dynamic-stage HTTP layer for tests.
type fakeGraphActions struct {
	previewErr error
	preview    orchestrator.AddStagePreview

	commitErr   error
	commitCalls map[string]int // RequestID -> count of DISTINCT commits actually performed
	commitFn    func(req orchestrator.AddStageDraft) (orchestrator.AddStageResult, error)

	revision int64
}

func (f *fakeGraphActions) PreviewAddStage(req orchestrator.AddStageDraft) (orchestrator.AddStagePreview, error) {
	if f.previewErr != nil {
		return orchestrator.AddStagePreview{}, f.previewErr
	}
	return f.preview, nil
}

func (f *fakeGraphActions) CommitAddStage(ctx context.Context, req orchestrator.AddStageDraft) (orchestrator.AddStageResult, error) {
	if f.commitErr != nil {
		return orchestrator.AddStageResult{}, f.commitErr
	}
	if f.commitFn != nil {
		return f.commitFn(req)
	}
	if f.commitCalls == nil {
		f.commitCalls = map[string]int{}
	}
	f.commitCalls[req.RequestID]++
	stage := flow.Stage{
		ID: req.Stage.ID, Name: req.Stage.Name, Command: req.Stage.Command, Prompt: req.Stage.Prompt,
	}
	return orchestrator.AddStageResult{Stage: stage, GraphRevision: f.revision}, nil
}

func (f *fakeGraphActions) GraphRevision() int64 { return f.revision }

func addStageBody(kind, id, cmd string, extra map[string]any) []byte {
	m := map[string]any{"kind": kind, "id": id, "command": cmd, "prompt": "p"}
	for k, v := range extra {
		m[k] = v
	}
	body, _ := json.Marshal(m)
	return body
}

func postGraph(t *testing.T, srv *Server, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Origin", "http://"+req.Host)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	return w
}

func TestGraphPreview_Success(t *testing.T) {
	fake := &fakeGraphActions{
		preview: orchestrator.AddStagePreview{
			Plan: graph.InsertionPlan{
				Stage:     flow.Stage{ID: "n", Command: "claude", Prompt: "p", DependsOn: []string{"a"}},
				ExtraDeps: map[string][]string{"t": {"n"}},
				Order:     []string{"a", "n", "t"},
			},
			GraphRevision: 3,
		},
	}
	srv := newTestServer(t, Config{GraphActions: fake})

	w := postGraph(t, srv, "/api/run/graph/preview", addStageBody("insert_before", "n", "claude", map[string]any{"target_id": "t"}))
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := resp["graph_revision"]; got != float64(3) {
		t.Fatalf("graph_revision = %v, want 3", got)
	}
	stage, ok := resp["stage"].(map[string]any)
	if !ok {
		t.Fatalf("response missing stage object: %+v", resp)
	}
	if stage["id"] != "n" {
		t.Fatalf("stage.id = %v, want n", stage["id"])
	}
	extraDeps, ok := stage["extra_deps"].(map[string]any)
	if !ok || len(extraDeps) == 0 {
		t.Fatalf("preview response must surface extra_deps for the dependency diff: %+v", stage)
	}
}

func TestGraphCommit_SuccessAndIdempotentReplay(t *testing.T) {
	fake := &fakeGraphActions{revision: 2}
	srv := newTestServer(t, Config{GraphActions: fake})

	body := addStageBody("append", "n", "claude", map[string]any{"request_id": "req-1"})
	w1 := postGraph(t, srv, "/api/run/graph/commit", body)
	w2 := postGraph(t, srv, "/api/run/graph/commit", body)
	if w1.Code != http.StatusOK || w2.Code != http.StatusOK {
		t.Fatalf("codes = %d, %d, want 200, 200 (bodies=%s / %s)", w1.Code, w2.Code, w1.Body.String(), w2.Body.String())
	}
	if fake.commitCalls["req-1"] != 2 {
		// The fake itself doesn't dedup (that's AddStageToGraph's/Orchestrator's job, already covered at the
		// orchestrator level) — this test only proves the HTTP layer forwards the SAME request_id both times
		// rather than inventing a new one, which is what would actually defeat idempotency.
		t.Fatalf("expected both POSTs to forward request_id=req-1 unchanged, got call count %d", fake.commitCalls["req-1"])
	}
}

func TestGraph_404WhenCapabilityOff(t *testing.T) {
	srv := newTestServer(t, Config{}) // no GraphActions wired

	w1 := postGraph(t, srv, "/api/run/graph/preview", addStageBody("append", "n", "claude", nil))
	if w1.Code != http.StatusNotFound {
		t.Fatalf("preview HTTP %d, want 404", w1.Code)
	}
	w2 := postGraph(t, srv, "/api/run/graph/commit", addStageBody("append", "n", "claude", map[string]any{"request_id": "req-1"}))
	if w2.Code != http.StatusNotFound {
		t.Fatalf("commit HTTP %d, want 404", w2.Code)
	}
}

func TestGraph_NonPostRejected(t *testing.T) {
	fake := &fakeGraphActions{}
	srv := newTestServer(t, Config{GraphActions: fake})

	req := httptest.NewRequest(http.MethodGet, "/api/run/graph/preview", nil)
	req.Header.Set("Origin", "http://"+req.Host)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET HTTP %d, want 405", w.Code)
	}
}

func TestGraph_WrongOriginRejected(t *testing.T) {
	fake := &fakeGraphActions{}
	srv := newTestServer(t, Config{GraphActions: fake})

	req := httptest.NewRequest(http.MethodPost, "/api/run/graph/preview", bytes.NewReader(addStageBody("append", "n", "claude", nil)))
	req.Header.Set("Origin", "http://evil.example.com")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("foreign origin: HTTP %d, want 403", w.Code)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/api/run/graph/preview", bytes.NewReader(addStageBody("append", "n", "claude", nil)))
	w2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w2, req2)
	if w2.Code != http.StatusForbidden {
		t.Fatalf("missing origin: HTTP %d, want 403", w2.Code)
	}
}

func TestGraph_OversizedBodyRejected(t *testing.T) {
	fake := &fakeGraphActions{}
	srv := newTestServer(t, Config{GraphActions: fake})

	huge := make([]byte, maxAddStageBodyBytes+1024)
	for i := range huge {
		huge[i] = 'a'
	}
	body, _ := json.Marshal(map[string]string{"kind": "append", "id": "n", "command": "claude", "prompt": string(huge)})
	w := postGraph(t, srv, "/api/run/graph/preview", body)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("HTTP %d, want 413 (body=%s)", w.Code, w.Body.String())
	}
}

func TestGraph_InvalidBodyRejected(t *testing.T) {
	fake := &fakeGraphActions{}
	srv := newTestServer(t, Config{GraphActions: fake})

	w := postGraph(t, srv, "/api/run/graph/preview", []byte("{not json"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("HTTP %d, want 400 (body=%s)", w.Code, w.Body.String())
	}
}

func TestGraph_UnknownSubPath404s(t *testing.T) {
	fake := &fakeGraphActions{}
	srv := newTestServer(t, Config{GraphActions: fake})

	w := postGraph(t, srv, "/api/run/graph/bogus", addStageBody("append", "n", "claude", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("HTTP %d, want 404", w.Code)
	}
}

// TestGraph_ErrorCodeMapping covers every error -> status mapping writeGraphError/graphRejectedStatus define.
func TestGraph_ErrorCodeMapping(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"flow_paused", orchestrator.ErrFlowPaused, http.StatusConflict, "flow_paused"},
		{"request_id_required", orchestrator.ErrRequestIDRequired, http.StatusBadRequest, "request_id_required"},
		{"invalid_kind", orchestrator.ErrUnknownInsertionKind, http.StatusBadRequest, "invalid_kind"},
		{"stale_revision", &state.OperationRejectedError{Code: assistants.CodeStaleRevision, Reason: "x"}, http.StatusConflict, "stale_revision"},
		{"invalid_status_rejected", &state.OperationRejectedError{Code: assistants.CodeInvalidStatus, Reason: "x"}, http.StatusConflict, "invalid_status"},
		{"run_finalizing", &state.OperationRejectedError{Code: assistants.CodeRunFinalizing, Reason: "x"}, http.StatusServiceUnavailable, "run_finalizing"},
		{"invalid_stage_id", &graph.ValidationError{Code: graph.CodeInvalidStageID, Reason: "x"}, http.StatusBadRequest, "invalid_stage_id"},
		{"duplicate_stage_id", &graph.ValidationError{Code: graph.CodeDuplicateStageID, Reason: "x"}, http.StatusBadRequest, "duplicate_stage_id"},
		{"unknown_dependency", &graph.ValidationError{Code: graph.CodeUnknownDependency, Reason: "x"}, http.StatusBadRequest, "unknown_dependency"},
		{"command_not_prepared", &graph.ValidationError{Code: graph.CodeCommandNotPrepared, Reason: "x"}, http.StatusBadRequest, "command_not_prepared"},
		{"target_not_found", &graph.ValidationError{Code: graph.CodeTargetNotFound, Reason: "x"}, http.StatusBadRequest, "target_not_found"},
		{"unmapped_internal", errUnmapped{}, http.StatusInternalServerError, "internal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeGraphActions{previewErr: tc.err}
			srv := newTestServer(t, Config{GraphActions: fake})
			w := postGraph(t, srv, "/api/run/graph/preview", addStageBody("append", "n", "claude", nil))
			if w.Code != tc.wantStatus {
				t.Fatalf("HTTP %d, want %d (body=%s)", w.Code, tc.wantStatus, w.Body.String())
			}
			var resp map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp["error"] != tc.wantCode {
				t.Fatalf("error code = %q, want %q", resp["error"], tc.wantCode)
			}
		})
	}
}

type errUnmapped struct{}

func (errUnmapped) Error() string { return "something unrelated broke" }
