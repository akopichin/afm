package afmsdk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// Codes of an *ApprovalError, mirroring the engine's conditional-approval conflicts.
const (
	ApprovalCodeStale       = "approval_stale"
	ApprovalCodeRunChanged  = "approval_run_changed"
	ApprovalCodeUnavailable = "approval_unavailable"
)

// ApprovalSnapshot is the plan content bound to one run, stage and approval generation.
type ApprovalSnapshot struct {
	RunID        string `json:"run_id"`
	StageID      string `json:"stage_id"`
	Revision     string `json:"revision"`
	PlanMarkdown string `json:"plan_markdown"`
}

// ApproveExpectedRequest names the run and the revision the caller reviewed; the stage comes from the call.
type ApproveExpectedRequest struct {
	RunID            string `json:"run_id"`
	ExpectedRevision string `json:"expected_revision"`
}

// ApproveExpectedResult acknowledges a committed approval.
type ApproveExpectedResult struct {
	RunID            string `json:"run_id"`
	StageID          string `json:"stage_id"`
	ApprovedRevision string `json:"approved_revision"`
	TransitionSeq    uint64 `json:"transition_seq"`
}

// ApprovalError is a refused conditional approval, e.g. Code == ApprovalCodeStale after a revised plan.
// It unwraps to the underlying *HTTPError.
type ApprovalError struct {
	Code string
	HTTP *HTTPError
}

func (e *ApprovalError) Error() string {
	return "afmsdk: approval refused: " + e.Code + ": " + e.HTTP.Error()
}

func (e *ApprovalError) Unwrap() error { return e.HTTP }

// ApprovalSnapshot reads the plan awaiting approval together with its revision
// (GET /api/stages/<id>/approval).
func (r *Run) ApprovalSnapshot(ctx context.Context, stageID string) (ApprovalSnapshot, error) {
	var snapshot ApprovalSnapshot
	err := r.getJSON(ctx, stagePath(stageID, "/approval"), &snapshot)
	return snapshot, asApprovalError(err)
}

// ApproveExpected approves the stage plan only if its revision still equals req.ExpectedRevision
// (POST /api/stages/<id>/approval). Unlike Approve it never approves a plan the caller has not seen.
func (r *Run) ApproveExpected(ctx context.Context, stageID string, req ApproveExpectedRequest) (ApproveExpectedResult, error) {
	var result ApproveExpectedResult
	data, err := r.request(ctx, http.MethodPost, stagePath(stageID, "/approval"), req, maxJSONBytes)
	if err != nil {
		return result, asApprovalError(err)
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return result, errors.Join(errors.New("afmsdk: decode approval result"), err)
	}
	return result, nil
}

// asApprovalError turns an HTTP refusal carrying {"error": code} into an *ApprovalError.
func asApprovalError(err error) error {
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		return err
	}
	var body struct {
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(httpErr.Body), &body) != nil || strings.TrimSpace(body.Error) == "" {
		return err
	}
	return &ApprovalError{Code: body.Error, HTTP: httpErr}
}
