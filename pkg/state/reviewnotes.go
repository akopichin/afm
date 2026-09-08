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
		return ReviewNotes{Version: 1, NextID: 1}, nil
	}
	if err != nil {
		return ReviewNotes{Version: 1, NextID: 1}, fmt.Errorf("read review notes: %w", err)
	}
	var n ReviewNotes
	if err := json.Unmarshal(data, &n); err != nil || n.Version != 1 {
		// Advisory data: quarantine the bad file and start empty.
		_ = os.Rename(path, fmt.Sprintf("%s.corrupt-%d", path, time.Now().UnixNano()))
		return ReviewNotes{Version: 1, NextID: 1}, nil
	}
	if n.NextID == 0 {
		n.NextID = 1
	}
	return n, nil
}

func DeleteReviewNotes(runDir string) error {
	if err := os.Remove(reviewNotesPath(runDir)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete review notes: %w", err)
	}
	return nil
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
	return nil
}
