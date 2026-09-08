package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
