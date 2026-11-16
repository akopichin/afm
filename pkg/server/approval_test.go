package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/akopichin/afm-engine/engine"
	"github.com/akopichin/afm/contracts/control"
)

type fakeApproval struct {
	snapshot func(stageID string) (control.ApprovalSnapshot, error)
	approve  func(req control.ApproveExpectedRequest) (control.ApproveExpectedResult, error)
	calls    int
}

func (f *fakeApproval) ApprovalSnapshot(_ context.Context, stageID string) (control.ApprovalSnapshot, error) {
	f.calls++
	return f.snapshot(stageID)
}

func (f *fakeApproval) ApproveExpected(_ context.Context, req control.ApproveExpectedRequest) (control.ApproveExpectedResult, error) {
	f.calls++
	return f.approve(req)
}

func approvalServer(t *testing.T, fake *fakeApproval) *Server {
	t.Helper()
	return newTestServer(t, Config{Approval: fake})
}

func doLocal(srv *Server, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = "127.0.0.1:8080"
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	return w
}

func conflict(code string) error {
	return &engine.Error{Kind: engine.KindConflict, Code: code, Message: code}
}

func TestApprovalSnapshotReturnsPlan(t *testing.T) {
	fake := &fakeApproval{snapshot: func(id string) (control.ApprovalSnapshot, error) {
		return control.ApprovalSnapshot{RunID: "r1", StageID: id, Revision: "rev1", PlanMarkdown: "# plan"}, nil
	}}
	w := doLocal(approvalServer(t, fake), http.MethodGet, "/api/stages/s1/approval", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	var got control.ApprovalSnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Revision != "rev1" || got.StageID != "s1" || got.PlanMarkdown != "# plan" {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestApprovalSnapshotRejectsNonUTF8Plan(t *testing.T) {
	fake := &fakeApproval{snapshot: func(id string) (control.ApprovalSnapshot, error) {
		return control.ApprovalSnapshot{RunID: "r1", StageID: id, Revision: "rev1", PlanMarkdown: "plan \xff"}, nil
	}}
	w := doLocal(approvalServer(t, fake), http.MethodGet, "/api/stages/s1/approval", "")
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body %s", w.Code, w.Body)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body["error"] != control.CodeApprovalUnavailable {
		t.Fatalf("body = %s, want %s", w.Body, control.CodeApprovalUnavailable)
	}
	if strings.Contains(w.Body.String(), "rev1") {
		t.Fatal("unavailable response leaks the revision")
	}
}

// currentPlan is a fake engine's snapshot of stage s1 awaiting approval in the test server's run.
func currentPlan(srv *Server, revision, plan string) func(string) (control.ApprovalSnapshot, error) {
	return func(stageID string) (control.ApprovalSnapshot, error) {
		return control.ApprovalSnapshot{RunID: srv.runID(), StageID: stageID, Revision: control.ApprovalRevision(revision), PlanMarkdown: plan}, nil
	}
}

func approveBody(runID, revision string) string {
	return `{"run_id":"` + runID + `","expected_revision":"` + revision + `"}`
}

func TestApprovalApproveAppliesExpectedRevision(t *testing.T) {
	var seen control.ApproveExpectedRequest
	fake := &fakeApproval{approve: func(req control.ApproveExpectedRequest) (control.ApproveExpectedResult, error) {
		seen = req
		return control.ApproveExpectedResult{RunID: req.RunID, StageID: req.StageID, ApprovedRevision: req.ExpectedRevision, TransitionSeq: 9}, nil
	}}
	srv := approvalServer(t, fake)
	fake.snapshot = currentPlan(srv, "rev1", "# plan")
	w := doLocal(srv, http.MethodPost, "/api/stages/s1/approval", approveBody(srv.runID(), "rev1"))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	if seen.RunID != srv.runID() || seen.StageID != "s1" || seen.ExpectedRevision != "rev1" {
		t.Fatalf("engine request = %+v", seen)
	}
	var got control.ApproveExpectedResult
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.TransitionSeq != 9 {
		t.Fatalf("result = %+v, err %v", got, err)
	}
}

func TestApprovalConflictsReturn409WithCode(t *testing.T) {
	for _, code := range []string{control.CodeApprovalStale, control.CodeApprovalRunChanged, control.CodeApprovalUnavailable} {
		t.Run(code, func(t *testing.T) {
			fake := &fakeApproval{approve: func(control.ApproveExpectedRequest) (control.ApproveExpectedResult, error) {
				return control.ApproveExpectedResult{}, conflict(code)
			}}
			srv := approvalServer(t, fake)
			fake.snapshot = currentPlan(srv, "rev1", "# plan")
			w := doLocal(srv, http.MethodPost, "/api/stages/s1/approval", approveBody(srv.runID(), "rev1"))
			if w.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409", w.Code)
			}
			var body map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body["error"] != code {
				t.Fatalf("body = %s, want error %q", w.Body, code)
			}
		})
	}
}

func TestApprovalRejectsMalformedBody(t *testing.T) {
	bodies := map[string]string{
		"empty body":         ``,
		"null":               `null`,
		"unknown field":      `{"run_id":"r1","expected_revision":"rev1","force":true}`,
		"missing revision":   `{"run_id":"r1"}`,
		"empty revision":     `{"run_id":"r1","expected_revision":""}`,
		"missing run id":     `{"expected_revision":"rev1"}`,
		"trailing data":      `{"run_id":"r1","expected_revision":"rev1"} {}`,
		"not json":           `approve`,
		"stage id in body":   `{"run_id":"r1","stage_id":"s1","expected_revision":"rev1"}`,
		"wrong revision typ": `{"run_id":"r1","expected_revision":1}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			fake := &fakeApproval{}
			w := doLocal(approvalServer(t, fake), http.MethodPost, "/api/stages/s1/approval", body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", w.Code)
			}
			if fake.calls != 0 {
				t.Fatalf("engine called %d times for a rejected body", fake.calls)
			}
		})
	}
}

func TestApprovalRejectsInvalidStageID(t *testing.T) {
	fake := &fakeApproval{}
	w := doLocal(approvalServer(t, fake), http.MethodGet, "/api/stages/bad$id/approval", "")
	if w.Code != http.StatusBadRequest || fake.calls != 0 {
		t.Fatalf("status = %d, calls = %d", w.Code, fake.calls)
	}
}

func TestApprovalRouteIsOffWithoutActions(t *testing.T) {
	srv := newTestServer(t, Config{})
	if w := doLocal(srv, http.MethodGet, "/api/stages/s1/approval", ""); w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if decodeStatus(t, srv).Capabilities.ConditionalApproval {
		t.Fatal("conditional_approval advertised without ApprovalActions")
	}
}

func TestApprovalCapabilityAdvertised(t *testing.T) {
	srv := approvalServer(t, &fakeApproval{})
	if !decodeStatus(t, srv).Capabilities.ConditionalApproval {
		t.Fatal("conditional_approval not advertised")
	}
}

func TestApprovalGuardedByLocalHost(t *testing.T) {
	fake := &fakeApproval{}
	srv := approvalServer(t, fake)
	req := httptest.NewRequest(http.MethodPost, "/api/stages/s1/approval", strings.NewReader(`{"run_id":"r1","expected_revision":"rev1"}`))
	req.Host = "evil.example:8080"
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusForbidden || fake.calls != 0 {
		t.Fatalf("status = %d, calls = %d", w.Code, fake.calls)
	}
}

// The legacy /approve keeps accepting the dashboard's "null" body and the SDK's empty body.
func TestApprovalLegacyApproveUnchanged(t *testing.T) {
	for _, body := range []string{"null", ""} {
		srv, _ := setupTestServer(t)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/stages/s1/approve", strings.NewReader(body))
		srv.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("body %q: status = %d, %s", body, w.Code, w.Body)
		}
	}
}

func TestApprovalClassifyEngineError(t *testing.T) {
	got := ClassifyEngineError(conflict(control.CodeApprovalStale))
	if got.Status != http.StatusConflict || got.Kind != "conflict" || got.Code != control.CodeApprovalStale {
		t.Fatalf("classified = %+v", got)
	}
	plain := ClassifyEngineError(os.ErrClosed)
	if plain.Status != http.StatusInternalServerError || plain.Kind != "internal" || plain.Code != "" {
		t.Fatalf("plain error classified = %+v", plain)
	}
}

// The golden file pins the JSON field names SDK clients mirror; it must decode strictly into the contract types.
func TestApprovalContractGolden(t *testing.T) {
	data, err := os.ReadFile("../../sdk/testdata/approval-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden struct {
		Snapshot       json.RawMessage   `json:"snapshot"`
		ApproveRequest json.RawMessage   `json:"approve_request"`
		ApproveResult  json.RawMessage   `json:"approve_result"`
		ErrorCodes     map[string]string `json:"error_codes"`
	}
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatal(err)
	}
	roundTrip := func(name string, raw json.RawMessage, v any) {
		t.Helper()
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(v); err != nil {
			t.Fatalf("%s does not match contract type: %v", name, err)
		}
		again, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var want, got any
		_ = json.Unmarshal(raw, &want)
		_ = json.Unmarshal(again, &got)
		if !jsonEqual(want, got) {
			t.Fatalf("%s drifted: golden %s, contract %s", name, raw, again)
		}
	}
	roundTrip("snapshot", golden.Snapshot, &control.ApprovalSnapshot{})
	roundTrip("approve_result", golden.ApproveResult, &control.ApproveExpectedResult{})
	// The REST body omits stage_id, which comes from the URL.
	var req approvalRequest
	roundTrip("approve_request", golden.ApproveRequest, &req)
	wantCodes := map[string]string{
		"stale": control.CodeApprovalStale, "run_changed": control.CodeApprovalRunChanged, "unavailable": control.CodeApprovalUnavailable,
	}
	for k, v := range wantCodes {
		if golden.ErrorCodes[k] != v {
			t.Fatalf("error code %s = %q, want %q", k, golden.ErrorCodes[k], v)
		}
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// approvalStage is a fake engine with one stage awaiting approval: approving it moves the stage on, as the
// engine does, and a commit hook may change the plan between the preflight and the commit.
type approvalStage struct {
	srv      *Server
	revision control.ApprovalRevision
	plan     string
	approved bool
	commits  []control.ApproveExpectedRequest
	// beforeCommit runs inside ApproveExpected before the engine's own revision check.
	beforeCommit func(s *approvalStage)
}

func (a *approvalStage) fake() *fakeApproval {
	return &fakeApproval{
		snapshot: func(stageID string) (control.ApprovalSnapshot, error) {
			if stageID != "s1" {
				return control.ApprovalSnapshot{}, &engine.Error{Kind: engine.KindNotFound, Message: "stage not found"}
			}
			if a.approved {
				return control.ApprovalSnapshot{}, &engine.Error{Kind: engine.KindInvalid, Message: "stage is not awaiting approval"}
			}
			return control.ApprovalSnapshot{RunID: a.srv.runID(), StageID: stageID, Revision: a.revision, PlanMarkdown: a.plan}, nil
		},
		approve: func(req control.ApproveExpectedRequest) (control.ApproveExpectedResult, error) {
			a.commits = append(a.commits, req)
			if a.beforeCommit != nil {
				a.beforeCommit(a)
			}
			if a.approved || req.ExpectedRevision != a.revision {
				return control.ApproveExpectedResult{}, conflict(control.CodeApprovalStale)
			}
			a.approved = true
			return control.ApproveExpectedResult{RunID: req.RunID, StageID: req.StageID, ApprovedRevision: req.ExpectedRevision, TransitionSeq: 7}, nil
		},
	}
}

func newApprovalStage(t *testing.T, revision, plan string) (*approvalStage, *Server) {
	t.Helper()
	a := &approvalStage{revision: control.ApprovalRevision(revision), plan: plan}
	a.srv = approvalServer(t, a.fake())
	return a, a.srv
}

func wantApprovalError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var body map[string]string
	if w.Code != status || json.Unmarshal(w.Body.Bytes(), &body) != nil || body["error"] != code {
		t.Fatalf("answer = %d %s, want %d %s", w.Code, w.Body, status, code)
	}
}

// The POST validates the plan it would approve, not just GET: a revision of bytes no client could have been
// shown, or of a plan other than the current one, never reaches the engine.
func TestApprovalApproveNeedsTheShownCurrentPlan(t *testing.T) {
	cases := []struct {
		name, plan, current, requested, code string
		snapshot                             func(a *approvalStage) func(string) (control.ApprovalSnapshot, error)
	}{
		{name: "invalid UTF-8 of the requested revision", plan: "plan \xff", current: "rev-A", requested: "rev-A", code: control.CodeApprovalUnavailable},
		{name: "valid snapshot B, requested A", plan: "plan B", current: "rev-B", requested: "rev-A", code: control.CodeApprovalStale},
		{name: "snapshot of another stage", plan: "plan", current: "rev-A", requested: "rev-A", code: control.CodeApprovalUnavailable,
			snapshot: func(a *approvalStage) func(string) (control.ApprovalSnapshot, error) {
				return func(string) (control.ApprovalSnapshot, error) {
					return control.ApprovalSnapshot{RunID: a.srv.runID(), StageID: "s2", Revision: "rev-A", PlanMarkdown: "plan"}, nil
				}
			}},
		{name: "snapshot of another run", plan: "plan", current: "rev-A", requested: "rev-A", code: control.CodeApprovalRunChanged,
			snapshot: func(*approvalStage) func(string) (control.ApprovalSnapshot, error) {
				return func(stageID string) (control.ApprovalSnapshot, error) {
					return control.ApprovalSnapshot{RunID: "other", StageID: stageID, Revision: "rev-A", PlanMarkdown: "plan"}, nil
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &approvalStage{revision: control.ApprovalRevision(tc.current), plan: tc.plan}
			fake := a.fake()
			a.srv = approvalServer(t, fake)
			if tc.snapshot != nil {
				fake.snapshot = tc.snapshot(a)
			}
			w := doLocal(a.srv, http.MethodPost, "/api/stages/s1/approval", approveBody(a.srv.runID(), tc.requested))
			wantApprovalError(t, w, http.StatusConflict, tc.code)
			if len(a.commits) != 0 {
				t.Fatalf("ApproveExpected called %d times for a plan the client could not have reviewed", len(a.commits))
			}
			if tc.code == control.CodeApprovalUnavailable && strings.Contains(w.Body.String(), tc.current) {
				t.Fatalf("refusal leaks the revision: %s", w.Body)
			}
		})
	}
}

// The engine's atomic check stays authoritative: a plan that changes after the preflight is refused with the
// original revision, which is passed on unchanged and never replaced by the latest one.
func TestApprovalApproveKeepsEngineFenceAfterPreflight(t *testing.T) {
	a, srv := newApprovalStage(t, "rev-A", "plan A")
	a.beforeCommit = func(a *approvalStage) { a.revision, a.plan = "rev-B", "plan B" }

	w := doLocal(srv, http.MethodPost, "/api/stages/s1/approval", approveBody(srv.runID(), "rev-A"))

	wantApprovalError(t, w, http.StatusConflict, control.CodeApprovalStale)
	if len(a.commits) != 1 || a.commits[0].ExpectedRevision != "rev-A" || a.commits[0].StageID != "s1" {
		t.Fatalf("engine requests = %+v, want one with the client's revision", a.commits)
	}
	if a.approved {
		t.Fatal("a plan that changed before the commit was approved")
	}
}

// Two clients of one revision and a retry after a lost answer: one approve applies, the others are stale (not
// the 400 the snapshot of a stage that left approval would give).
func TestApprovalApproveTwiceIsStale(t *testing.T) {
	a, srv := newApprovalStage(t, "rev-A", "plan A")
	first := doLocal(srv, http.MethodPost, "/api/stages/s1/approval", approveBody(srv.runID(), "rev-A"))
	if first.Code != http.StatusOK {
		t.Fatalf("first approve = %d %s", first.Code, first.Body)
	}
	for _, client := range []string{"second client", "retry"} {
		w := doLocal(srv, http.MethodPost, "/api/stages/s1/approval", approveBody(srv.runID(), "rev-A"))
		if w.Code != http.StatusConflict {
			t.Fatalf("%s: status = %d %s", client, w.Code, w.Body)
		}
		wantApprovalError(t, w, http.StatusConflict, control.CodeApprovalStale)
	}
	if len(a.commits) != 1 {
		t.Fatalf("engine commits = %d, want 1", len(a.commits))
	}
}

func TestApprovalApproveKeepsErrorClasses(t *testing.T) {
	t.Run("foreign run reads nothing", func(t *testing.T) {
		fake := &fakeApproval{}
		srv := approvalServer(t, fake)
		w := doLocal(srv, http.MethodPost, "/api/stages/s1/approval", approveBody("other-run", "rev-A"))
		wantApprovalError(t, w, http.StatusConflict, control.CodeApprovalRunChanged)
		if fake.calls != 0 {
			t.Fatalf("a foreign run made %d engine calls", fake.calls)
		}
	})
	t.Run("unknown stage", func(t *testing.T) {
		a, srv := newApprovalStage(t, "rev-A", "plan")
		w := doLocal(srv, http.MethodPost, "/api/stages/nope/approval", approveBody(srv.runID(), "rev-A"))
		if w.Code != http.StatusNotFound || len(a.commits) != 0 {
			t.Fatalf("unknown stage = %d %s, commits %d", w.Code, w.Body, len(a.commits))
		}
	})
	for name, snapErr := range map[string]error{
		// The engine reports a missing, unreadable, oversize or symlinked plan.md as unavailable.
		"plan unreadable": conflict(control.CodeApprovalUnavailable),
		"run closing":     &engine.Error{Kind: engine.KindClosed, Message: "run is shutting down"},
		"storage failure": &engine.Error{Kind: engine.KindInternal, Message: "journal write failed"},
	} {
		t.Run(name, func(t *testing.T) {
			commits := 0
			fake := &fakeApproval{
				snapshot: func(string) (control.ApprovalSnapshot, error) { return control.ApprovalSnapshot{}, snapErr },
				approve: func(control.ApproveExpectedRequest) (control.ApproveExpectedResult, error) {
					commits++
					return control.ApproveExpectedResult{}, nil
				},
			}
			srv := approvalServer(t, fake)
			w := doLocal(srv, http.MethodPost, "/api/stages/s1/approval", approveBody(srv.runID(), "rev-A"))
			want := ClassifyEngineError(snapErr)
			if w.Code != want.Status || commits != 0 {
				t.Fatalf("answer = %d %s, want %d without a commit (commits %d)", w.Code, w.Body, want.Status, commits)
			}
			if want.Code != "" {
				wantApprovalError(t, w, want.Status, want.Code)
			}
		})
	}
	t.Run("engine refusal after preflight", func(t *testing.T) {
		paused := &engine.Error{Kind: engine.KindConflict, Message: "flow is paused for review"}
		a, srv := newApprovalStage(t, "rev-A", "plan")
		fake := srv.approval.(*fakeApproval)
		fake.approve = func(req control.ApproveExpectedRequest) (control.ApproveExpectedResult, error) {
			a.commits = append(a.commits, req)
			return control.ApproveExpectedResult{}, paused
		}
		w := doLocal(srv, http.MethodPost, "/api/stages/s1/approval", approveBody(srv.runID(), "rev-A"))
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "paused") || len(a.commits) != 1 {
			t.Fatalf("paused = %d %s, commits %d", w.Code, w.Body, len(a.commits))
		}
	})
}

func TestApprovalApproveAcceptsNonASCIIPlan(t *testing.T) {
	a, srv := newApprovalStage(t, "rev-A", "# План: «тесты» ✓")
	w := doLocal(srv, http.MethodPost, "/api/stages/s1/approval", approveBody(srv.runID(), "rev-A"))
	if w.Code != http.StatusOK || !a.approved {
		t.Fatalf("non-ASCII plan = %d %s", w.Code, w.Body)
	}
}
