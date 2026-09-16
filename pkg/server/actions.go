package server

import (
	"context"

	"github.com/akopichin/afm/pkg/state"
)

// StageActions are the commands the dashboard can always trigger for any
// stage: approve/revise a plan, retry a failed stage, or pause/continue a
// stage, plus fire a predefined button (a canned Revise). Every production
// Config wires all of them (there is exactly one caller, cmd/afm/run.go) —
// this interface has no meaningful "partially nil"
// state, unlike SecondaryActions.
type StageActions interface {
	Approve(ctx context.Context, stageID string) error
	// Revise delivers feedback to a live agent. applied is false (with a nil
	// error) when the transition was a no-op (stage left the running/
	// awaiting_approval window, or the CAS was lost); seq is the transition's
	// sequence number, used as the unique id of the resulting agent_note feed
	// line. handleRevise emits the notice only when applied.
	Revise(ctx context.Context, stageID, feedback string) (applied bool, seq uint64, err error)
	Retry(ctx context.Context, stageID string) error
	Pause(ctx context.Context, stageID string) error
	Continue(ctx context.Context, stageID string) error
	Button(ctx context.Context, stageID, name string) error
}

// SecondaryActions are dashboard commands that are optional as a group: hook
// retry/skip (only meaningful for stages with before/after hooks) and
// file-based dialog answer/cancel notification (the critical write to
// answer.json already happened before NotifyAnswer/CancelDialog are
// consulted — these are best-effort FSM/restart notifications, not the
// source of truth). A nil SecondaryActions on Config makes retry-hook/
// skip-hook respond 501 and makes dialog answer/cancel a silent no-op notify —
// same behavior as today's four independently-nilable Config fields, now
// grouped under one nil check instead of four.
type SecondaryActions interface {
	RetryHook(stageID string) error
	SkipHook(stageID string) error
	NotifyAnswer(stageID, phase, qID, answer string, fromOptions bool) error
	CancelDialog(stageID string) error
}

// FlowActions are the flow-wide review-pause commands: hold every active
// stage for review (PauseFlow), collect review notes against the paused
// files (AddNote/UpdateNote/DeleteNote/ListNotes), then close the round by
// either injecting the collected notes into a target stage
// (InjectNotesAndResume) or discarding them (CancelNotesAndResume). The
// orchestrator implements this directly (pkg/orchestrator/reviewpause.go) —
// see cmd/afm/run.go for the wiring. The notes CRUD methods serialize with
// the review-pause lifecycle under the orchestrator's flowPauseMu and require
// the flow to be paused (ListNotes is the one exception — a lock-free read,
// safe to call anytime).
type FlowActions interface {
	PauseFlow(ctx context.Context) ([]string, error)
	InjectNotesAndResume(ctx context.Context, targetStageID string) error
	CancelNotesAndResume(ctx context.Context) error
	AddNote(root, path string, line *int, text, contentSHA string, expectedRev int) (state.ReviewNote, int, error)
	UpdateNote(id, text string, expectedRev int) (int, error)
	DeleteNote(id string, expectedRev int) (int, error)
	ListNotes() (state.ReviewNotes, error)
}
