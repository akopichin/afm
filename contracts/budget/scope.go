package budget

import (
	"fmt"
	"strings"
)

// ScopeKind is the kind of a budget scope; helpers and run agents are tasks, told apart by View.Kind.
type ScopeKind string

const (
	ScopeRun   ScopeKind = "run"
	ScopeStage ScopeKind = "stage"
	ScopeTask  ScopeKind = "task"
)

// ScopeID is the durable key of one budget counter: "run", "stage:<id>" or "task:<taskID>".
// It never uses names, titles or aliases, which can change.
type ScopeID string

func RunScope() ScopeID                 { return ScopeID(ScopeRun) }
func StageScope(stageID string) ScopeID { return ScopeID(string(ScopeStage) + ":" + stageID) }
func TaskScope(taskID string) ScopeID   { return ScopeID(string(ScopeTask) + ":" + taskID) }

// ParseScopeID validates the textual form of a scope ID.
func ParseScopeID(s string) (ScopeID, error) {
	if s == string(ScopeRun) {
		return RunScope(), nil
	}
	kind, id, ok := strings.Cut(s, ":")
	if !ok || id == "" || (kind != string(ScopeStage) && kind != string(ScopeTask)) {
		return "", fmt.Errorf("invalid budget scope %q (want run, stage:<id> or task:<id>)", s)
	}
	return ScopeID(s), nil
}

// Kind returns the scope kind, or "" for a malformed ID.
func (s ScopeID) Kind() ScopeKind {
	if _, err := ParseScopeID(string(s)); err != nil {
		return ""
	}
	kind, _, _ := strings.Cut(string(s), ":")
	return ScopeKind(kind)
}

// ObjectID returns the stage or task ID, or "" for the run scope or a malformed ID.
func (s ScopeID) ObjectID() string {
	if s.Kind() == ScopeRun || s.Kind() == "" {
		return ""
	}
	_, id, _ := strings.Cut(string(s), ":")
	return id
}
