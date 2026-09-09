package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const reviewNotesFile = "review-notes.json"

type ReviewNote struct {
	ID           string    `json:"id"`
	Root         string    `json:"root"`
	Path         string    `json:"path"`
	DisplayPath  string    `json:"display_path"`
	Reference    string    `json:"reference"`
	Line         *int      `json:"line"`
	OrigLineText *string   `json:"orig_line_text"`
	ContentSHA   string    `json:"content_sha"`
	Text         string    `json:"text"`
	CreatedAt    time.Time `json:"created_at"`
}

type ReviewNotes struct {
	Version int          `json:"version"`
	Rev     int          `json:"rev"`
	NextID  int          `json:"next_id"`
	Notes   []ReviewNote `json:"notes"`
}

func reviewNotesPath(runDir string) string { return filepath.Join(runDir, reviewNotesFile) }

func SaveReviewNotes(runDir string, n ReviewNotes) error {
	if n.Version == 0 {
		n.Version = 1
	}
	data, err := json.MarshalIndent(n, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal review notes: %w", err)
	}
	return atomicWriteFile(reviewNotesPath(runDir), data, 0644)
}

func LoadReviewNotes(runDir string) (ReviewNotes, error) {
	path := reviewNotesPath(runDir)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return emptyReviewNotes(), nil
	}
	if err != nil {
		return emptyReviewNotes(), fmt.Errorf("read review notes: %w", err)
	}
	var n ReviewNotes
	if err := json.Unmarshal(data, &n); err != nil || n.Version != 1 {
		// Advisory data: quarantine the bad file and start empty.
		_ = os.Rename(path, fmt.Sprintf("%s.corrupt-%d", path, time.Now().UnixNano()))
		return emptyReviewNotes(), nil
	}
	if n.NextID == 0 {
		n.NextID = 1
	}
	// A store that was persisted with zero notes round-trips through JSON as
	// `"notes": null`; force a non-nil slice so the API always serializes `[]`
	// and never `null` (a `null` breaks the dashboard's ReviewNotesModal, which
	// iterates the array without a runtime nil-guard).
	if n.Notes == nil {
		n.Notes = []ReviewNote{}
	}
	return n, nil
}

// emptyReviewNotes is the canonical "no notes yet" value — a fresh store with a
// non-nil (empty) Notes slice so it serializes as `[]`, never `null`.
func emptyReviewNotes() ReviewNotes {
	return ReviewNotes{Version: 1, NextID: 1, Notes: []ReviewNote{}}
}

func DeleteReviewNotes(runDir string) error {
	if err := os.Remove(reviewNotesPath(runDir)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete review notes: %w", err)
	}
	// fsync the parent so the unlink is durable — otherwise a crash right after
	// a resume "completed" could resurrect the deleted notes and leak stale
	// review comments into the next round (see runResumeTransaction cleanup).
	return fsyncDir(runDir)
}

const notesPauseMarkerFile = "notes-pause.json"

const (
	PauseStatePaused   = "paused"
	PauseStateResuming = "resuming"
	PauseModeInject    = "inject"
	PauseModeCancel    = "cancel"
)

var ErrCorruptPauseMarker = errors.New("corrupt pause marker")

type PauseOwner struct {
	ID           string `json:"id"`
	ResumeKind   string `json:"resume_kind"`
	FromRevising bool   `json:"from_revising"`
}

type PauseMarker struct {
	Version     int          `json:"version"`
	OperationID string       `json:"operation_id"`
	State       string       `json:"state"`
	Mode        string       `json:"mode"`
	TargetStage string       `json:"target_stage"`
	CreatedAt   time.Time    `json:"created_at"`
	Owned       []PauseOwner `json:"owned"`
}

func notesPauseMarkerPath(runDir string) string {
	return filepath.Join(runDir, notesPauseMarkerFile)
}

func WriteNotesPauseMarker(runDir string, m PauseMarker) error {
	if m.Version == 0 {
		m.Version = 1
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal pause marker: %w", err)
	}
	return atomicWriteFile(notesPauseMarkerPath(runDir), data, 0644)
}

func ReadNotesPauseMarker(runDir string) (PauseMarker, bool, error) {
	path := notesPauseMarkerPath(runDir)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return PauseMarker{}, false, nil
	}
	if err != nil {
		return PauseMarker{}, true, fmt.Errorf("read pause marker: %w", err)
	}
	var m PauseMarker
	if err := json.Unmarshal(data, &m); err != nil || m.Version != 1 {
		// Best-effort state detection before quarantining
		best := PauseMarker{State: PauseStatePaused}
		if bytes.Contains(data, []byte(`"resuming"`)) {
			best.State = PauseStateResuming
		}
		_ = os.Rename(path, fmt.Sprintf("%s.corrupt-%d", path, time.Now().UnixNano()))
		return best, true, ErrCorruptPauseMarker
	}
	return m, true, nil
}

func ClearNotesPauseMarker(runDir string) error {
	if err := os.Remove(notesPauseMarkerPath(runDir)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear pause marker: %w", err)
	}
	return fsyncDir(runDir)
}

const notesPausePoisonFile = "notes-pause.poison"

func notesPausePoisonPath(runDir string) string {
	return filepath.Join(runDir, notesPausePoisonFile)
}

// WritePoisonMarker durably records that a review-pause resume transaction was
// found un-recoverable (a corrupt `resuming` marker). Unlike the canonical
// marker — which ReadNotesPauseMarker quarantines/renames on corruption, so it
// would vanish on the next restart — this breadcrumb persists until an operator
// removes it, so the run stays fail-closed across ANY number of restarts rather
// than silently returning to normal scheduling after one. `detail` is a
// human-readable note for whoever inspects it. Best-effort content; its mere
// existence is the signal.
func WritePoisonMarker(runDir, detail string) error {
	return atomicWriteFile(notesPausePoisonPath(runDir), []byte(detail+"\n"), 0644)
}

// HasPoisonMarker reports whether a durable poison breadcrumb is present.
func HasPoisonMarker(runDir string) bool {
	_, err := os.Stat(notesPausePoisonPath(runDir))
	return err == nil
}

// ClearPoisonMarker removes the poison breadcrumb (operator-driven manual
// recovery — the only way back to normal scheduling).
func ClearPoisonMarker(runDir string) error {
	if err := os.Remove(notesPausePoisonPath(runDir)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear poison marker: %w", err)
	}
	return fsyncDir(runDir)
}

// fsyncDir flushes a directory entry change (an unlink here) to disk, the same
// durability step atomicWriteFile already does after a rename. A non-existent
// dir is not an error (the run dir is always present in practice).
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("open dir for fsync: %w", err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("fsync dir: %w", err)
	}
	return nil
}
