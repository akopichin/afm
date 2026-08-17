package server

import "context"

// StageActions are the commands the dashboard can always trigger for any
// stage: approve/revise a plan, retry a failed stage, or pause/continue a
// stage. Every production Config wires all five (there is exactly one
// caller, cmd/afm/run.go) — this interface has no meaningful "partially nil"
// state, unlike SecondaryActions.
type StageActions interface {
	Approve(ctx context.Context, stageID string) error
	Revise(ctx context.Context, stageID, feedback string) error
	Retry(ctx context.Context, stageID string) error
	Pause(ctx context.Context, stageID string) error
	Continue(ctx context.Context, stageID string) error
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
