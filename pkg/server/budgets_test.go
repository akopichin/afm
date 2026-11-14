package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/akopichin/afm-engine/engine"
	"github.com/akopichin/afm/contracts/budget"
)

// fakeBudgets records every call; err and updateErr script the failures.
type fakeBudgets struct {
	mu        sync.Mutex
	calls     []string
	view      budget.View
	updateRes budget.UpdateResult
	err       error
	updateErr error
	updateCtx context.Context
	noViews   bool // List answers nil, the engine's answer for a run without budget scopes
}

func (f *fakeBudgets) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *fakeBudgets) List(context.Context) ([]budget.View, error) {
	f.record("list")
	if f.err != nil {
		return nil, f.err
	}
	if f.noViews {
		return nil, nil
	}
	return []budget.View{f.view}, nil
}

func (f *fakeBudgets) View(context.Context, budget.ScopeID) (budget.View, error) {
	f.record("view")
	return f.view, f.err
}

func (f *fakeBudgets) Update(ctx context.Context, _ budget.UpdateRequest) (budget.UpdateResult, error) {
	f.record("update")
	f.updateCtx = ctx
	if f.updateErr != nil {
		return f.updateRes, f.updateErr
	}
	return f.updateRes, f.err
}

func (f *fakeBudgets) Resume(context.Context, budget.ResumeRequest) (budget.View, error) {
	f.record("resume")
	return f.view, f.err
}

func (f *fakeBudgets) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type fakeExportCheck struct{ reason string }

func (c fakeExportCheck) Check(budget.DeclarationRef) (string, string, *budget.Spec) {
	return "flow.yaml", c.reason, &budget.Spec{Tokens: 1000, StopPolicy: budget.PolicyHard}
}

func stageBudgetView(actions ...budget.Action) budget.View {
	return budget.View{
		Scope:       budget.StageScope("alpha"),
		Kind:        budget.ViewStage,
		Revision:    3,
		BaseSource:  budget.SourceDeclaration,
		Declaration: &budget.DeclarationRef{Kind: budget.DeclStage, StageID: "alpha"},
		Override:    budget.Override{Kind: budget.OverrideNone},
		Actions:     actions,
	}
}

func budgetServer(t *testing.T, fake *fakeBudgets, check BudgetExportCheck) *Server {
	t.Helper()
	return newTestServer(t, Config{Budgets: fake, BudgetExport: check})
}

func budgetRequest(srv *Server, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if method == http.MethodPost {
		req.Header.Set("Origin", "http://"+req.Host)
	}
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	return w
}

func decodeBody[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return out
}

const updateBody = `{"scope":"stage:alpha","expected_revision":3,"override":{"kind":"limit","spec":{"tokens":2000,"stop_policy":"hard"}}}`

func TestBudgets_CapabilityOffIs404AndFlagsFalse(t *testing.T) {
	srv := newTestServer(t, Config{})
	for _, route := range []string{"/api/budgets", "/api/budgets/view?scope=run", "/api/budgets/update"} {
		if w := budgetRequest(srv, http.MethodGet, route, ""); w.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", route, w.Code)
		}
	}
	caps := decodeStatus(t, srv).Capabilities
	if caps.Budgets || caps.BudgetExport {
		t.Errorf("capabilities = %+v, want budgets and budget_export off", caps)
	}
}

func TestBudgets_CapabilityFlags(t *testing.T) {
	on := decodeStatus(t, budgetServer(t, &fakeBudgets{}, fakeExportCheck{})).Capabilities
	if !on.Budgets || !on.BudgetExport {
		t.Errorf("capabilities = %+v, want both on", on)
	}
	noExport := decodeStatus(t, budgetServer(t, &fakeBudgets{}, nil)).Capabilities
	if !noExport.Budgets || noExport.BudgetExport {
		t.Errorf("capabilities = %+v, want budgets on, export off", noExport)
	}
}

func TestBudgets_GetNeverMutates(t *testing.T) {
	fake := &fakeBudgets{view: stageBudgetView(budget.ActionUpdate)}
	srv := budgetServer(t, fake, nil)
	for _, route := range []string{"/api/budgets", "/api/budgets/view?scope=stage:alpha"} {
		if w := budgetRequest(srv, http.MethodGet, route, ""); w.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", route, w.Code, w.Body)
		}
	}
	for _, call := range fake.calls {
		if call == "update" || call == "resume" {
			t.Errorf("GET called %s", call)
		}
	}
}

func TestBudgets_ListWithoutScopesIsEmptyArray(t *testing.T) {
	w := budgetRequest(budgetServer(t, &fakeBudgets{noViews: true}, nil), http.MethodGet, "/api/budgets", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"budgets":[`) {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
}

func TestBudgets_ViewRejectsBadScope(t *testing.T) {
	srv := budgetServer(t, &fakeBudgets{}, nil)
	for _, target := range []string{"/api/budgets/view", "/api/budgets/view?scope=bogus", "/api/budgets/view?scope=stage:"} {
		w := budgetRequest(srv, http.MethodGet, target, "")
		if w.Code != http.StatusBadRequest || decodeBody[budgetError](t, w).Code != budgetCodeInvalid {
			t.Errorf("%s: status %d body %s", target, w.Code, w.Body)
		}
	}
}

func TestBudgets_ViewYAMLBlock(t *testing.T) {
	type viewResp struct {
		View budget.View `json:"view"`
		YAML *budgetYAML `json:"yaml"`
	}
	withAction := &fakeBudgets{view: stageBudgetView(budget.ActionUpdate, budget.ActionSaveToYAML)}
	w := budgetRequest(budgetServer(t, withAction, fakeExportCheck{}), http.MethodGet, "/api/budgets/view?scope=stage:alpha", "")
	got := decodeBody[viewResp](t, w)
	if got.YAML == nil || !got.YAML.Available || got.YAML.File != "flow.yaml" || got.YAML.Declared == nil || got.YAML.Declared.Tokens != 1000 {
		t.Errorf("yaml block = %+v", got.YAML)
	}

	refused := budgetRequest(budgetServer(t, withAction, fakeExportCheck{reason: "flow file is a symbolic link"}), http.MethodGet, "/api/budgets/view?scope=stage:alpha", "")
	if y := decodeBody[viewResp](t, refused).YAML; y == nil || y.Available || y.Reason != "flow file is a symbolic link" {
		t.Errorf("refused yaml block = %+v", y)
	}

	noAction := &fakeBudgets{view: stageBudgetView(budget.ActionUpdate)}
	w = budgetRequest(budgetServer(t, noAction, fakeExportCheck{}), http.MethodGet, "/api/budgets/view?scope=stage:alpha", "")
	if strings.Contains(w.Body.String(), `"yaml"`) {
		t.Errorf("yaml block without save_to_yaml: %s", w.Body)
	}
}

func TestBudgets_UpdateRejectsBadBodies(t *testing.T) {
	srv := budgetServer(t, &fakeBudgets{}, nil)
	cases := map[string]string{
		"unknown field":      `{"scope":"run","expected_revision":1,"override":{"kind":"none"},"surprise":true}`,
		"missing revision":   `{"scope":"run","override":{"kind":"none"}}`,
		"missing override":   `{"scope":"run","expected_revision":1}`,
		"bad scope":          `{"scope":"nope","expected_revision":1,"override":{"kind":"none"}}`,
		"not json":           `{`,
		"limit without spec": `{"scope":"run","expected_revision":1,"override":{"kind":"limit"}}`,
	}
	for name, body := range cases {
		w := budgetRequest(srv, http.MethodPost, "/api/budgets/update", body)
		if w.Code != http.StatusBadRequest || decodeBody[budgetError](t, w).Code != budgetCodeInvalid {
			t.Errorf("%s: status %d body %s", name, w.Code, w.Body)
		}
	}
}

func TestBudgets_UpdateRejectsOversizedBody(t *testing.T) {
	fake := &fakeBudgets{}
	body := `{"scope":"run","expected_revision":1,"override":{"kind":"none"},"pad":"` + strings.Repeat("x", maxBudgetBodyBytes) + `"}`
	w := budgetRequest(budgetServer(t, fake, nil), http.MethodPost, "/api/budgets/update", body)
	if w.Code != http.StatusRequestEntityTooLarge || decodeBody[budgetError](t, w).Code != budgetCodeTooLarge {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
	if fake.callCount() != 0 {
		t.Error("an oversized body reached the engine")
	}
}

func TestBudgets_PostRejectsForeignOrigin(t *testing.T) {
	fake := &fakeBudgets{}
	srv := budgetServer(t, fake, nil)
	for _, route := range []string{"/api/budgets/update", "/api/budgets/resume"} {
		for _, origin := range []string{"", "http://evil.example"} {
			req := httptest.NewRequest(http.MethodPost, route, strings.NewReader(updateBody))
			if origin != "" {
				req.Header.Set("Origin", origin)
			}
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, req)
			if w.Code != http.StatusForbidden || decodeBody[budgetError](t, w).Code != budgetCodeForbiddenOrigin {
				t.Errorf("%s origin %q: status %d body %s", route, origin, w.Code, w.Body)
			}
		}
	}
	if fake.callCount() != 0 {
		t.Error("a foreign origin reached the engine")
	}
}

func TestBudgets_WrongMethod(t *testing.T) {
	srv := budgetServer(t, &fakeBudgets{}, nil)
	for target, method := range map[string]string{
		"/api/budgets/update": http.MethodGet,
		"/api/budgets/resume": http.MethodGet,
		"/api/budgets":        http.MethodPost,
		"/api/budgets/view":   http.MethodPost,
	} {
		if w := budgetRequest(srv, method, target, "{}"); w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s: status %d, want 405", method, target, w.Code)
		}
	}
}

func TestBudgets_EngineErrorsKeepTheirCode(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"conflict", &engine.Error{Kind: engine.KindConflict, Code: engine.CodeBudgetRevisionConflict, Message: "stale"}, http.StatusConflict, engine.CodeBudgetRevisionConflict},
		{"still limited", &engine.Error{Kind: engine.KindConflict, Code: engine.CodeBudgetStillLimited, Message: "limited"}, http.StatusConflict, engine.CodeBudgetStillLimited},
		{"unknown scope", &engine.Error{Kind: engine.KindNotFound, Code: engine.CodeBudgetUnknownScope, Message: "no scope"}, http.StatusNotFound, engine.CodeBudgetUnknownScope},
		{"invalid", &engine.Error{Kind: engine.KindInvalid, Code: engine.CodeBudgetInvalid, Message: "bad"}, http.StatusBadRequest, engine.CodeBudgetInvalid},
		{"closed", &engine.Error{Kind: engine.KindClosed, Message: "run is shutting down"}, http.StatusServiceUnavailable, budgetCodeClosed},
		{"unexpected", errors.New("disk exploded"), http.StatusInternalServerError, errCodeInternal},
	}
	for _, tc := range cases {
		srv := budgetServer(t, &fakeBudgets{err: tc.err, view: stageBudgetView()}, nil)
		for route, body := range map[string]string{
			"/api/budgets/update": updateBody,
			"/api/budgets/resume": `{"scope":"stage:alpha","expected_revision":3}`,
		} {
			w := budgetRequest(srv, http.MethodPost, route, body)
			if got := decodeBody[budgetError](t, w); w.Code != tc.status || got.Code != tc.code {
				t.Errorf("%s %s: status %d code %q, want %d %q", tc.name, route, w.Code, got.Code, tc.status, tc.code)
			}
		}
	}
}

func TestBudgets_UpdateWithRefusedResumeKeepsSavedResult(t *testing.T) {
	saved := stageBudgetView(budget.ActionUpdate)
	saved.Revision = 4
	fake := &fakeBudgets{
		updateRes: budget.UpdateResult{View: saved},
		updateErr: &engine.Error{Kind: engine.KindConflict, Code: engine.CodeBudgetStillLimited, Message: "still limited"},
	}
	w := budgetRequest(budgetServer(t, fake, nil), http.MethodPost, "/api/budgets/update", updateBody)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
	got := decodeBody[budgetUpdateResponse](t, w)
	if got.View.Revision != 4 {
		t.Errorf("revision = %d, want the saved 4", got.View.Revision)
	}
	if got.ResumeError == nil || got.ResumeError.Code != engine.CodeBudgetStillLimited {
		t.Errorf("resume_error = %+v", got.ResumeError)
	}
}

func TestBudgets_FailedExportTravelsInBody(t *testing.T) {
	fake := &fakeBudgets{updateRes: budget.UpdateResult{
		View:   stageBudgetView(),
		Export: &budget.ExportResult{Status: budget.ExportFailed, Path: "flow.yaml", Error: "flow file is a symbolic link"},
	}}
	w := budgetRequest(budgetServer(t, fake, nil), http.MethodPost, "/api/budgets/update", updateBody)
	got := decodeBody[budgetUpdateResponse](t, w)
	if w.Code != http.StatusOK || got.Export == nil || got.Export.Status != budget.ExportFailed || got.ResumeError != nil {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
}

func TestBudgets_ResumeReturnsView(t *testing.T) {
	fake := &fakeBudgets{view: stageBudgetView(budget.ActionUpdate)}
	w := budgetRequest(budgetServer(t, fake, nil), http.MethodPost, "/api/budgets/resume", `{"scope":"stage:alpha","expected_revision":3}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
	if got := decodeBody[struct{ View budget.View }](t, w); got.View.Scope != budget.StageScope("alpha") {
		t.Errorf("view scope = %q", got.View.Scope)
	}
}

func TestBudgets_CancelledRequestDoesNotCancelUpdate(t *testing.T) {
	fake := &fakeBudgets{updateRes: budget.UpdateResult{View: stageBudgetView()}}
	srv := budgetServer(t, fake, nil)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/api/budgets/update", strings.NewReader(updateBody)).WithContext(ctx)
	req.Header.Set("Origin", "http://"+req.Host)
	cancel()
	srv.Handler().ServeHTTP(httptest.NewRecorder(), req)
	if fake.updateCtx == nil || fake.updateCtx.Err() != nil {
		t.Errorf("update ran under a cancelled context: %v", fake.updateCtx)
	}
}

func TestBudgets_UnexpectedErrorDoesNotLeakDetails(t *testing.T) {
	leak := errors.New("open /srv/runs/r1/events.jsonl: disk exploded")
	srv := budgetServer(t, &fakeBudgets{err: leak, view: stageBudgetView()}, nil)
	for route, body := range map[string]string{
		"/api/budgets/update": updateBody,
		"/api/budgets/resume": `{"scope":"stage:alpha","expected_revision":3}`,
	} {
		w := budgetRequest(srv, http.MethodPost, route, body)
		if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "events.jsonl") {
			t.Errorf("%s: status %d body %s", route, w.Code, w.Body)
		}
	}
	if w := budgetRequest(srv, http.MethodGet, "/api/budgets", ""); strings.Contains(w.Body.String(), "events.jsonl") {
		t.Errorf("list leaks details: %s", w.Body)
	}
}
