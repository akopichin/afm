package afmsdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBudgetUpdate_GoldenJSON(t *testing.T) {
	limit := BudgetOverride{Kind: BudgetOverrideLimit, Spec: &BudgetSpec{Tokens: 500000, MoneyUSD: 2.5, StopPolicy: BudgetPolicyMedium}}
	cases := []struct {
		name string
		req  BudgetUpdate
		want string
	}{
		{"limit", BudgetUpdate{Scope: "stage:build", ExpectedRevision: 3, Override: limit, SaveToYAML: true, Resume: true},
			`{"scope":"stage:build","expected_revision":3,"override":{"kind":"limit","spec":{"tokens":500000,"money_usd":2.5,"stop_policy":"medium"}},"save_to_yaml":true,"resume":true}`},
		{"tokens only", BudgetUpdate{Scope: "run", ExpectedRevision: 0, Override: BudgetOverride{Kind: BudgetOverrideLimit, Spec: &BudgetSpec{Tokens: 1000}}},
			`{"scope":"run","expected_revision":0,"override":{"kind":"limit","spec":{"tokens":1000}}}`},
		{"none carries no spec", BudgetUpdate{Scope: "run", ExpectedRevision: 0, Override: BudgetOverride{Kind: BudgetOverrideNone}},
			`{"scope":"run","expected_revision":0,"override":{"kind":"none"}}`},
		{"unlimited carries no spec", BudgetUpdate{Scope: "task:t-1", ExpectedRevision: 7, Override: BudgetOverride{Kind: BudgetOverrideUnlimited}, Resume: true},
			`{"scope":"task:t-1","expected_revision":7,"override":{"kind":"unlimited"},"resume":true}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.req)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Errorf("json = %s\nwant   %s", got, tc.want)
			}
		})
	}
}

const heldViewJSON = `{"scope":"stage:build","kind":"stage","revision":2,"base":{"tokens":100,"stop_policy":"hard"},"base_source":"declaration",` +
	`"declaration":{"kind":"stage","stage_id":"build"},"override":{"kind":"limit","spec":{"tokens":50,"stop_policy":"hard"}},` +
	`"effective":{"tokens":50,"stop_policy":"hard"},"spent":{"tokens":115,"money_nano_usd":0,"records":2,"unpriced":2},` +
	`"evaluation":{"tokens":"reached"},"parent_limits":[{"scope":"run","limit":{"tokens":1000,"stop_policy":"hard"},"spent":{"tokens":115,"money_nano_usd":0,"records":2},"evaluation":{"tokens":"ok"}}],` +
	`"hold":{"reason":"limit_reached","scope":"stage:build","metric":"tokens"},"actions":["update","resume","save_to_yaml"]}`

func budgetServer(t *testing.T, handler http.HandlerFunc) *Run {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &Run{baseURL: srv.URL, httpClient: srv.Client()}
}

func TestBudgets_ListAndView(t *testing.T) {
	run := budgetServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/budgets":
			_, _ = fmt.Fprintf(w, `{"budgets":[%s]}`, heldViewJSON)
		case "/api/budgets/view":
			if got := r.URL.Query().Get("scope"); got != "stage:build" {
				t.Errorf("scope = %q", got)
			}
			_, _ = fmt.Fprintf(w, `{"view":%s,"yaml":{"available":true,"file":"flow.yaml","declared":{"tokens":100,"stop_policy":"hard"}}}`, heldViewJSON)
		default:
			http.NotFound(w, r)
		}
	})
	ctx := context.Background()

	views, err := run.Budgets(ctx)
	if err != nil || len(views) != 1 {
		t.Fatalf("Budgets = %+v, %v", views, err)
	}
	v := views[0]
	if v.Scope != "stage:build" || v.Revision != 2 || v.Hold == nil || v.Hold.Reason != "limit_reached" || v.Spent.Tokens != 115 ||
		v.Effective == nil || v.Effective.Tokens != 50 || v.Override.Spec == nil || len(v.ParentLimits) != 1 || v.ParentLimits[0].Scope != "run" {
		t.Errorf("view = %+v", v)
	}

	detail, err := run.Budget(ctx, "stage:build")
	if err != nil {
		t.Fatal(err)
	}
	if detail.YAML == nil || !detail.YAML.Available || detail.YAML.File != "flow.yaml" || detail.YAML.Declared == nil || detail.YAML.Declared.Tokens != 100 {
		t.Errorf("yaml = %+v", detail.YAML)
	}
}

func TestBudgets_ScopeIsQueryEscaped(t *testing.T) {
	run := budgetServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("scope"); got != "stage:a&b=c" {
			t.Errorf("scope = %q", got)
		}
		_, _ = fmt.Fprintf(w, `{"view":%s}`, heldViewJSON)
	})
	if _, err := run.Budget(context.Background(), "stage:a&b=c"); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateBudget_SendsStrictBodyWithOrigin(t *testing.T) {
	var body []byte
	run := budgetServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/budgets/update" || r.Header.Get("Origin") != "http://"+r.Host {
			t.Errorf("%s %s Origin=%q", r.Method, r.URL.Path, r.Header.Get("Origin"))
		}
		body, _ = io.ReadAll(r.Body)
		_, _ = fmt.Fprintf(w, `{"view":%s,"export":{"status":"saved","path":"flow.yaml"}}`, heldViewJSON)
	})
	res, err := run.UpdateBudget(context.Background(), BudgetUpdate{
		Scope: "stage:build", ExpectedRevision: 1, Override: BudgetOverride{Kind: BudgetOverrideNone}, SaveToYAML: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"scope":"stage:build","expected_revision":1,"override":{"kind":"none"},"save_to_yaml":true}` {
		t.Errorf("body = %s", body)
	}
	if res.View.Revision != 2 || res.Export == nil || res.Export.Status != "saved" || res.Export.Path != "flow.yaml" {
		t.Errorf("result = %+v", res)
	}
}

// A saved override whose continue was refused is a result and an error at once.
func TestUpdateBudget_PartialResultKeepsTheSavedView(t *testing.T) {
	run := budgetServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"view":%s,"resume_error":{"code":"budget_still_limited","message":"still limited by stage:build"}}`, heldViewJSON)
	})
	res, err := run.UpdateBudget(context.Background(), BudgetUpdate{Scope: "stage:build", Resume: true, Override: BudgetOverride{Kind: BudgetOverrideNone}})
	var budgetErr *BudgetError
	if !errors.As(err, &budgetErr) || budgetErr.Code != "budget_still_limited" || budgetErr.Message != "still limited by stage:build" {
		t.Fatalf("err = %v, want *BudgetError budget_still_limited", err)
	}
	if res.View.Revision != 2 || res.View.Scope != "stage:build" {
		t.Errorf("result = %+v, want the saved view", res)
	}
}

func TestBudgetCommands_TypedErrors(t *testing.T) {
	cases := []struct {
		status int
		code   string
	}{
		{http.StatusConflict, "budget_revision_conflict"},
		{http.StatusConflict, "budget_still_limited"},
		{http.StatusNotFound, "budget_unknown_scope"},
		{http.StatusBadRequest, "budget_invalid"},
		{http.StatusServiceUnavailable, "budget_closed"},
	}
	for _, tc := range cases {
		run := budgetServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = fmt.Fprintf(w, `{"error":%q,"message":"boom"}`, tc.code)
		})
		check := func(name string, err error) {
			t.Helper()
			var budgetErr *BudgetError
			if !errors.As(err, &budgetErr) || budgetErr.Status != tc.status || budgetErr.Code != tc.code || budgetErr.Message != "boom" {
				t.Errorf("%s %d %s: err = %#v", name, tc.status, tc.code, err)
			}
			if errors.Is(err, ErrBudgetsUnavailable) {
				t.Errorf("%s: a typed error must not read as unavailable", name)
			}
		}
		_, err := run.UpdateBudget(context.Background(), BudgetUpdate{Scope: "run", Override: BudgetOverride{Kind: BudgetOverrideNone}})
		check("update", err)
		_, err = run.ResumeBudget(context.Background(), "run", 0)
		check("resume", err)
		_, err = run.Budget(context.Background(), "run")
		check("view", err)
	}
}

// An afm that predates budgets has no routes: the SDK says so instead of returning an HTTP error.
func TestBudgets_OldServerIsUnavailable(t *testing.T) {
	run := budgetServer(t, http.NotFound)
	ctx := context.Background()
	if _, err := run.Budgets(ctx); !errors.Is(err, ErrBudgetsUnavailable) {
		t.Errorf("Budgets err = %v", err)
	}
	if _, err := run.Budget(ctx, "run"); !errors.Is(err, ErrBudgetsUnavailable) {
		t.Errorf("Budget err = %v", err)
	}
	if _, err := run.UpdateBudget(ctx, BudgetUpdate{Scope: "run", Override: BudgetOverride{Kind: BudgetOverrideNone}}); !errors.Is(err, ErrBudgetsUnavailable) {
		t.Errorf("UpdateBudget err = %v", err)
	}
	if _, err := run.ResumeBudget(ctx, "run", 0); !errors.Is(err, ErrBudgetsUnavailable) {
		t.Errorf("ResumeBudget err = %v", err)
	}
}

func TestBudgets_ServerFailureIsNotUnavailable(t *testing.T) {
	run := budgetServer(t, func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", http.StatusInternalServerError) })
	_, err := run.Budgets(context.Background())
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusInternalServerError || errors.Is(err, ErrBudgetsUnavailable) {
		t.Errorf("err = %v, want the plain HTTP error", err)
	}
}

func TestCapabilities_BudgetFlags(t *testing.T) {
	run := budgetServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"flow_name":"f","capabilities":{"budgets":true,"budget_export":true},"stages":[]}`)
	})
	status, err := run.Status(context.Background())
	if err != nil || !status.Capabilities.Budgets || !status.Capabilities.BudgetExport {
		t.Fatalf("capabilities = %+v, err %v", status.Capabilities, err)
	}
	old := budgetServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"flow_name":"f","capabilities":{"file_browser":true},"stages":[]}`)
	})
	status, err = old.Status(context.Background())
	if err != nil || status.Capabilities.Budgets || status.Capabilities.BudgetExport {
		t.Fatalf("old capabilities = %+v, err %v", status.Capabilities, err)
	}
}
