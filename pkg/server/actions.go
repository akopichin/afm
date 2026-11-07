package server

import (
	"context"

	"github.com/akopichin/afm/contracts/state"
)

// StageActions are the stage and dialog commands of the dashboard. Each one checks the stage, performs the
// command and records its durable and live effects, so the handlers only decode the request and render the
// result; the implementation is engine.StageControl. Failures are *engine.Error.
type StageActions interface {
	Approve(ctx context.Context, stageID string) error
	// Revise delivers feedback to a live agent and records the note in the feed.
	Revise(ctx context.Context, stageID, feedback string) error
	// CheckRevise, CheckButton and CheckPreNote report why the stage cannot take the command. Handlers
	// call them when the body is unreadable, so a bad stage is reported before a bad body.
	CheckRevise(stageID string) error
	CheckButton(stageID string) error
	CheckPreNote(stageID string) error
	Retry(ctx context.Context, stageID string) error
	Pause(ctx context.Context, stageID string) error
	Continue(ctx context.Context, stageID string) error
	Button(ctx context.Context, stageID, name string) error
	// SetPreNote attaches a note to a pending stage; empty text removes it.
	SetPreNote(stageID, note string) error
	// SaveAttachment stores a pasted image (ext is ".png", ".jpg", ".webp" or ".gif") and returns its
	// absolute path.
	SaveAttachment(stageID, ext string, data []byte) (path string, err error)
	// Answer records the user's answer to the stage agent's question.
	Answer(stageID, phase, qID, answer string, fromOptions bool) error
	CancelDialog(stageID string) error
}

// SecondaryActions are dashboard commands that are optional as a group: lifecycle hook retry/skip.
type SecondaryActions interface {
	RetryHook(stageID string) error
	SkipHook(stageID string) error
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
