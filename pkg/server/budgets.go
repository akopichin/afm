package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"slices"

	"github.com/akopichin/afm-engine/engine"
	"github.com/akopichin/afm/contracts/budget"
)

// maxBudgetBodyBytes bounds a budget command body; a valid request is far smaller.
const maxBudgetBodyBytes = 16 << 10

// Error codes of the budget routes that the engine does not name.
const (
	budgetCodeForbiddenOrigin = "forbidden_origin"
	budgetCodeMethod          = "method_not_allowed"
	budgetCodeTooLarge        = "request_too_large"
	budgetCodeInvalid         = "budget_invalid"
	budgetCodeClosed          = "budget_closed"
)

// BudgetActions is the budget surface of the run; the implementation is engine.Budgets. nil = capability off:
// the routes are not registered and answer 404.
type BudgetActions interface {
	List(ctx context.Context) ([]budget.View, error)
	View(ctx context.Context, scope budget.ScopeID) (budget.View, error)
	Update(ctx context.Context, req budget.UpdateRequest) (budget.UpdateResult, error)
	Resume(ctx context.Context, req budget.ResumeRequest) (budget.View, error)
}

// BudgetExportCheck tells, without side effects, whether a declaration can be saved to the flow file.
// reason is empty when saving is available; declared is the budget the file holds now.
type BudgetExportCheck interface {
	Check(ref budget.DeclarationRef) (file, reason string, declared *budget.Spec)
}

// budgetError is the JSON error body of the budget routes; clients branch on Code, not on the status.
type budgetError struct {
	Code    string `json:"error"`
	Message string `json:"message,omitempty"`
}

// budgetResumeError is a refused continue inside an update result, whose override was saved.
type budgetResumeError struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// budgetYAML is the save-to-YAML block of a scope view.
type budgetYAML struct {
	Available bool         `json:"available"`
	Reason    string       `json:"reason,omitempty"`
	File      string       `json:"file"`
	Declared  *budget.Spec `json:"declared"`
}

// budgetUpdateResponse is the update result; ResumeError is set when the limit was saved but continuing was refused.
type budgetUpdateResponse struct {
	budget.UpdateResult
	ResumeError *budgetResumeError `json:"resume_error,omitempty"`
}

func (s *Server) routeBudgets(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/budgets":
		s.handleBudgetList(w, r)
	case "/api/budgets/view":
		s.handleBudgetView(w, r)
	case "/api/budgets/update":
		s.handleBudgetUpdate(w, r)
	case "/api/budgets/resume":
		s.handleBudgetResume(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleBudgetList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeBudgetError(w, http.StatusMethodNotAllowed, budgetError{Code: budgetCodeMethod})
		return
	}
	views, err := s.budgets.List(r.Context())
	if err != nil {
		s.writeBudgetFailure(w, err)
		return
	}
	if views == nil {
		views = []budget.View{}
	}
	writeBudgetJSON(w, map[string]any{"budgets": views})
}

func (s *Server) handleBudgetView(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeBudgetError(w, http.StatusMethodNotAllowed, budgetError{Code: budgetCodeMethod})
		return
	}
	scope, err := budget.ParseScopeID(r.URL.Query().Get("scope"))
	if err != nil {
		writeBudgetError(w, http.StatusBadRequest, budgetError{Code: budgetCodeInvalid, Message: err.Error()})
		return
	}
	view, err := s.budgets.View(r.Context(), scope)
	if err != nil {
		s.writeBudgetFailure(w, err)
		return
	}
	resp := map[string]any{"view": view}
	if yaml := s.budgetYAMLBlock(view); yaml != nil {
		resp["yaml"] = yaml
	}
	writeBudgetJSON(w, resp)
}

// budgetYAMLBlock is nil unless the engine offers save_to_yaml for the scope.
func (s *Server) budgetYAMLBlock(view budget.View) *budgetYAML {
	if s.budgetExport == nil || view.Declaration == nil || !slices.Contains(view.Actions, budget.ActionSaveToYAML) {
		return nil
	}
	file, reason, declared := s.budgetExport.Check(*view.Declaration)
	return &budgetYAML{Available: reason == "", Reason: reason, File: file, Declared: declared}
}

func (s *Server) handleBudgetUpdate(w http.ResponseWriter, r *http.Request) {
	var req budget.UpdateRequest
	if !decodeBudgetRequest(w, r, &req) {
		return
	}
	// The request is accepted once the override is committed: a dropped connection must not cancel the
	// export or the resume, or the client would retry against a stale revision.
	result, err := s.budgets.Update(context.WithoutCancel(r.Context()), req)
	if err != nil && result.View.Scope == "" {
		s.writeBudgetFailure(w, err)
		return
	}
	resp := budgetUpdateResponse{UpdateResult: result}
	if err != nil {
		failure := s.budgetFailure(err)
		resp.ResumeError = &budgetResumeError{Code: failure.Code, Message: failure.Message}
	}
	writeBudgetJSON(w, resp)
}

func (s *Server) handleBudgetResume(w http.ResponseWriter, r *http.Request) {
	var req budget.ResumeRequest
	if !decodeBudgetRequest(w, r, &req) {
		return
	}
	view, err := s.budgets.Resume(context.WithoutCancel(r.Context()), req)
	if err != nil {
		s.writeBudgetFailure(w, err)
		return
	}
	writeBudgetJSON(w, map[string]any{"view": view})
}

// decodeBudgetRequest enforces the POST contract (same origin, bounded and strictly decoded body) and writes
// the refusal itself; false means the response is already written.
func decodeBudgetRequest(w http.ResponseWriter, r *http.Request, into any) bool {
	if r.Method != http.MethodPost {
		writeBudgetError(w, http.StatusMethodNotAllowed, budgetError{Code: budgetCodeMethod})
		return false
	}
	if !sameOriginOK(r) {
		writeBudgetError(w, http.StatusForbidden, budgetError{Code: budgetCodeForbiddenOrigin})
		return false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBudgetBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeBudgetError(w, http.StatusRequestEntityTooLarge, budgetError{Code: budgetCodeTooLarge})
			return false
		}
		writeBudgetError(w, http.StatusBadRequest, budgetError{Code: budgetCodeInvalid, Message: "cannot read the request body"})
		return false
	}
	// The contracts' UnmarshalJSON is the strict decoder: exact keys, required fields, validated values.
	if err := json.Unmarshal(body, into); err != nil {
		writeBudgetError(w, http.StatusBadRequest, budgetError{Code: budgetCodeInvalid, Message: err.Error()})
		return false
	}
	return true
}

// budgetFailure is the error body of a failed engine budget command.
func (s *Server) budgetFailure(err error) budgetError {
	var ce *engine.Error
	if !errors.As(err, &ce) {
		// An unexpected error may carry absolute paths: the client is pointed at the log instead.
		log.Printf("ERROR: budgets request failed: %q", s.redactSecrets(err.Error())) //nolint:gosec // G706: secrets redacted, %q escapes newlines so a value cannot forge log lines
		return budgetError{Code: errCodeInternal, Message: "internal error — see the afm log for details"}
	}
	code := ce.Code
	if code == "" {
		code = budgetCodeForKind(ce.Kind)
	}
	return budgetError{Code: code, Message: ce.Message}
}

func budgetCodeForKind(kind engine.Kind) string {
	switch kind {
	case engine.KindInvalid:
		return budgetCodeInvalid
	case engine.KindClosed:
		return budgetCodeClosed
	default:
		return errCodeInternal
	}
}

func (s *Server) writeBudgetFailure(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	var ce *engine.Error
	if errors.As(err, &ce) {
		status = commandErrorStatus(ce.Kind)
	}
	writeBudgetError(w, status, s.budgetFailure(err))
}

func writeBudgetError(w http.ResponseWriter, status int, body budgetError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeBudgetJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}
