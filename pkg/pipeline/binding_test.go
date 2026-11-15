package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sampleBinding(runID, worktree string, updated time.Time) Binding {
	return Binding{
		RunID: runID, RunName: "demo", Worktree: worktree,
		Package:       PackageRef{Name: "demo-pipe", Version: "1"},
		Source:        Source{Kind: SourceGit, Location: "https://example.com/r", Subdir: "p", Ref: "main"},
		Commit:        "abc",
		ContentSHA256: "def",
		SnapshotDir:   "/cache/snapshots/def",
		Options:       &OptionsRef{Path: "/o.yaml", SHA256: "123"},
		Recipes:       map[string]RecipeRecord{"demo-pipe-writer": {Type: "claude", Identity: "id1"}},
		CreatedAt:     updated, UpdatedAt: updated,
	}
}

func TestBindingRoundTripAndFind(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs")
	now := time.Now().UTC().Truncate(time.Second)
	older := sampleBinding("demo-20260101", "", now.Add(-time.Hour))
	newer := sampleBinding("demo-20260102", "", now)
	named := sampleBinding("feature-20260103", "feature", now)
	for _, b := range []Binding{older, newer, named} {
		if err := WriteBinding(dir, b); err != nil {
			t.Fatal(err)
		}
	}
	all, err := ReadBindings(dir)
	if err != nil || len(all) != 3 {
		t.Fatalf("ReadBindings = %d, %v", len(all), err)
	}
	identity := older.Source.Identity()
	got := FindBindings(all, identity, "")
	if len(got) != 2 || got[0].RunID != newer.RunID || got[1].RunID != older.RunID {
		t.Fatalf("FindBindings(no worktree) = %+v", got)
	}
	if got := FindBindings(all, identity, "feature"); len(got) != 1 || got[0].RunID != named.RunID {
		t.Fatalf("FindBindings(feature) = %+v", got)
	}
	if got := FindBindings(all, "git:https://other//p", ""); len(got) != 0 {
		t.Fatalf("a different source must not match: %+v", got)
	}
	if got[0].Options == nil || got[0].Recipes["demo-pipe-writer"].Identity != "id1" {
		t.Fatalf("fields lost: %+v", got[0])
	}
}

func TestReadBindingsReportsDamagedFilesButKeepsTheRest(t *testing.T) {
	dir := t.TempDir()
	if err := WriteBinding(dir, sampleBinding("good", "", time.Now())); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mismatch.json"), []byte(`{"run_id":"other"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadBindings(dir)
	if len(got) != 1 || got[0].RunID != "good" {
		t.Fatalf("got = %+v", got)
	}
	if err == nil || !strings.Contains(err.Error(), "broken.json") || !strings.Contains(err.Error(), "mismatch.json") {
		t.Fatalf("err = %v", err)
	}
}

func TestReadBindingsMissingDirIsEmpty(t *testing.T) {
	got, err := ReadBindings(filepath.Join(t.TempDir(), "none"))
	if got != nil || err != nil {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestBindingRejectsUnsafeRunID(t *testing.T) {
	for _, id := range []string{"", "..", "a/b", `a\b`} {
		if err := WriteBinding(t.TempDir(), Binding{RunID: id}); err == nil {
			t.Errorf("run id %q accepted", id)
		}
	}
}

func TestDeleteBinding(t *testing.T) {
	dir := t.TempDir()
	if err := WriteBinding(dir, sampleBinding("x", "", time.Now())); err != nil {
		t.Fatal(err)
	}
	if err := DeleteBinding(dir, "x"); err != nil {
		t.Fatal(err)
	}
	if err := DeleteBinding(dir, "x"); err != nil {
		t.Fatalf("deleting a missing binding: %v", err)
	}
	if got, _ := ReadBindings(dir); len(got) != 0 {
		t.Fatalf("still present: %+v", got)
	}
}

// A binding only carries selectors and digests; the JSON must not grow a place for secret values.
func TestBindingCarriesNoSecretFields(t *testing.T) {
	data, err := json.Marshal(sampleBinding("x", "", time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	for key := range fields {
		for _, banned := range []string{"secret", "token", "key", "password", "env"} {
			if strings.Contains(strings.ToLower(key), banned) {
				t.Errorf("binding field %q looks like a secret carrier", key)
			}
		}
	}
}
