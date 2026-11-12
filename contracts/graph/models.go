// Package graph defines the values exchanged with the execution engine.
package graph

import (
	"encoding/json"
	"fmt"

	"github.com/akopichin/afm/contracts/budget"
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
	Budget  *budget.Spec `json:",omitempty"`
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

// UnmarshalJSON rejects an explicit null budget, which a *budget.Spec field would read as absent.
func (r *StageDraft) UnmarshalJSON(data []byte) error {
	if err := budget.RejectNullField(data, "Budget"); err != nil {
		return err
	}
	type plain StageDraft // no methods: decoding it does not recurse into UnmarshalJSON
	return json.Unmarshal(data, (*plain)(r))
}
