package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLoadReviewNotes_EmptyStoreSerializesAsArray закрывает review-finding P1
// #6 (backend-половина): свежий store БЕЗ файла review-notes.json обязан отдать
// НЕ nil-срез Notes, иначе encoding/json кодирует его как `null`, и дашбордный
// ReviewNotesModal падает с TypeError на `for (const note of notes)`. Проверяем
// не только пустой файл, но и store, который был сохранён с нулём заметок и
// round-trip'нулся через JSON как `"notes": null`.
func TestLoadReviewNotes_EmptyStoreSerializesAsArray(t *testing.T) {
	dir := t.TempDir()

	// (1) Файла нет вовсе.
	fresh, err := LoadReviewNotes(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Notes == nil {
		t.Fatal("missing-file store must have a non-nil Notes slice")
	}
	if b, _ := json.Marshal(fresh); !wantArray(b) {
		t.Fatalf(`missing-file store serializes notes as null, not []: %s`, b)
	}

	// (2) Файл есть, но заметок ноль — сериализуется как "notes": null на диске.
	if err := os.WriteFile(filepath.Join(dir, reviewNotesFile),
		[]byte(`{"version":1,"rev":0,"next_id":1,"notes":null}`), 0644); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadReviewNotes(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Notes == nil {
		t.Fatal("zero-notes store must be normalized to a non-nil slice")
	}
	if b, _ := json.Marshal(loaded); !wantArray(b) {
		t.Fatalf(`zero-notes store serializes as null, not []: %s`, b)
	}
}

// wantArray reports whether the marshaled ReviewNotes JSON encodes notes as a
// (possibly empty) array rather than null.
func wantArray(b []byte) bool {
	var probe struct {
		Notes json.RawMessage `json:"notes"`
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		return false
	}
	return string(probe.Notes) == "[]" || (len(probe.Notes) > 0 && probe.Notes[0] == '[')
}

func TestReviewNotes_SaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	loaded, err := LoadReviewNotes(dir)
	if err != nil || loaded.Version != 1 || loaded.NextID != 1 || len(loaded.Notes) != 0 {
		t.Fatalf("empty default wrong: %+v err=%v", loaded, err)
	}
	line := 42
	orig := "\tif err != nil {"
	loaded.Notes = append(loaded.Notes, ReviewNote{
		ID: "n1", Root: "project", Path: "a.go", DisplayPath: "project/a.go",
		Reference: `[AFM file: "/w/a.go"]`, Line: &line, OrigLineText: &orig,
		ContentSHA: "sha256:x", Text: "fix", CreatedAt: time.Unix(0, 0).UTC(),
	})
	loaded.NextID = 2
	loaded.Rev = 1
	if err := SaveReviewNotes(dir, loaded); err != nil {
		t.Fatal(err)
	}
	again, err := LoadReviewNotes(dir)
	if err != nil || len(again.Notes) != 1 || again.NextID != 2 || *again.Notes[0].Line != 42 {
		t.Fatalf("roundtrip wrong: %+v err=%v", again, err)
	}
}

func TestReviewNotes_CorruptQuarantines(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, reviewNotesFile), []byte("{not json"), 0644)
	loaded, err := LoadReviewNotes(dir)
	if err != nil || len(loaded.Notes) != 0 {
		t.Fatalf("want empty+nil, got %+v err=%v", loaded, err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, reviewNotesFile+".corrupt-*"))
	if len(matches) != 1 {
		t.Fatalf("want 1 quarantine file, got %d", len(matches))
	}
}

func TestDeleteReviewNotes_Idempotent(t *testing.T) {
	dir := t.TempDir()
	if err := DeleteReviewNotes(dir); err != nil {
		t.Fatalf("delete on missing should be nil: %v", err)
	}
}

func TestPauseMarker_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	_, found, err := ReadNotesPauseMarker(dir)
	if found || err != nil {
		t.Fatalf("absent marker: found=%v err=%v", found, err)
	}
	m := PauseMarker{
		Version: 1, OperationID: "op1", State: PauseStatePaused,
		CreatedAt: time.Unix(0, 0).UTC(),
		Owned:     []PauseOwner{{ID: "s1", ResumeKind: "implementation", FromRevising: false}},
	}
	if err := WriteNotesPauseMarker(dir, m); err != nil {
		t.Fatal(err)
	}
	got, found, err := ReadNotesPauseMarker(dir)
	if !found || err != nil || got.State != PauseStatePaused || got.Owned[0].ID != "s1" {
		t.Fatalf("roundtrip wrong: %+v found=%v err=%v", got, found, err)
	}
	if err := ClearNotesPauseMarker(dir); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := ReadNotesPauseMarker(dir); found {
		t.Fatal("marker should be gone")
	}
}

func TestPauseMarker_CorruptReturnsErr(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, notesPauseMarkerFile), []byte("{bad"), 0644)
	_, found, err := ReadNotesPauseMarker(dir)
	if !found || err == nil || !errors.Is(err, ErrCorruptPauseMarker) {
		t.Fatalf("want found+ErrCorrupt, got found=%v err=%v", found, err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, notesPauseMarkerFile+".corrupt-*"))
	if len(matches) != 1 {
		t.Fatal("want quarantine copy")
	}
}

func TestPauseMarker_CorruptDetectsState(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, notesPauseMarkerFile), []byte(`{"state":"resuming" bad`), 0644)
	m, found, err := ReadNotesPauseMarker(dir)
	if !found || err == nil || !errors.Is(err, ErrCorruptPauseMarker) || m.State != PauseStateResuming {
		t.Fatalf("want resuming best-effort, got state=%q found=%v err=%v", m.State, found, err)
	}
}
