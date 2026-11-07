package jsonstrict

import (
	"encoding/json"
	"errors"
	"io"
)

// IssueKind separates format defects from outcomes a formatter must never change.
type IssueKind string

const (
	SyntaxIssue    IssueKind = "syntax"
	StructureIssue IssueKind = "structure"
	DomainIssue    IssueKind = "domain"
	RefusalIssue   IssueKind = "refusal"
)

// Issue preserves the original error for errors.Is/As and human diagnostics.
type Issue struct {
	Kind IssueKind
	Err  error
}

func (e *Issue) Error() string { return e.Err.Error() }
func (e *Issue) Unwrap() error { return e.Err }

func NewIssue(kind IssueKind, err error) error {
	if err == nil {
		return nil
	}
	return &Issue{Kind: kind, Err: err}
}

// Classify never guesses that an unclassified failure is safe to repair.
func Classify(err error) IssueKind {
	if err == nil {
		return ""
	}
	var issue *Issue
	if errors.As(err, &issue) {
		return issue.Kind
	}
	if errors.Is(err, ErrTooLarge) || errors.Is(err, ErrDuplicateKey) || errors.Is(err, ErrTrailingContent) || errors.Is(err, ErrMarkdownFence) {
		return RefusalIssue
	}
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return SyntaxIssue
	}
	var shape *json.UnmarshalTypeError
	if errors.As(err, &shape) {
		return StructureIssue
	}
	return RefusalIssue
}

// RepairKind describes only a candidate that has passed the caller's strict validator.
type RepairKind string

const (
	MechanicalRepair    RepairKind = "mechanical"
	SchemaNormalization RepairKind = "schema_normalization"
)
