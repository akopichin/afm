package orchestrator

import (
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
			if li == nil {
				return 1 // file-level note goes last
			}
			if lj == nil {
				return -1 // line note comes first
			}
			if *li < *lj {
				return -1
			}
			if *li > *lj {
				return 1
			}
			return 0
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
