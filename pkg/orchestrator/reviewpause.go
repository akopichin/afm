package orchestrator

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// ErrRunFinalizing means PauseFlow was called while a review-pause
// transaction is already being finalized (see the finalizing flag and the
// task that consumes it) — starting a second review round on top of one
// still winding down would race the same marker file.
var ErrRunFinalizing = errors.New("run is finalizing")

// newOperationID returns a fresh random hex id identifying one review-pause
// round, stored in PauseMarker.OperationID so a later finalize/resume call
// can confirm it's acting on the round it thinks it is.
func newOperationID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// computeResumeKind determines which of the runner variants (autonomous/
// planning/implementation) a paused stage should be resumed with. Preference
// order: (1) a stage hard-wired or already-activated onto the autonomous
// track always resumes autonomous; (2) runnerKindOf is authoritative — it
// records what was ACTUALLY spawned for this stage, which beats any static
// inference; (3) if runnerKind was never set (e.g. paused before the runner
// stamped it) fall back to inferring from the status the stage was paused
// from: Planning means the planning agent was live, anything else means the
// implementation agent was.
func (o *Orchestrator) computeResumeKind(s flow.Stage, pausedFrom state.StageStatus) string {
	if s.IsAuto() || isAutonomousStage(filepath.Join(o.opts.RunDir, s.ID)) {
		return kindAutonomous
	}
	if k := o.runnerKindOf(s.ID); k != "" {
		return k // authoritative: what was actually spawned
	}
	if pausedFrom == state.StatusPlanning {
		return kindPlanning
	}
	return kindImplementation
}

// PauseFlow puts the flow into best-effort review mode: it holds new stage
// activations (activationHeld) and gracefully pauses every currently-active,
// non-script agent stage via the existing per-stage Pause internals
// (EvPause + bumpPauseGen + interruptChans signal). Only stages whose EvPause
// CAS actually wins are recorded as owners in the durable pause marker —
// a stage that lost the race (finished/failed/already left the pausable
// From-set between the snapshot and the Trigger call) is simply skipped, not
// force-recorded, so the marker never claims ownership it doesn't hold.
//
// Idempotent: if a review-pause marker already exists, PauseFlow doesn't
// pause anything a second time — it just returns the existing owners.
func (o *Orchestrator) PauseFlow(ctx context.Context) ([]string, error) {
	o.flowPauseMu.Lock()
	defer o.flowPauseMu.Unlock()

	if o.finalizing.Load() {
		return nil, ErrRunFinalizing
	}
	if m := o.reviewMarker.Load(); m != nil {
		return ownerIDs(m), nil // already in review mode; idempotent
	}

	o.activationHeld.Store(true)

	snap := o.opts.Store.Snapshot()
	var owned []state.PauseOwner
	var ids []string
	for _, id := range snap.StageOrder {
		st := snap.Stages[id]
		stage := o.graph.Stage(id)
		if stage == nil || stage.IsScript() {
			continue // scripts have no interruptChans registration point (see Pause)
		}
		switch st.Status {
		case state.StatusRunning, state.StatusPlanning, state.StatusRevising, state.StatusRetrying:
		default:
			continue
		}
		if !o.concurrency.IsActive(id) {
			continue // queued/not-live: not owned, left to best-effort progress
		}

		kind := o.computeResumeKind(*stage, st.Status)
		fromRevising := st.Status == state.StatusRevising

		if _, ok := o.Trigger(id, bus.EvPause, bus.GuardCtx{}, "review pause"); !ok {
			continue // lost the CAS (stage left the From-set concurrently): not an owner
		}
		o.bumpPauseGen(id)
		if ch, ok := o.interruptChans.Load(id); ok {
			select {
			case ch.(chan struct{}) <- struct{}{}:
			default: // already signaled — don't block
			}
		}

		owned = append(owned, state.PauseOwner{ID: id, ResumeKind: kind, FromRevising: fromRevising})
		ids = append(ids, id)
	}

	m := state.PauseMarker{
		Version:     1,
		OperationID: newOperationID(),
		State:       state.PauseStatePaused,
		CreatedAt:   time.Now().UTC(),
		Owned:       owned,
	}
	if err := state.WriteNotesPauseMarker(o.opts.RunDir, m); err != nil {
		// Initial-marker failure: roll back the hold, but surface the IDs we
		// really did pause — those stages are now sitting paused with no
		// marker to remember it, and the caller needs to know to Continue
		// them manually rather than assume nothing happened.
		o.activationHeld.Store(false)
		return ids, fmt.Errorf("write pause marker (these stages are paused, Continue them manually): %w", err)
	}
	o.reviewMarker.Store(&m)
	return ids, nil
}

// ReviewState reports the current flow-wide review-pause state for
// /api/status (see pkg/server's Config.ReviewState wiring): "none" when no
// review-pause round is active, "resuming" once InjectNotesAndResume/
// CancelNotesAndResume has flipped the marker but the async resume hasn't
// cleared it yet (no per-stage owners are meaningful at that point — they're
// all mid-resume), or "paused" with the subset of owned stages that are
// CURRENTLY sitting in StatusPaused (an owner may have already progressed
// past paused if a resume is racing this read, so this recomputes from live
// FSM status rather than trusting the marker's static Owned list wholesale).
// Lock-free: reviewMarker is an atomic.Pointer, and currentStatus reads the
// store directly — safe to call from the HTTP handler goroutine.
func (o *Orchestrator) ReviewState() (string, []string) {
	m := o.reviewMarker.Load()
	if m == nil {
		return "none", nil
	}
	if m.State == state.PauseStateResuming {
		return "resuming", nil
	}
	var paused []string
	for _, ow := range m.Owned {
		if o.currentStatus(ow.ID) == state.StatusPaused {
			paused = append(paused, ow.ID)
		}
	}
	return "paused", paused
}

// ownerIDs extracts just the stage ids from a pause marker's owner list.
func ownerIDs(m *state.PauseMarker) []string {
	ids := make([]string, 0, len(m.Owned))
	for _, ow := range m.Owned {
		ids = append(ids, ow.ID)
	}
	return ids
}

// renderReviewFeedback renders collected review notes into feedback-markdown
// text that gets injected into a stage. It groups notes by display_path
// (deterministic order), sorts line notes ascending by line number (file-level
// notes last), and for each note includes the JSON-quoted original line text.
// A content_sha mismatch adds "(⚠ content differed at injection)"; an
// unavailable file (current() returns ok==false) adds "(⚠ file unavailable at
// injection)" on the file header.
func renderReviewFeedback(notes []state.ReviewNote, current func(root, path string) (string, bool)) string {
	// Group by display_path, deterministic order.
	byFile := map[string][]state.ReviewNote{}
	var order []string
	for _, n := range notes {
		if _, seen := byFile[n.DisplayPath]; !seen {
			order = append(order, n.DisplayPath)
		}
		byFile[n.DisplayPath] = append(byFile[n.DisplayPath], n)
	}
	slices.Sort(order)
	var b strings.Builder
	b.WriteString("## Review notes\n")
	for _, dp := range order {
		group := byFile[dp]
		slices.SortStableFunc(group, func(i, j state.ReviewNote) int { // line notes by line asc, file-level last
			li, lj := i.Line, j.Line
			if li == nil && lj == nil {
				return 0 // both file-level, equal
			}
			if li == nil {
				return 1 // i is file-level, goes last
			}
			if lj == nil {
				return -1 // j is file-level, goes last
			}
			return cmp.Compare(*li, *lj)
		})
		curSHA, ok := current(group[0].Root, group[0].Path)
		header := fmt.Sprintf("### %s  %s", group[0].Reference, jsonQuote(dp))
		if !ok {
			header += "  (⚠ file unavailable at injection)"
		}
		b.WriteString(header + "\n")
		for _, n := range group {
			if n.Line == nil {
				fmt.Fprintf(&b, "- File: %s\n", n.Text)
				continue
			}
			drift := ""
			if ok && curSHA != n.ContentSHA {
				drift = " (⚠ content differed at injection)"
			}
			origTxt := ""
			if n.OrigLineText != nil {
				origTxt = *n.OrigLineText
			}
			fmt.Fprintf(&b, "- Line %d%s; original: %s: %s\n",
				*n.Line, drift, jsonQuote(origTxt), n.Text)
		}
	}
	return b.String()
}

// jsonQuote returns a JSON-quoted string (the string literal as it would appear
// in JSON output).
func jsonQuote(s string) string {
	q, _ := json.Marshal(s)
	return string(q)
}

// withFeedbackRunner maps a resume_kind (see computeResumeKind/kindPlanning
// etc.) to the *WithFeedback variant of the matching runner — used by
// resumeOwner when the resumed stage must see a feedback.md (it's the review
// target, or it was paused mid-revising: ow.FromRevising).
func (o *Orchestrator) withFeedbackRunner(kind string) func(context.Context, flow.Stage) {
	switch kind {
	case kindPlanning:
		return o.runPlanningWithFeedback
	case kindReview:
		return o.runReviewWithFeedback
	case kindAutonomous:
		return o.runAutonomousWithFeedback
	default:
		return o.runImplementationWithFeedback
	}
}

// plainRunner maps a resume_kind to the plain (fresh, no feedback.md) variant
// of the matching runner — used by resumeOwner for a non-target owner that
// wasn't paused mid-revising.
func (o *Orchestrator) plainRunner(kind string) func(context.Context, flow.Stage) {
	switch kind {
	case kindPlanning:
		return o.runPlanningAgent
	case kindReview:
		return o.runReviewAgent
	case kindAutonomous:
		return o.runAutonomousAgent
	default:
		return o.runImplementationAgent
	}
}

// resumeOwner resumes a single owned, paused stage (ow) after finalize
// decided review notes should be injected (or discarded). It first waits for
// the OLD callback (the one interrupted by PauseFlow's Pause) to drain —
// concurrency.IsActive(ow.ID) — so that when the interrupted RunAgent call
// returns with ErrUserInterrupted, it still observes status==paused and
// treats it as a genuine Pause (no re-spawn), rather than racing the resume's
// own transition/spawn below. Only after the drain does it transition the
// stage out of paused and spawn the correct runner for ow.ResumeKind:
//   - isTarget (the stage the review notes are FOR) always gets feedback
//     (EvRevise -> Revising, withFeedbackRunner) so the notes reach it.
//   - a non-target owner gets feedback too if it was paused mid-revising
//     (ow.FromRevising) — it already had its own feedback.md in flight,
//     unrelated to review notes, and must resume the same way it would have
//     without PauseFlow ever intervening.
//   - every other non-target owner resumes plain (EvContinue back to
//     PausedFrom, plainRunner) and gets armResumeContext so its non-
//     interactive agent replays "previously completed actions" from attempt 0
//     instead of starting over (see the resumeContextOnce field comment).
func (o *Orchestrator) resumeOwner(ctx context.Context, ow state.PauseOwner, isTarget bool) {
	for o.concurrency.IsActive(ow.ID) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
	}

	stage := o.graph.Stage(ow.ID)
	if stage == nil {
		return
	}
	useFeedback := isTarget || ow.FromRevising

	if o.testRunnerHook != nil {
		o.testRunnerHook(ow.ResumeKind, useFeedback)
		return
	}

	if !isTarget {
		o.armResumeContext(ow.ID) // A′: attempt-0 replay for non-interactive
	}

	if useFeedback {
		if _, ok := o.Trigger(ow.ID, bus.EvRevise, bus.GuardCtx{}, "review resume"); !ok {
			return
		}
		o.concurrency.SpawnAgent(ctx, *stage, o.withFeedbackRunner(ow.ResumeKind))
		return
	}

	pausedFrom := o.opts.Store.PausedFrom(ow.ID)
	if _, ok := o.Trigger(ow.ID, bus.EvContinue, bus.GuardCtx{PausedFrom: pausedFrom}, "review resume"); !ok {
		return
	}
	o.concurrency.SpawnAgent(ctx, *stage, o.plainRunner(ow.ResumeKind))
}

// currentFileSHA resolves the current content_sha of a file (root/path — the
// same pair stored on a state.ReviewNote), used by renderReviewFeedback to
// detect drift between when a note was taken and when it's injected. Backed
// by Options.CurrentFileSHA, wired in at construction (the server plugs in a
// workspace-backed resolver in a later task) — nil (the default, e.g. host
// runs with no workspace) means "can't tell", and every note renders with
// "(⚠ file unavailable at injection)" rather than a false drift/no-drift
// verdict.
func (o *Orchestrator) currentFileSHA(root, path string) (string, bool) {
	if o.opts.CurrentFileSHA == nil {
		return "", false
	}
	return o.opts.CurrentFileSHA(root, path)
}

// Sentinels for InjectNotesAndResume/CancelNotesAndResume: the durable
// resume transaction that closes out a review-pause round (PauseFlow,
// reviewpause.go above) opened by an operator either injecting collected
// review notes into a target stage or discarding them outright.
var (
	ErrNoReviewPause   = errors.New("no review pause active")
	ErrNoNotes         = errors.New("no notes to inject")
	ErrTargetNotPaused = errors.New("target is not paused")
	ErrNotOwned        = errors.New("stage is not owned by the review pause")
)

// ownsStage reports whether id is one of the stages a pause marker claims
// ownership of (see PauseFlow: only stages whose EvPause CAS actually won
// are recorded as owners).
func ownsStage(m *state.PauseMarker, id string) bool {
	for _, ow := range m.Owned {
		if ow.ID == id {
			return true
		}
	}
	return false
}

// InjectNotesAndResume closes a review-pause round by delivering the
// collected review notes to targetStageID and resuming every owned stage.
// The durable part — flipping the marker to State=resuming/Mode=inject and
// writing feedback.md — happens synchronously, under flowPauseMu, before this
// call returns: a crash right after doesn't lose the operator's intent (a
// future recovery pass has enough on disk to know injection was decided and
// which stage it targets), it only leaves the actual resume unfinished. The
// resume itself (waiting out in-flight agents, transitioning FSM, spawning
// runners, then deleting notes+marker) runs asynchronously under the run's
// own context (runContext) via runResumeTransaction — reqCtx is an HTTP
// request context that dies the moment the handler returns, and the resume
// of several stages plus their drain-waits can easily outlive that.
func (o *Orchestrator) InjectNotesAndResume(reqCtx context.Context, targetStageID string) error {
	o.flowPauseMu.Lock()
	m := o.reviewMarker.Load()
	if m == nil {
		o.flowPauseMu.Unlock()
		return ErrNoReviewPause
	}
	if !ownsStage(m, targetStageID) {
		o.flowPauseMu.Unlock()
		return ErrNotOwned
	}
	if o.currentStatus(targetStageID) != state.StatusPaused {
		o.flowPauseMu.Unlock()
		return ErrTargetNotPaused
	}
	notes, err := state.LoadReviewNotes(o.opts.RunDir)
	if err != nil {
		o.flowPauseMu.Unlock()
		return err
	}
	if len(notes.Notes) == 0 {
		o.flowPauseMu.Unlock()
		return ErrNoNotes
	}

	updated := *m
	updated.State = state.PauseStateResuming
	updated.Mode = state.PauseModeInject
	updated.TargetStage = targetStageID
	if err := state.WriteNotesPauseMarker(o.opts.RunDir, updated); err != nil {
		o.flowPauseMu.Unlock()
		return err
	}
	o.reviewMarker.Store(&updated)

	rendered := renderReviewFeedback(notes.Notes, o.currentFileSHA)
	targetDir := filepath.Join(o.opts.RunDir, targetStageID)
	if _, err := state.SaveFeedbackOnce(targetDir, updated.OperationID, rendered); err != nil {
		o.flowPauseMu.Unlock()
		return err
	}
	o.activationHeld.Store(false)
	o.flowPauseMu.Unlock()

	o.concurrency.SpawnDetached(o.runContext(reqCtx), func(ctx context.Context) {
		o.runResumeTransaction(ctx, updated)
	})
	return nil
}

// CancelNotesAndResume closes a review-pause round by discarding the
// collected review notes: every owned stage resumes plain (no target, no
// feedback.md), the same async transaction as InjectNotesAndResume minus the
// target/feedback step. See InjectNotesAndResume's comment for why the
// durable marker flip is synchronous but the actual resume isn't.
func (o *Orchestrator) CancelNotesAndResume(reqCtx context.Context) error {
	o.flowPauseMu.Lock()
	m := o.reviewMarker.Load()
	if m == nil {
		o.flowPauseMu.Unlock()
		return ErrNoReviewPause
	}

	updated := *m
	updated.State = state.PauseStateResuming
	updated.Mode = state.PauseModeCancel
	if err := state.WriteNotesPauseMarker(o.opts.RunDir, updated); err != nil {
		o.flowPauseMu.Unlock()
		return err
	}
	o.reviewMarker.Store(&updated)
	o.activationHeld.Store(false)
	o.flowPauseMu.Unlock()

	o.concurrency.SpawnDetached(o.runContext(reqCtx), func(ctx context.Context) {
		o.runResumeTransaction(ctx, updated)
	})
	return nil
}

// runResumeTransaction is the async second half of InjectNotesAndResume/
// CancelNotesAndResume: resume every owner (the review target gets feedback,
// see resumeOwner), nudge the scheduler in case activationHeld was gating
// pending stages, and only then delete the review notes and the pause marker
// — last, because as long as the marker is on disk a crash mid-transaction
// is recoverable (the notes/marker are still there to retry from), whereas
// deleting them first and crashing before resuming every owner would strand
// paused stages with no marker left to explain why.
func (o *Orchestrator) runResumeTransaction(ctx context.Context, m state.PauseMarker) {
	for _, ow := range m.Owned {
		isTarget := m.Mode == state.PauseModeInject && ow.ID == m.TargetStage
		o.resumeOwner(ctx, ow, isTarget)
	}
	o.startReadyStages(ctx) // re-drive activation-held pending stages
	_ = state.DeleteReviewNotes(o.opts.RunDir)
	_ = state.ClearNotesPauseMarker(o.opts.RunDir)
	o.reviewMarker.Store(nil)
}
