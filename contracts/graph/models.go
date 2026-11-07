// Package graph defines the values exchanged with the execution engine.
package graph

import (
	"fmt"

	"github.com/akopichin/afm/contracts/flow"
)

type InsertionPlan struct {
	Stage     flow.Stage
	ExtraDeps map[string][]string
	Order     []string
	Warnings  []string
}

type StageDraft struct {
	ID      string
	Name    string
	Command string
	Prompt  string
}

type ValidationError struct {
	Code   string
	Reason string
}

const CodeCommandNotPrepared = "command_not_prepared"
const CodeDuplicateStageID = "duplicate_stage_id"
const CodeInvalidStageID = "invalid_stage_id"
const CodeTargetNotFound = "target_not_found"
const CodeUnknownDependency = "unknown_dependency"

func (e *ValidationError) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Reason) }
