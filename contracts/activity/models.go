// Package stagefiles defines the values exchanged with the execution engine.
package activity

import (
	"time"
)

type ActivityData struct {
	EventID       string `json:"event_id"`
	TaskID        string `json:"task_id"`
	ParticipantID string `json:"participant_id"`
	Turn          uint64 `json:"turn"`
	OccurredAt    string `json:"occurred_at"`
	Tool          string `json:"tool"`
	Detail        string `json:"detail"`
	Truncated     bool   `json:"truncated,omitempty"`
	Kind          string `json:"kind,omitempty"`
	InvocationID  string `json:"invocation_id,omitempty"`
	ItemID        string `json:"item_id,omitempty"`
	Revision      int    `json:"revision,omitempty"`
	Partial       bool   `json:"partial,omitempty"`
	Final         bool   `json:"final,omitempty"`
	Outcome       string `json:"outcome,omitempty"`
	ExitCode      *int   `json:"exit_code,omitempty"`
	StartedAt     string `json:"started_at,omitempty"`
	FinishedAt    string `json:"finished_at,omitempty"`
}

type ActivityEntry struct {
	ActivityData
	StageID string `json:"stage_id,omitempty"`
	Cursor  uint64 `json:"cursor,string"`
}

type ActivityPage struct {
	Entries  []ActivityEntry `json:"entries"`
	After    uint64          `json:"after,string"`
	Before   uint64          `json:"before,string"`
	HasMore  bool            `json:"has_more"`
	HasOlder bool            `json:"has_older"`
}

type ActivityQuery struct {
	Limit  int
	After  *uint64
	Before *uint64
}

type IncompleteWorkError struct {
	Reason string
	Marker *MarkerFailure
}

type Manifest struct {
	RunID          string         `json:"run_id"`
	StageID        string         `json:"stage_id"`
	Phase          string         `json:"phase"`
	VerificationID string         `json:"verification_id"`
	CreatedAt      time.Time      `json:"created_at"`
	Steps          []ManifestStep `json:"steps"`
}

type ManifestStep struct {
	Index   int    `json:"index"`
	Kind    string `json:"kind"`
	Command string `json:"command,omitempty"`
	Outcome string `json:"outcome"`
}

type MarkerFailure struct {
	Path    string
	Problem MarkerProblem
}

type MarkerProblem string

type MissingArtifactError struct{ Name string }

const DefaultActivityLimit = 100
const MarkerEmpty MarkerProblem = "empty"
const MarkerMissing MarkerProblem = "missing"
const MarkerUnreadable MarkerProblem = "unreadable"
const MaxActivityDetailBytes = 16384
const MaxActivityInvocationBytes = 128
const MaxActivityItemBytes = 256
const MaxActivityLimit = 200
const MaxActivityToolBytes = 256
const SummaryFile = "execution_summary.md"
const VerifyOutputLimit = 2000

func (e *IncompleteWorkError) Error() string { return "incomplete work: " + e.Reason }

func (e *MissingArtifactError) Error() string { return "missing artifact: " + e.Name }
