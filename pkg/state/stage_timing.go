package state

import "time"

// TimeKind says what a stage spends its time on; the zero value means the stage accumulates nothing now.
type TimeKind string

const (
	TimeNone       TimeKind = ""
	TimeWork       TimeKind = "work"
	TimeApproval   TimeKind = "approval"   // waits for the human: approval, replies, a helper verdict
	TimeHelpers    TimeKind = "helpers"    // waits for helpers and colleagues
	TimeBackoff    TimeKind = "backoff"    // retry backoff and usage-limit waits
	TimeScheduling TimeKind = "scheduling" // waits in the queue after the first start
	TimePaused     TimeKind = "paused"
)

// StageTiming is the stage's time ledger: closed intervals summed per kind plus the one open interval.
// It is derived from the journal (live and on replay alike); the open interval's live part is added by the reader.
type StageTiming struct {
	// Started is set at the stage's first start; nothing accumulates before it.
	Started bool `json:"started,omitempty"`
	// Incomplete marks a stage that started before the journal had run events: its anchors are unknown.
	Incomplete   bool  `json:"incomplete,omitempty"`
	WorkMs       int64 `json:"work_ms,omitempty"`
	ApprovalMs   int64 `json:"approval_ms,omitempty"`
	HelpersMs    int64 `json:"helpers_ms,omitempty"`
	BackoffMs    int64 `json:"backoff_ms,omitempty"`
	SchedulingMs int64 `json:"scheduling_ms,omitempty"`
	PausedMs     int64 `json:"paused_ms,omitempty"`
	// OpenKind/OpenSince describe the interval that is still running (TimeNone while the run is not alive).
	OpenKind  TimeKind  `json:"open_kind,omitempty"`
	OpenSince time.Time `json:"open_since,omitzero"`
}

// firstStartStatuses are the statuses whose first entry starts the stage's clock; waiting in the queue
// (ready), at a gate (paused) or after a failed dependency does not.
var firstStartStatuses = map[StageStatus]bool{
	StatusPlanning:         true,
	StatusAwaitingApproval: true,
	StatusRevising:         true,
	StatusRunning:          true,
}

// classifyStageTime says which bucket the stage's time goes to right now. It is the only place that maps a
// status to a kind, so a new status has to be placed here explicitly.
func classifyStageTime(rs *RunState, stageID string) TimeKind {
	st := rs.Stages[stageID]
	if rs.EndedAt != nil || !st.Timing.Started {
		return TimeNone
	}
	switch st.Status {
	case StatusPlanning, StatusRunning, StatusRevising:
		return TimeWork
	case StatusAwaitingApproval, StatusAwaitingUserInput, StatusHookFailed:
		return TimeApproval
	case StatusAwaitingPeer:
		return TimeHelpers
	case StatusAwaitingAssistants:
		if rs.Assistants.NeedsHumanDecision(stageID) {
			return TimeApproval
		}
		return TimeHelpers
	case StatusRetrying, StatusAwaitingQuota:
		return TimeBackoff
	case StatusPending, StatusReady:
		return TimeScheduling
	case StatusPaused:
		return TimePaused
	case StatusDone, StatusFailed:
		return TimeNone
	}
	return TimeNone
}

// syncStageTiming moves every stage whose class changed to the new one at time at: the interval that was open
// is closed into its kind and the new one is opened. Called after each journal record, live and on replay.
func (rs *RunState) syncStageTiming(at time.Time) {
	at = at.UTC().Round(0)
	for id, st := range rs.Stages {
		kind := classifyStageTime(rs, id)
		if kind == st.Timing.OpenKind {
			continue
		}
		st.Timing.closeAt(at)
		if kind != TimeNone {
			st.Timing.OpenKind, st.Timing.OpenSince = kind, at
		}
		rs.Stages[id] = st
	}
	rs.lastRecordAt = at
}

// closeStageTimingAt closes every open interval at the given time (the crash boundary of a lost process).
func (rs *RunState) closeStageTimingAt(at time.Time) {
	at = at.UTC().Round(0)
	for id, st := range rs.Stages {
		if st.Timing.OpenKind == TimeNone {
			continue
		}
		st.Timing.closeAt(at)
		rs.Stages[id] = st
	}
}

// markStageStarted starts the stage's clock on the first transition into a start status. A stage migrated from
// state.json already ran before the journal began, so its anchors are unknown whatever status it was found in.
func (rs *RunState) markStageStarted(t *Transition) {
	st := rs.Stages[t.StageID]
	if st.Timing.Started {
		return
	}
	migrated := t.Event == legacyLoadEvent
	if migrated {
		// A migrated pending or ready stage never started; anything else did.
		if t.To == StatusPending || t.To == StatusReady {
			return
		}
	} else if !firstStartStatuses[t.To] {
		return
	}
	st.Timing.Started = true
	st.Timing.Incomplete = migrated || rs.RunStartedAt.IsZero()
	rs.Stages[t.StageID] = st
}

// closeAt adds the open interval up to at (never negative) to its bucket and leaves no interval open.
func (tm *StageTiming) closeAt(at time.Time) {
	if tm.OpenKind == TimeNone {
		return
	}
	ms := max(at.Sub(tm.OpenSince).Milliseconds(), 0)
	switch tm.OpenKind {
	case TimeWork:
		tm.WorkMs += ms
	case TimeApproval:
		tm.ApprovalMs += ms
	case TimeHelpers:
		tm.HelpersMs += ms
	case TimeBackoff:
		tm.BackoffMs += ms
	case TimeScheduling:
		tm.SchedulingMs += ms
	case TimePaused:
		tm.PausedMs += ms
	default: // an unknown interval kind accumulates nowhere
	}
	tm.OpenKind, tm.OpenSince = TimeNone, time.Time{}
}
