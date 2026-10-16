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

// ErrRunFinalizing rejects a new review round while finalization still owns the marker file.
var ErrRunFinalizing = errors.New("run is finalizing")

// newOperationID identifies a review round so resume/finalize cannot act on a different round.
func newOperationID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// computeResumeKind determines which of the runner variants a paused stage should be resumed with.
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

// spawnAgentLeased — ЕДИНАЯ точка запуска любого агента стадии: оборачивает concurrency.SpawnAgentLease,
// сохраняя выданный *concurrency.Lease в o.stageLeases на время run(ctx,s) и убирая его в defer.
// RunVerification достаёт lease отсюда, чтобы временно перевести командный слот на верификатора.
// Централизация обязательна: голый SpawnAgent мимо неё не регистрирует lease, и RunVerification молча
// деградирует (slot не учтён в семафоре → реальное превышение max_parallel).
func (o *Orchestrator) spawnAgentLeased(ctx context.Context, s flow.Stage, run func(context.Context, flow.Stage)) {
	o.concurrency.SpawnAgentLease(ctx, s, func(ctx context.Context, s flow.Stage, lease *concurrency.Lease) {
		o.stageLeases.Store(s.ID, lease)
		// E3: CompareAndDelete, а не безусловный Delete — на Revise-прерывании раннер-замена может Store'ить
		// свой lease под тем же stageID ДО того, как ЭТОТ callback размотает свой defer.
		defer o.stageLeases.CompareAndDelete(s.ID, lease)
		run(ctx, s)
	})
}

func (o *Orchestrator) spawnKind(ctx context.Context, s flow.Stage, kind string, run func(context.Context, flow.Stage)) {
	o.setRunnerKind(s.ID, kind)
	o.spawnAgentLeased(ctx, s, run)
}

// PauseFlow holds activation and pauses active non-script agents; the durable marker owns only successful
// CAS transitions. Idempotent.
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
		stage := o.reg().Stage(id)
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

		// Clear any stale "already resumed" mark left by a PRIOR review round on this same stage — this round
		// re-pauses it, so a leftover entry would make its resume wrongly skip re-spawning.
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
		// Marker write failed: release activation hold and report paused stage IDs so callers can Continue them
		// manually.
		o.activationHeld.Store(false)
		return ids, fmt.Errorf("write pause marker (these stages are paused, Continue them manually): %w", err)
	}
	o.reviewMarker.Store(&m)
	return ids, nil
}

// ReviewState reports none/resuming/paused and currently paused owners using atomic marker reads and live
// stage status.
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

// renderReviewFeedback renders collected review notes into feedback-markdown text that gets injected into
// a stage.
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
	// Cap the aggregate so a large collection can't produce an unbounded agent prompt. Truncate on a line
	// boundary and mark the cut.
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

// withFeedbackRunner maps a resume_kind to the *WithFeedback variant of the matching runner — used by
// resumeOwner when the resumed stage must see a feedback.md.
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

// plainRunner maps a resume_kind to the plain (fresh, no feedback.md) variant of the matching runner —
// used by resumeOwner for a non-target owner that wasn't paused mid-revising.
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

// resumeOwner resumes a single owned, paused stage after finalize decided review notes should be injected.
func (o *Orchestrator) resumeOwner(ctx context.Context, ow state.PauseOwner, isTarget bool) bool {
	for o.concurrency.IsActive(ow.ID) {
		select {
		case <-ctx.Done():
			return false // drain interrupted: a later retry must resume this owner
		case <-time.After(50 * time.Millisecond):
		}
	}

	stage := o.reg().Stage(ow.ID)
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

	// Dispatch on the owner's CURRENT status, not on the assumption that it's still paused. Anything else
	// (done/failed — recovered completion, or already resumed): nothing to do.
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
		o.spawnAgentLeased(ctx, *stage, runner)
		return true
	case state.StatusRunning, state.StatusPlanning, state.StatusRevising, state.StatusRetrying:
		o.spawnAgentLeased(ctx, *stage, runner)
		return true
	default:
		return true // terminal or already-resumed: nothing to spawn
	}
}

// currentFileSHA resolves the current content_sha of a file, used by renderReviewFeedback to detect drift
// between when a note was taken and when it's injected.
func (o *Orchestrator) currentFileSHA(root, path string) (string, bool) {
	if o.opts.CurrentFileSHA == nil {
		return "", false
	}
	return o.opts.CurrentFileSHA(root, path)
}

// Errors returned by the durable review resume transaction.
var (
	ErrNoReviewPause    = errors.New("no review pause active")
	ErrNoNotes          = errors.New("no notes to inject")
	ErrTargetNotPaused  = errors.New("target is not paused")
	ErrNotOwned         = errors.New("stage is not owned by the review pause")
	ErrResumeInProgress = errors.New("review resume already in progress")
)

// ownsStage reports whether id is one of the stages a pause marker claims ownership of (see PauseFlow:
// only stages whose EvPause CAS actually won are recorded as owners).
func ownsStage(m *state.PauseMarker, id string) bool {
	for _, ow := range m.Owned {
		if ow.ID == id {
			return true
		}
	}
	return false
}

// InjectNotesAndResume closes a review-pause round by delivering the collected review notes to
// targetStageID and resuming every owned stage.
func (o *Orchestrator) InjectNotesAndResume(reqCtx context.Context, targetStageID string) error {
	o.flowPauseMu.Lock()
	m := o.reviewMarker.Load()
	if m == nil {
		o.flowPauseMu.Unlock()
		return ErrNoReviewPause
	}
	// Only a round still in `paused` may be closed. Guarded under flowPauseMu, atomic with the flip below and
	// with PauseFlow.
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

// CancelNotesAndResume durably starts an asynchronous resume of all owners without feedback.
func (o *Orchestrator) CancelNotesAndResume(reqCtx context.Context) error {
	o.flowPauseMu.Lock()
	m := o.reviewMarker.Load()
	if m == nil {
		o.flowPauseMu.Unlock()
		return ErrNoReviewPause
	}
	// See InjectNotesAndResume: reject a repeat Cancel once the round is already resuming — the second worker
	// would re-spawn owners that are no longer paused.
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

// runResumeTransaction resumes owners and nudges scheduling before deleting notes and marker, preserving
// recovery after a crash.
func (o *Orchestrator) runResumeTransaction(ctx context.Context, m state.PauseMarker) error {
	// Re-deliver the review feedback idempotently BEFORE resuming any owner. SaveFeedbackOnce is keyed on the
	// operation id, so the live path's earlier write makes this a no-op there.
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
		// Skip owners already resumed by this transaction; mark only after successful handling so cancellation
		// remains recoverable.
		if _, done := o.reviewResumed.Load(ow.ID); done {
			continue
		}
		isTarget := m.Mode == state.PauseModeInject && ow.ID == m.TargetStage
		if o.resumeOwner(ctx, ow, isTarget) {
			o.reviewResumed.Store(ow.ID, struct{}{})
		}
	}
	// Keep notes and marker on cancellation so the next run can resume unhandled owners.
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// Full scheduling cascade, not just startReadyStages: while activationHeld was set, unblocked dependents
	// were parked in `pending` by tryActivatePrePlanned / startPlanningForUnblocked.
	o.failBlockedStages()
	o.startPlanningForUnblocked(ctx)
	o.startReadyStages(ctx)
	o.tryActivatePrePlanned(ctx)

	// Durable cleanup is part of the transaction result: only once notes AND marker are gone from disk may
	// the in-memory marker be dropped.
	if err := o.clearReviewArtifacts(); err != nil {
		return err
	}
	o.reviewMarker.Store(nil)
	// NB: reviewResumed entries are deliberately NOT cleared here. A LATER review round re-pauses its owners
	// via PauseFlow, which clears any stale entry there.
	o.concurrency.WakeEventLoop()
	return nil
}

// clearReviewArtifacts removes the durable review-notes and pause-marker files. Overridable in tests via
// testClearReviewArtifacts to exercise the fail-closed cleanup path.
func (o *Orchestrator) clearReviewArtifacts() error {
	if o.testClearReviewArtifacts != nil {
		return o.testClearReviewArtifacts()
	}
	if err := state.DeleteReviewNotes(o.opts.RunDir); err != nil {
		return err
	}
	return state.ClearNotesPauseMarker(o.opts.RunDir)
}

// Sentinels for the notes CRUD methods below.
var (
	ErrRevConflict  = errors.New("notes revision conflict")
	ErrStaleContent = errors.New("file content changed since the note was captured")
	ErrStaleLine    = errors.New("line is out of range for the current file")
	ErrEmptyText    = errors.New("note text is empty")
	ErrNoteNotFound = errors.New("note not found")
	ErrNoteTooLong  = errors.New("note text exceeds the maximum length")
	ErrTooManyNotes = errors.New("too many review notes")
)

// Resource limits for the review-notes store. Enforced in AddNote/UpdateNote (per note + count) and
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

// resolveFile is the nil-safe wrapper around Options.ResolveFile, mirroring the currentFileSHA pattern
// above: nil means "can't resolve anything", not a panic.
func (o *Orchestrator) resolveFile(root, path string, line *int) (ResolvedFile, bool) {
	if o.opts.ResolveFile == nil {
		return ResolvedFile{}, false
	}
	return o.opts.ResolveFile(root, path, line)
}

// requireReviewPaused permits notes CRUD only while the active review marker is paused, never while
// resuming.
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

// AddNote adds a review note while the flow is paused for review. Replacing an existing note keeps its
// id/created_at — only text/content_sha/ orig_line_text and the notes-wide rev change.
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

// ListNotes reads the current review notes.
func (o *Orchestrator) ListNotes() (state.ReviewNotes, error) {
	return state.LoadReviewNotes(o.opts.RunDir)
}
