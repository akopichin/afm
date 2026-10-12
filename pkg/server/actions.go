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
