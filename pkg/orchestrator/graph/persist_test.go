package graph_test

import (
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
)

func TestSaveLoadRegistry_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	stages := []flow.Stage{{ID: "a", Name: "A"}, {ID: "b", DependsOn: []string{"a"}}}
	if err := graph.SaveRegistry(dir, stages); err != nil {
		t.Fatalf("SaveRegistry: %v", err)
	}
	got, found, err := graph.LoadRegistry(dir)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if !found {
		t.Fatal("found = false, want true")
	}
	if len(got) != 2 || got[0].ID != "a" || got[1].DependsOn[0] != "a" {
		t.Fatalf("LoadRegistry = %+v, want round-tripped stages", got)
	}
}

func TestLoadRegistry_NotFound(t *testing.T) {
	dir := t.TempDir()
	_, found, err := graph.LoadRegistry(dir)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if found {
		t.Fatal("found = true for a run that never called SaveRegistry")
	}
}

func TestSaveRegistry_SecondCallIsNoOp(t *testing.T) {
	dir := t.TempDir()
	original := []flow.Stage{{ID: "a"}}
	if err := graph.SaveRegistry(dir, original); err != nil {
		t.Fatal(err)
	}
	edited := []flow.Stage{{ID: "a"}, {ID: "b"}}
	if err := graph.SaveRegistry(dir, edited); err != nil {
		t.Fatal(err)
	}
	got, _, err := graph.LoadRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("SaveRegistry overwrote an existing registry: got %+v, want the original single stage", got)
	}
}

func TestRestoreOrPersist_FreshRunPersistsAndReturnsFresh(t *testing.T) {
	dir := t.TempDir()
	fresh := []flow.Stage{{ID: "a"}}
	got, err := graph.RestoreOrPersist(dir, fresh)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("RestoreOrPersist (fresh) = %+v", got)
	}
	stored, found, err := graph.LoadRegistry(dir)
	if err != nil || !found {
		t.Fatalf("expected RestoreOrPersist to durably persist fresh: found=%v err=%v", found, err)
	}
	if len(stored) != 1 {
		t.Fatalf("stored = %+v", stored)
	}
}

func TestRestoreOrPersist_ResumedRunKeepsStoredGraph(t *testing.T) {
	dir := t.TempDir()
	original := []flow.Stage{{ID: "a", Name: "Original"}}
	if _, err := graph.RestoreOrPersist(dir, original); err != nil {
		t.Fatal(err)
	}
	edited := []flow.Stage{{ID: "a", Name: "Edited"}, {ID: "b", Name: "New"}}
	got, err := graph.RestoreOrPersist(dir, edited)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "Original" {
		t.Fatalf("RestoreOrPersist (resumed) = %+v, want the original stored graph, not the edited one", got)
	}
}
