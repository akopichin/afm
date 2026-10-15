package server

import (
	"context"

	"github.com/akopichin/afm/pkg/state"
)

// StageActions are the commands the dashboard can always trigger for any stage: approve/revise a plan,
// retry a failed stage, or pause/continue a stage, plus fire a predefined button.
type StageActions interface {
	Approve(ctx context.Context, stageID string) error
	// Revise delivers feedback to a live agent. handleRevise emits the notice only when applied.
	Revise(ctx context.Context, stageID, feedback string) (applied bool, seq uint64, err error)
	Retry(ctx context.Context, stageID string) error
	Pause(ctx context.Context, stageID string) error
	Continue(ctx context.Context, stageID string) error
	Button(ctx context.Context, stageID, name string) error
}

// SecondaryActions are dashboard commands that are optional as a group: hook retry/skip and file-based
// dialog answer/cancel notification.
type SecondaryActions interface {
	RetryHook(stageID string) error
	SkipHook(stageID string) error
	NotifyAnswer(stageID, phase, qID, answer string, fromOptions bool) error
	CancelDialog(stageID string) error
}

// AttachRequest is the HTTP body shape for POST /api/stages/{id}/assistants. The client supplies ONLY
// profile_id/title/instruction/mode/trigger/request_id — depth, parent task id, and
// command/args/env/cwd are NEVER taken from the request body (dev-plan §4.1): the server derives/validates
// all of that itself from the already-configured profile registry and the current policy.
type AttachRequest struct {
	RequestID   string
	ProfileID   string
	Title       string
	Instruction string
	Mode        string
	Trigger     string
}

// AssistantActions are the collaboration (helper-task) write commands the dashboard can trigger. A
// separate, narrow interface — NOT folded into StageActions, which would bloat every stage-control call
// site with a concern most stages never use.
type AssistantActions interface {
	Attach(ctx context.Context, stageID string, req AttachRequest) (string, error)
	// PolicyRevision returns the run's CURRENT effective collaboration depth-policy revision and max depth —
	// the draft-confirm path (T4.5) compares an action draft's own ExpectedRevision against this live value
	// BEFORE calling Attach, rejecting a stale draft instead of letting Attach's own internal CAS silently
	// succeed against whatever the policy happens to be now.
	PolicyRevision() (revision string, maxDepth int)
	PauseTask(ctx context.Context, taskID string) error
	ContinueTask(ctx context.Context, taskID string) error
	CancelTask(ctx context.Context, taskID string) error
	// WaiveTask (Этап 7, T7.6) records a human decision to accept the risk of taskID never closing its own
	// required gate. The actor is always the authenticated dashboard operator — stamped by the adapter, never
	// taken from the request body (a model's own task result can never waive itself, §19.5).
	WaiveTask(ctx context.Context, taskID string, reason string) error
}

// FlowActions controls review-pause and notes. Mutations serialize under flowPauseMu and require paused;
// ListNotes is lock-free.
type FlowActions interface {
	PauseFlow(ctx context.Context) ([]string, error)
	InjectNotesAndResume(ctx context.Context, targetStageID string) error
	CancelNotesAndResume(ctx context.Context) error
	AddNote(root, path string, line *int, text, contentSHA string, expectedRev int) (state.ReviewNote, int, error)
	UpdateNote(id, text string, expectedRev int) (int, error)
	DeleteNote(id string, expectedRev int) (int, error)
	ListNotes() (state.ReviewNotes, error)
}
