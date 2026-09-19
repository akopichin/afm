package orchestrator

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/concurrency"
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

// spawnKind stamps the runner kind SYNCHRONOUSLY (before SpawnAgent's goroutine
// can markActive) and then spawns the runner. Each runner also sets its own kind
// as its first line, but that runs INSIDE the goroutine, only after markActive —
// leaving a window in which a concurrent PauseFlow observes the stage as active
// while runnerKind is still empty or STALE from the previous phase. Two concrete
// misclassifications that window causes, both fixed by stamping here:
//   - planning → implementation: EvPlanReady does not clear runnerKind, so the
//     implementation spawn briefly still reads kindPlanning and a pause in that
//     window would resume the stage via the planning runner.
//   - a restart-resumed Revising stage: the in-memory registry is empty, and
//     computeResumeKind's status fallback maps Revising to implementation even
//     when the live runner is planning/review.
//
// Stamping the kind before the active marker is ever published closes the window
// for every kind, not just review. The runner's own first-line setRunnerKind
// stays as a harmless idempotent backstop for any direct (non-SpawnAgent) path.
//
// SpawnAgentLease (не SpawnAgent) — единственная точка запуска исполнительских
// раннеров (AI-verify, V4b): callback сохраняет выданный *concurrency.Lease в
// o.stageLeases на время работы run(ctx,s) и убирает его в defer. RunVerification
// достаёт lease отсюда, чтобы временно перевести командный слот на верификатора
// (см. поле stageLeases). Планировочные раннеры тоже проходят через spawnKind и
// тоже получают lease, но никогда им не пользуются — RunVerification вызывают
// только исполнительские раннеры (agents.go).
func (o *Orchestrator) spawnKind(ctx context.Context, s flow.Stage, kind string, run func(context.Context, flow.Stage)) {
	o.setRunnerKind(s.ID, kind)
	o.concurrency.SpawnAgentLease(ctx, s, func(ctx context.Context, s flow.Stage, lease *concurrency.Lease) {
		o.stageLeases.Store(s.ID, lease)
		defer o.stageLeases.Delete(s.ID)
		run(ctx, s)
	})
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

		// Clear any stale "already resumed" mark left by a PRIOR review round
		// on this same stage — this round re-pauses it, so a leftover entry
		// would make its resume (runResumeTransaction) wrongly skip re-spawning.
		o.reviewResumed.Delete(id)

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
		return state.PauseStateResuming, nil
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
	out := b.String()
	// Cap the aggregate so a large collection can't produce an unbounded agent
	// prompt (per-note length is already bounded in AddNote/UpdateNote; this
	// guards the total). Truncate on a line boundary and mark the cut.
	if len(out) > maxRenderedFeedbackBytes {
		cut := out[:maxRenderedFeedbackBytes]
		if nl := strings.LastIndexByte(cut, '\n'); nl >= 0 {
			cut = cut[:nl+1]
		}
		out = cut + "… (review notes truncated)\n"
	}
	return out
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
//
// Returns whether the owner was handled: true once it has been resumed (or
// there was genuinely nothing to spawn), false only when the drain-wait was
// cut short by ctx cancellation or a resume transition lost its CAS — cases a
// later retry must attempt again. runResumeTransaction records an owner in
// reviewResumed ONLY on a true return, so a spawn that never happened is never
// mistaken for one already done.
func (o *Orchestrator) resumeOwner(ctx context.Context, ow state.PauseOwner, isTarget bool) bool {
	for o.concurrency.IsActive(ow.ID) {
		select {
		case <-ctx.Done():
			return false // drain interrupted: a later retry must resume this owner
		case <-time.After(50 * time.Millisecond):
		}
	}

	stage := o.graph.Stage(ow.ID)
	if stage == nil {
		return true // nothing to resume; don't retry
	}
	useFeedback := isTarget || ow.FromRevising

	if o.testRunnerHook != nil {
		o.testRunnerHook(ow.ResumeKind, useFeedback)
		return true
	}

	if !isTarget {
		o.armResumeContext(ow.ID) // A′: attempt-0 replay for non-interactive
	}

	runner := o.plainRunner(ow.ResumeKind)
	if useFeedback {
		runner = o.withFeedbackRunner(ow.ResumeKind)
	}

	// Dispatch on the owner's CURRENT status, not on the assumption that it's
	// still paused. Three situations reach here:
	//
	//   - paused (the live PauseFlow→resume path, and a crash that landed before
	//     any resume transition): drive the FSM out of paused first, then spawn.
	//   - running/planning/revising/retrying (a crash AFTER a prior resume
	//     attempt's FSM transition committed but BEFORE its SpawnAgent ran,
	//     replayed by recoverReviewPause): the transition already happened and no
	//     live process exists — just re-spawn the saved runner kind, WITHOUT a
	//     second transition (which would CAS-fail from a non-paused status and
	//     strand the owner in an active status with no agent behind it forever).
	//     retrying is included specifically: a stage paused FROM retrying resumes
	//     via EvContinue back INTO retrying (its PausedFrom), so a crash in that
	//     window leaves a passive `retrying` status with no backoff timer and no
	//     goroutine — the exact "hangs forever" case the earlier three-status
	//     matrix missed.
	//   - anything else (done/failed — recovered completion, or already resumed):
	//     nothing to do.
	switch o.currentStatus(ow.ID) {
	case state.StatusPaused:
		if useFeedback {
			if _, ok := o.Trigger(ow.ID, bus.EvRevise, bus.GuardCtx{}, "review resume"); !ok {
				return false
			}
		} else {
			pausedFrom := o.opts.Store.PausedFrom(ow.ID)
			if _, ok := o.Trigger(ow.ID, bus.EvContinue, bus.GuardCtx{PausedFrom: pausedFrom}, "review resume"); !ok {
				return false
			}
		}
		o.concurrency.SpawnAgent(ctx, *stage, runner)
		return true
	case state.StatusRunning, state.StatusPlanning, state.StatusRevising, state.StatusRetrying:
		o.concurrency.SpawnAgent(ctx, *stage, runner)
		return true
	default:
		return true // terminal or already-resumed: nothing to spawn
	}
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
	ErrNoReviewPause    = errors.New("no review pause active")
	ErrNoNotes          = errors.New("no notes to inject")
	ErrTargetNotPaused  = errors.New("target is not paused")
	ErrNotOwned         = errors.New("stage is not owned by the review pause")
	ErrResumeInProgress = errors.New("review resume already in progress")
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
	// Only a round still in `paused` may be closed. Once it has flipped to
	// `resuming`, exactly one resume worker is already running for this
	// operation id; a repeat Inject/Cancel must NOT launch a second worker (which
	// would see owners already out of paused and re-spawn them). Guarded under
	// flowPauseMu, atomic with the flip below and with PauseFlow.
	if m.State != state.PauseStatePaused {
		o.flowPauseMu.Unlock()
		return ErrResumeInProgress
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
		if err := o.runResumeTransaction(ctx, updated); err != nil {
			log.Printf("review-pause: resume transaction incomplete for op %s (marker retained, fail-closed): %v", updated.OperationID, err)
		}
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
	// See InjectNotesAndResume: reject a repeat Cancel once the round is already
	// resuming — the second worker would re-spawn owners that are no longer
	// paused. Cancel is the easiest way to hit this (no target-status check to
	// otherwise catch it).
	if m.State != state.PauseStatePaused {
		o.flowPauseMu.Unlock()
		return ErrResumeInProgress
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
		if err := o.runResumeTransaction(ctx, updated); err != nil {
			log.Printf("review-pause: resume transaction incomplete for op %s (marker retained, fail-closed): %v", updated.OperationID, err)
		}
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
func (o *Orchestrator) runResumeTransaction(ctx context.Context, m state.PauseMarker) error {
	// Re-deliver the review feedback idempotently BEFORE resuming any owner.
	// InjectNotesAndResume also writes it synchronously, but recoverReviewPause
	// (a crash between that write and here) does NOT — without this, a restart
	// would resume the target via a *WithFeedback runner whose feedback.md never
	// received the review block, silently dropping the operator's notes.
	// SaveFeedbackOnce is keyed on the operation id, so the live path's earlier
	// write makes this a no-op there.
	if m.Mode == state.PauseModeInject && m.TargetStage != "" {
		notes, err := state.LoadReviewNotes(o.opts.RunDir)
		if err != nil {
			return err // keep notes+marker; a later start retries the whole txn
		}
		if len(notes.Notes) > 0 {
			rendered := renderReviewFeedback(notes.Notes, o.currentFileSHA)
			targetDir := filepath.Join(o.opts.RunDir, m.TargetStage)
			if _, err := state.SaveFeedbackOnce(targetDir, m.OperationID, rendered); err != nil {
				return err
			}
		}
	}

	for _, ow := range m.Owned {
		// Skip an owner a PRIOR invocation of this same transaction already
		// resumed: an in-process retry (e.g. after a durable-cleanup failure)
		// must repeat ONLY the cleanup, never re-spawn a still-live owner — two
		// agents mutating one stage's files is exactly the corruption the resume
		// lifecycle must never cause. reviewResumed also makes Run()'s bootstrap
		// skip these owners (recovery.go). Marked only AFTER resumeOwner reports
		// it actually handled the owner, so a ctx-cancel mid-drain (return false)
		// leaves the owner unmarked for the next attempt rather than stranding it.
		if _, done := o.reviewResumed.Load(ow.ID); done {
			continue
		}
		isTarget := m.Mode == state.PauseModeInject && ow.ID == m.TargetStage
		if o.resumeOwner(ctx, ow, isTarget) {
			o.reviewResumed.Store(ow.ID, struct{}{})
		}
	}
	// If the run context was cancelled mid-loop (shutdown during resume), the
	// remaining resumeOwner calls returned early — leave notes+marker on disk
	// so a later start can finish the resume, instead of discarding the intent
	// for owners we never got to.
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// Full scheduling cascade, not just startReadyStages: while activationHeld
	// was set, unblocked dependents were parked in `pending` by
	// tryActivatePrePlanned / startPlanningForUnblocked (which startReadyStages
	// alone never re-drives — it only promotes stages already in `ready`). A
	// script stage that finished DURING the review is the canonical case: it's
	// not an owner (PauseFlow skips scripts), so nothing else re-evaluates the
	// planning/pre-planned stage it unblocked. Run the same cascade normal agent
	// completion does, so those dependents actually start once the hold lifts.
	o.failBlockedStages()
	o.startPlanningForUnblocked(ctx)
	o.startReadyStages(ctx)
	o.tryActivatePrePlanned(ctx)

	// Durable cleanup is part of the transaction result: only once notes AND
	// marker are gone from disk may the in-memory marker be dropped. If either
	// delete fails we keep reviewTxnActive() true (fail-closed): the run won't
	// finalize, external controls stay rejected, and a restart re-runs this
	// (now no-op) transaction to finish cleanup — far safer than declaring
	// review done while a durable `resuming` marker or stale notes still linger.
	if err := o.clearReviewArtifacts(); err != nil {
		return err
	}
	o.reviewMarker.Store(nil)
	// NB: reviewResumed entries are deliberately NOT cleared here. Run()'s
	// bootstrap (startPlanningForPending) runs immediately AFTER recovery and
	// relies on them to skip owners recovery just resumed; clearing them now
	// would re-expose those owners to a double spawn. A LATER review round
	// re-pauses its owners via PauseFlow, which clears any stale entry there.
	// Nudge Run()'s select loop: a cancel round with no owners (or one whose
	// owners all recovered already-complete) spawns no agent and thus produces
	// no EventAgentCompleted — without this, the loop could sit blocked forever,
	// never re-checking shouldExit() now that the marker is cleared.
	o.concurrency.WakeEventLoop()
	return nil
}

// clearReviewArtifacts removes the durable review-notes and pause-marker files
// (notes first, so a partial failure never leaves stale notes with no marker to
// gate a fresh round). Overridable in tests via testClearReviewArtifacts to
// exercise the fail-closed cleanup path.
func (o *Orchestrator) clearReviewArtifacts() error {
	if o.testClearReviewArtifacts != nil {
		return o.testClearReviewArtifacts()
	}
	if err := state.DeleteReviewNotes(o.opts.RunDir); err != nil {
		return err
	}
	return state.ClearNotesPauseMarker(o.opts.RunDir)
}

// Sentinels for the notes CRUD methods below (AddNote/UpdateNote/DeleteNote).
// ErrRevConflict/ErrStaleContent/ErrStaleLine/ErrEmptyText map to 409/400 in
// pkg/server's flowErrCode; ErrNoteNotFound is the natural extension for
// Update/Delete against an id that isn't in the store (not requested by the
// original task list explicitly, but Update/Delete need SOME answer for a
// stale/typoed id, and silently no-op'ing would be worse than a clear 404).
var (
	ErrRevConflict  = errors.New("notes revision conflict")
	ErrStaleContent = errors.New("file content changed since the note was captured")
	ErrStaleLine    = errors.New("line is out of range for the current file")
	ErrEmptyText    = errors.New("note text is empty")
	ErrNoteNotFound = errors.New("note not found")
	ErrNoteTooLong  = errors.New("note text exceeds the maximum length")
	ErrTooManyNotes = errors.New("too many review notes")
)

// Resource limits for the review-notes store. Even a local single-user
// dashboard must bound these: one oversized note (or thousands of notes) turns
// into a large review-notes.json, several in-memory copies during the atomic
// rewrite, and — worst — an unbounded agent prompt when the notes are rendered
// into feedback.md. Enforced in AddNote/UpdateNote (per note + count) and
// renderReviewFeedback (the rendered aggregate).
const (
	// maxNoteTextBytes caps a single note's text.
	maxNoteTextBytes = 8 << 10 // 8 KiB
	// maxNotes caps how many notes one review round may hold.
	maxNotes = 500
	// maxRenderedFeedbackBytes caps the feedback block injected into the agent
	// so a large collection can't produce an unbounded prompt.
	maxRenderedFeedbackBytes = 256 << 10 // 256 KiB
)

// resolveFile is the nil-safe wrapper around Options.ResolveFile, mirroring
// the currentFileSHA pattern above: nil (host runs, no file browser wired)
// means "can't resolve anything", not a panic.
func (o *Orchestrator) resolveFile(root, path string, line *int) (ResolvedFile, bool) {
	if o.opts.ResolveFile == nil {
		return ResolvedFile{}, false
	}
	return o.opts.ResolveFile(root, path, line)
}

// requireReviewPaused is the shared precondition for every notes CRUD method:
// a review-pause round must be active AND currently sitting in the paused
// state (not mid-resume) — the same fact ReviewState() reports, checked
// directly against the marker rather than going through the string-based
// ReviewState() API.
func (o *Orchestrator) requireReviewPaused() error {
	m := o.reviewMarker.Load()
	if m == nil || m.State != state.PauseStatePaused {
		return ErrNoReviewPause
	}
	return nil
}

// sameLine reports whether two line pointers denote the same review-note key:
// both nil (a file-level note) or both non-nil with an equal value.
func sameLine(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// AddNote adds (or, for the same (root,path,line) key, replaces) a review
// note while the flow is paused for review. It resolves the file via
// Options.ResolveFile to capture display_path/reference/content_sha (and, for
// a line note, the original line text) and to detect two kinds of staleness
// against the client's view of the file: a changed content_sha
// (ErrStaleContent, also returned when the file can't be resolved at all —
// e.g. it no longer exists) and a line number that's fallen out of range
// (ErrStaleLine). expectedRev implements optimistic concurrency against
// concurrent editors of the same note list (ErrRevConflict on mismatch).
// Replacing an existing note keeps its id/created_at — only text/content_sha/
// orig_line_text and the notes-wide rev change.
func (o *Orchestrator) AddNote(root, path string, line *int, text, clientSHA string, expectedRev int) (state.ReviewNote, int, error) {
	o.flowPauseMu.Lock()
	defer o.flowPauseMu.Unlock()

	if err := o.requireReviewPaused(); err != nil {
		return state.ReviewNote{}, 0, err
	}

	notes, err := state.LoadReviewNotes(o.opts.RunDir)
	if err != nil {
		return state.ReviewNote{}, 0, err
	}
	if expectedRev != notes.Rev {
		return state.ReviewNote{}, 0, ErrRevConflict
	}
	if strings.TrimSpace(text) == "" {
		return state.ReviewNote{}, 0, ErrEmptyText
	}
	if len(text) > maxNoteTextBytes {
		return state.ReviewNote{}, 0, ErrNoteTooLong
	}

	rf, ok := o.resolveFile(root, path, line)
	if !ok || rf.ContentSHA != clientSHA {
		return state.ReviewNote{}, 0, ErrStaleContent
	}

	note := state.ReviewNote{
		Root:        root,
		Path:        path,
		DisplayPath: rf.DisplayPath,
		Reference:   rf.Reference,
		Line:        line,
		ContentSHA:  rf.ContentSHA,
		Text:        text,
		CreatedAt:   time.Now().UTC(),
	}
	if line != nil {
		if !rf.InRange {
			return state.ReviewNote{}, 0, ErrStaleLine
		}
		lineText := rf.LineText
		note.OrigLineText = &lineText
	}

	replaced := false
	for i, existing := range notes.Notes {
		if existing.Root == root && existing.Path == path && sameLine(existing.Line, line) {
			note.ID = existing.ID
			note.CreatedAt = existing.CreatedAt
			notes.Notes[i] = note
			replaced = true
			break
		}
	}
	if !replaced {
		// The count cap applies only to genuinely new notes — replacing an
		// existing note (same root/path/line) never grows the store.
		if len(notes.Notes) >= maxNotes {
			return state.ReviewNote{}, 0, ErrTooManyNotes
		}
		note.ID = "n" + strconv.Itoa(notes.NextID)
		notes.NextID++
		notes.Notes = append(notes.Notes, note)
	}
	notes.Rev++

	if err := state.SaveReviewNotes(o.opts.RunDir, notes); err != nil {
		return state.ReviewNote{}, 0, err
	}
	return note, notes.Rev, nil
}

// UpdateNote edits the text of an existing note while the flow is paused for
// review, rev-checked like AddNote.
func (o *Orchestrator) UpdateNote(id, text string, expectedRev int) (int, error) {
	o.flowPauseMu.Lock()
	defer o.flowPauseMu.Unlock()

	if err := o.requireReviewPaused(); err != nil {
		return 0, err
	}

	notes, err := state.LoadReviewNotes(o.opts.RunDir)
	if err != nil {
		return 0, err
	}
	if expectedRev != notes.Rev {
		return 0, ErrRevConflict
	}
	if strings.TrimSpace(text) == "" {
		return 0, ErrEmptyText
	}
	if len(text) > maxNoteTextBytes {
		return 0, ErrNoteTooLong
	}

	found := false
	for i := range notes.Notes {
		if notes.Notes[i].ID == id {
			notes.Notes[i].Text = text
			found = true
			break
		}
	}
	if !found {
		return 0, ErrNoteNotFound
	}
	notes.Rev++

	if err := state.SaveReviewNotes(o.opts.RunDir, notes); err != nil {
		return 0, err
	}
	return notes.Rev, nil
}

// DeleteNote removes an existing note while the flow is paused for review,
// rev-checked like AddNote.
func (o *Orchestrator) DeleteNote(id string, expectedRev int) (int, error) {
	o.flowPauseMu.Lock()
	defer o.flowPauseMu.Unlock()

	if err := o.requireReviewPaused(); err != nil {
		return 0, err
	}

	notes, err := state.LoadReviewNotes(o.opts.RunDir)
	if err != nil {
		return 0, err
	}
	if expectedRev != notes.Rev {
		return 0, ErrRevConflict
	}

	idx := -1
	for i, n := range notes.Notes {
		if n.ID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		return 0, ErrNoteNotFound
	}
	notes.Notes = slices.Delete(notes.Notes, idx, idx+1)
	notes.Rev++

	if err := state.SaveReviewNotes(o.opts.RunDir, notes); err != nil {
		return 0, err
	}
	return notes.Rev, nil
}

// ListNotes reads the current review notes. Unlike Add/Update/Delete it has
// no pause gate and takes no lock — a plain read of state.LoadReviewNotes is
// safe to call anytime (e.g. from the HTTP handler goroutine, whether or not
// a review-pause round is active), matching the read-side pattern used
// elsewhere (ReviewState/currentStatus).
func (o *Orchestrator) ListNotes() (state.ReviewNotes, error) {
	return state.LoadReviewNotes(o.opts.RunDir)
}
