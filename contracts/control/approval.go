package control

import (
	"fmt"
	"strings"
)

// ApprovalRevision is opaque: only the engine can bind it to a durable approval generation and plan bytes.
type ApprovalRevision string

// ApprovalSnapshot binds the reviewed content to one run, stage and approval generation.
// Engines must return unavailable when they cannot supply a consistent snapshot.
type ApprovalSnapshot struct {
	RunID        string           `json:"run_id"`
	StageID      string           `json:"stage_id"`
	Revision     ApprovalRevision `json:"revision"`
	PlanMarkdown string           `json:"plan_markdown"`
}

// ApproveExpectedRequest must be checked atomically with the durable transition, never as a preflight read.
type ApproveExpectedRequest struct {
	RunID            string           `json:"run_id"`
	StageID          string           `json:"stage_id"`
	ExpectedRevision ApprovalRevision `json:"expected_revision"`
}

// Validate checks required identifiers only; it cannot establish that a revision is current or authorized.
func (r ApproveExpectedRequest) Validate() error {
	for _, field := range []struct{ name, value string }{
		{"run_id", r.RunID},
		{"stage_id", r.StageID},
		{"expected_revision", string(r.ExpectedRevision)},
	} {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("%s is required", field.name)
		}
	}
	return nil
}

// ApproveExpectedResult acknowledges a committed transition; a rejected or unapplied command returns an error.
// It is not a durable deduplication receipt: a retry after a lost response may report a conflict.
type ApproveExpectedResult struct {
	RunID            string           `json:"run_id"`
	StageID          string           `json:"stage_id"`
	ApprovedRevision ApprovalRevision `json:"approved_revision"`
	TransitionSeq    uint64           `json:"transition_seq"`
}

// These codes supplement the engine's existing invalid, paused, closed and storage-error categories.
const (
	CodeApprovalStale       = "approval_stale"
	CodeApprovalRunChanged  = "approval_run_changed"
	CodeApprovalUnavailable = "approval_unavailable"
)
