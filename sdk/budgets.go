package afmsdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// ErrBudgetsUnavailable means the afm behind the run has no budgets API: an older version, or one that serves
// the dashboard without it.
var ErrBudgetsUnavailable = errors.New("afmsdk: budgets are not available in this afm")

// Stop policies of a budget limit; an empty policy means hard.
const (
	BudgetPolicyHard   = "hard"
	BudgetPolicyMedium = "medium"
	BudgetPolicyInfo   = "info"
)

// Kinds of BudgetOverride.
const (
	BudgetOverrideNone      = "none"
	BudgetOverrideLimit     = "limit"
	BudgetOverrideUnlimited = "unlimited"
)

// Actions a BudgetView offers right now.
const (
	BudgetActionUpdate     = "update"
	BudgetActionResume     = "resume"
	BudgetActionSaveToYAML = "save_to_yaml"
)

// BudgetSpec is one limit; a zero metric is not set. The DTOs below mirror afm's JSON on pointers with omitempty:
// this module builds with Go 1.22, which ignores omitzero, and an empty {"spec":{}} would be rejected.
type BudgetSpec struct {
	Tokens     uint64  `json:"tokens,omitempty"`
	MoneyUSD   float64 `json:"money_usd,omitempty"`
	StopPolicy string  `json:"stop_policy,omitempty"`
}

// BudgetSpend is what a scope has spent; the counters say how many records could not contribute to a figure.
type BudgetSpend struct {
	Tokens       uint64 `json:"tokens"`
	MoneyNanoUSD int64  `json:"money_nano_usd"`
	Records      int    `json:"records"`
	Unmetered    int    `json:"unmetered,omitempty"`
	Unpriced     int    `json:"unpriced,omitempty"`
	NotExplicit  int    `json:"not_explicit,omitempty"`
}

// BudgetEvaluation is the level of each limited metric: "ok", "warning" (from 80%) or "reached"; empty without
// a limit on that metric.
type BudgetEvaluation struct {
	Tokens string `json:"tokens,omitempty"`
	Money  string `json:"money,omitempty"`
}

// BudgetOverride replaces the declared budget of a scope as a whole. Spec is set only for the limit kind.
type BudgetOverride struct {
	Kind string      `json:"kind"`
	Spec *BudgetSpec `json:"spec,omitempty"`
}

// BudgetHold is why a scope is not running; Scope is the one whose limit caused it.
type BudgetHold struct {
	Reason string `json:"reason"`
	Scope  string `json:"scope,omitempty"`
	Metric string `json:"metric,omitempty"`
}

// BudgetDeclaration points at the flow declaration of a scope's base budget.
type BudgetDeclaration struct {
	Kind     string `json:"kind"`
	StageID  string `json:"stage_id,omitempty"`
	Profile  string `json:"profile,omitempty"`
	MemberID string `json:"member_id,omitempty"`
}

// BudgetParentLimit is one ancestor limit that also constrains a scope.
type BudgetParentLimit struct {
	Scope      string           `json:"scope"`
	Limit      BudgetSpec       `json:"limit"`
	Spent      BudgetSpend      `json:"spent"`
	Evaluation BudgetEvaluation `json:"evaluation"`
}

// BudgetView is the engine's decision about one scope: clients display it and never recompute admission.
type BudgetView struct {
	Scope        string              `json:"scope"`
	Kind         string              `json:"kind"`
	Revision     int64               `json:"revision"`
	Base         *BudgetSpec         `json:"base,omitempty"`
	BaseSource   string              `json:"base_source"`
	Declaration  *BudgetDeclaration  `json:"declaration,omitempty"`
	Override     BudgetOverride      `json:"override"`
	Effective    *BudgetSpec         `json:"effective,omitempty"`
	Spent        BudgetSpend         `json:"spent"`
	Evaluation   BudgetEvaluation    `json:"evaluation"`
	ParentLimits []BudgetParentLimit `json:"parent_limits,omitempty"`
	Hold         *BudgetHold         `json:"hold,omitempty"`
	Draining     bool                `json:"draining,omitempty"`
	Actions      []string            `json:"actions,omitempty"`
}

// BudgetYAML says whether the limit of a scope can be saved to the flow file. Declared is the budget the file
// holds now; it differs from the view's base after a restart-less change of the file.
type BudgetYAML struct {
	Available bool        `json:"available"`
	Reason    string      `json:"reason,omitempty"`
	File      string      `json:"file"`
	Declared  *BudgetSpec `json:"declared"`
}

// BudgetDetail is one scope with its save-to-YAML block, present only when the scope can be saved.
type BudgetDetail struct {
	View BudgetView  `json:"view"`
	YAML *BudgetYAML `json:"yaml,omitempty"`
}

// BudgetUpdate changes the override of one scope. ExpectedRevision is the revision of the view the change is
// based on; 0 is a valid revision.
type BudgetUpdate struct {
	Scope            string         `json:"scope"`
	ExpectedRevision int64          `json:"expected_revision"`
	Override         BudgetOverride `json:"override"`
	SaveToYAML       bool           `json:"save_to_yaml,omitempty"`
	Resume           bool           `json:"resume,omitempty"`
}

// BudgetExportResult is the outcome of saving to the flow file: "saved", "failed" or "unavailable".
type BudgetExportResult struct {
	Status string `json:"status"`
	Path   string `json:"path,omitempty"`
	Error  string `json:"error,omitempty"`
}

// BudgetUpdateResult is the fresh view and, when saving was requested, its outcome.
type BudgetUpdateResult struct {
	View   BudgetView          `json:"view"`
	Export *BudgetExportResult `json:"export,omitempty"`
}

// BudgetError is a refusal of a budget command. Branch on Code, which is stable: budget_invalid,
// budget_unknown_scope, budget_revision_conflict, budget_still_limited, budget_rates_missing, budget_closed.
type BudgetError struct {
	Status  int
	Code    string
	Message string
}

func (e *BudgetError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("afmsdk: budget: %s", e.Code)
	}
	return fmt.Sprintf("afmsdk: budget: %s: %s", e.Code, e.Message)
}

// Budgets lists the budget view of every scope of the run (GET /api/budgets).
func (r *Run) Budgets(ctx context.Context) ([]BudgetView, error) {
	var out struct {
		Budgets []BudgetView `json:"budgets"`
	}
	if err := budgetError(r.getJSON(ctx, "/api/budgets", &out)); err != nil {
		return nil, err
	}
	return out.Budgets, nil
}

// Budget reads one scope: "run", "stage:<id>" or "task:<id>" (GET /api/budgets/view).
func (r *Run) Budget(ctx context.Context, scope string) (BudgetDetail, error) {
	var out BudgetDetail
	err := budgetError(r.getJSON(ctx, "/api/budgets/view?scope="+url.QueryEscape(scope), &out))
	return out, err
}

// budgetUpdateWire is the update response: the result plus the refusal of the optional continue.
type budgetUpdateWire struct {
	BudgetUpdateResult
	ResumeError *budgetResumeErrorWire `json:"resume_error"`
}

type budgetResumeErrorWire struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// UpdateBudget saves a new override and, with Resume, continues the scope. When the override was saved but the
// continue was refused (the limit still holds the scope), it returns the saved result together with a
// *BudgetError, so the caller can rebase on the new revision.
func (r *Run) UpdateBudget(ctx context.Context, req BudgetUpdate) (BudgetUpdateResult, error) {
	var out budgetUpdateWire
	if err := budgetError(r.postJSON(ctx, "/api/budgets/update", req, &out)); err != nil {
		return BudgetUpdateResult{}, err
	}
	if out.ResumeError != nil {
		return out.BudgetUpdateResult, &BudgetError{Status: http.StatusOK, Code: out.ResumeError.Code, Message: out.ResumeError.Message}
	}
	return out.BudgetUpdateResult, nil
}

// ResumeBudget continues a held scope without changing its limit (POST /api/budgets/resume).
func (r *Run) ResumeBudget(ctx context.Context, scope string, expectedRevision int64) (BudgetView, error) {
	var out struct {
		View BudgetView `json:"view"`
	}
	body := struct {
		Scope            string `json:"scope"`
		ExpectedRevision int64  `json:"expected_revision"`
	}{scope, expectedRevision}
	err := budgetError(r.postJSON(ctx, "/api/budgets/resume", body, &out))
	return out.View, err
}

// budgetError turns a rejected request into *BudgetError when its body is a budget error, and into
// ErrBudgetsUnavailable when the route does not exist; other errors pass through.
func budgetError(err error) error {
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		return err
	}
	var body struct {
		Code    string `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(httpErr.Body), &body) == nil && body.Code != "" {
		return &BudgetError{Status: httpErr.StatusCode, Code: body.Code, Message: body.Message}
	}
	if httpErr.StatusCode == http.StatusNotFound {
		return ErrBudgetsUnavailable
	}
	return err
}
