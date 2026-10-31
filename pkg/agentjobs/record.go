// Package agentjobs is the durable journal of AI subprocess boundaries (agent-jobs.jsonl).
// It stores who ran and when, never tokens, money, argv, environment or message text.
package agentjobs

import (
	"errors"
	"fmt"
	"time"
	"unicode/utf8"
)

const (
	// FileName is the journal's fixed name directly under a run (or side-agent) directory.
	FileName = "agent-jobs.jsonl"
	// RecordVersion is the only journal format this package reads and writes.
	RecordVersion = 1

	// MaxTitleLen bounds Title in runes; callers keep message text out of it.
	MaxTitleLen = 120
	// MaxReasonLen bounds a start_failed Reason in bytes.
	MaxReasonLen = 64
)

// RecordType is the "type" tag of a journal line.
type RecordType string

const (
	TypeHeader      RecordType = "header"
	TypeStarted     RecordType = "started"
	TypeFinished    RecordType = "finished"
	TypeStartFailed RecordType = "start_failed"
)

// Outcome says how a job ended.
type Outcome string

const (
	OutcomeOK          Outcome = "ok"
	OutcomeError       Outcome = "error"
	OutcomeInterrupted Outcome = "interrupted"
	OutcomeIdleTimeout Outcome = "idle_timeout"
	OutcomeUsageLimit  Outcome = "usage_limit"
	OutcomeCancelled   Outcome = "cancelled"
	OutcomeYielded     Outcome = "yielded"
)

var validOutcomes = map[Outcome]bool{
	OutcomeOK: true, OutcomeError: true, OutcomeInterrupted: true, OutcomeIdleTimeout: true,
	OutcomeUsageLimit: true, OutcomeCancelled: true, OutcomeYielded: true,
}

var (
	// ErrInvalidRecord means the record is malformed; the store is left untouched.
	ErrInvalidRecord = errors.New("agentjobs: invalid record")
	// ErrConflictingRecord means a different fact is already stored for that job.
	ErrConflictingRecord = errors.New("agentjobs: conflicting record")
	// ErrClosed is returned by Append after Close.
	ErrClosed = errors.New("agentjobs: store closed")
)

// Header is the first journal line: it marks when recording began for this file.
// It is written once at creation and never rewritten.
type Header struct {
	Type               RecordType `json:"type"`
	RecordVersion      int        `json:"record_version"`
	RecordingStartedAt time.Time  `json:"recording_started_at"`
	// BaseOffsetMS is the active-timeline offset of the run when the file was created.
	BaseOffsetMS int64 `json:"base_offset_ms"`
}

// JobMeta is the immutable identity of one job, assigned before the process starts.
// Every field is a label or an ID; the caller must not put argv, env or user text in them.
type JobMeta struct {
	JobID        string `json:"job_id"`
	RunID        string `json:"run_id,omitempty"`
	AgentID      string `json:"agent_id,omitempty"`
	AgentKind    string `json:"agent_kind,omitempty"`
	StageID      string `json:"stage_id,omitempty"`
	RootStageID  string `json:"root_stage_id,omitempty"`
	TaskID       string `json:"task_id,omitempty"`
	ParentTaskID string `json:"parent_task_id,omitempty"`
	ProfileID    string `json:"profile_id,omitempty"`
	Phase        string `json:"phase,omitempty"`
	VerifyStep   int    `json:"verify_step,omitempty"`
	Turn         int    `json:"turn,omitempty"`
	Attempt      int    `json:"attempt,omitempty"`
	// SessionID and PrevJobID link a continuation to the CLI session and the job it resumed.
	SessionID string `json:"session_id,omitempty"`
	PrevJobID string `json:"prev_job_id,omitempty"`
	Title     string `json:"title,omitempty"`
	// Channel and Model are the configured hint; the observed model lives in usage.jsonl.
	Channel string `json:"channel,omitempty"`
	Model   string `json:"model,omitempty"`
}

// Segment places a job on the run's active timeline. It is absent when the run state
// had no timeline to offer (for example an Assistant job after the flow ended).
type Segment struct {
	RunStartedAt time.Time `json:"run_started_at"`
	// BaseMS is the accumulated active time of earlier process segments.
	BaseMS int64 `json:"elapsed_base_ms"`
	// OffsetMS is the job's start on the active timeline.
	OffsetMS int64 `json:"run_offset_ms"`
}

// IsZero lets encoding/json omit an absent segment.
func (s Segment) IsZero() bool { return s.RunStartedAt.IsZero() }

// Record is a started, finished or start_failed line. Finished lines carry only the JobID
// of JobMeta; readers join them to the started line.
type Record struct {
	Type RecordType `json:"type"`
	JobMeta
	// At is the start instant (started), end instant (finished) or attempt instant (start_failed).
	At time.Time `json:"at"`

	Segment Segment `json:"segment,omitzero"` // started

	Outcome    Outcome `json:"outcome,omitempty"`     // finished
	DurationMS int64   `json:"duration_ms,omitempty"` // finished, measured on a monotonic clock

	// Reason is a short machine code (not error text) for start_failed.
	Reason string `json:"reason,omitempty"`
}

// normalized returns the record in its stored form: UTC times and bounded text.
func (r Record) normalized() Record {
	r.At = r.At.UTC()
	r.Segment.RunStartedAt = r.Segment.RunStartedAt.UTC()
	r.Title = clipRunes(r.Title, MaxTitleLen)
	r.Reason = clipBytes(r.Reason, MaxReasonLen)
	return r
}

func (r Record) validate() error {
	if r.JobID == "" {
		return fmt.Errorf("%w: empty job_id", ErrInvalidRecord)
	}
	if r.At.IsZero() {
		return fmt.Errorf("%w: zero time", ErrInvalidRecord)
	}
	switch r.Type {
	case TypeStarted:
	case TypeFinished:
		if !validOutcomes[r.Outcome] {
			return fmt.Errorf("%w: unknown outcome %q", ErrInvalidRecord, r.Outcome)
		}
		if r.DurationMS < 0 {
			return fmt.Errorf("%w: negative duration", ErrInvalidRecord)
		}
	case TypeStartFailed:
		if r.Reason == "" {
			return fmt.Errorf("%w: start_failed without reason", ErrInvalidRecord)
		}
	default:
		return fmt.Errorf("%w: unknown type %q", ErrInvalidRecord, r.Type)
	}
	return nil
}

func clipRunes(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit])
}

// clipBytes cuts on a rune boundary so the result stays valid UTF-8.
func clipBytes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
