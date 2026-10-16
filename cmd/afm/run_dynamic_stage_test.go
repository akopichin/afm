package main

// Этап 9, T9.5, §11.6 — adding a dynamic stage and resuming the run must never touch the original flow.yaml
// file on disk: the frozen stage registry (graph_registry.json, T8.6) and a run's durable stage_graph_added
// records (T9.4) are the ONLY things that grow — flow.yaml itself is read-only input, re-parsed on every
// restart (run.go) but never written back to. Nothing in RestoreOrPersist/AddStageToGraph/Materialize opens
// flow.yaml for writing today, but that is exactly the kind of invariant a future "sync edits back" feature
// could silently break — pin it explicitly, mirroring run_registry_test.go's documented stand-in style (a
// full executeFlow run is disproportionate for an ordering/invariant test like this one).

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/state"
)

func TestRestart_OriginalFlowYamlUnchanged(t *testing.T) {
	projectDir := t.TempDir()
	flowPath := filepath.Join(projectDir, "flow.yaml")
	original := []byte("name: demo\nstages:\n  - id: a\n  - id: b\n    depends_on: [a]\n")
	if err := os.WriteFile(flowPath, original, 0644); err != nil {
		t.Fatal(err)
	}
	checksum := func() [32]byte {
		data, err := os.ReadFile(flowPath)
		if err != nil {
			t.Fatal(err)
		}
		return sha256.Sum256(data)
	}
	before := checksum()

	runDir := filepath.Join(projectDir, ".afm", "runs", "run1")
	parsed := []flow.Stage{{ID: "a"}, {ID: "b", DependsOn: []string{"a"}}}

	// First start: freeze the registry (same call site as run.go).
	frozen, err := graph.RestoreOrPersist(runDir, parsed)
	if err != nil {
		t.Fatalf("RestoreOrPersist (first start): %v", err)
	}

	store, err := state.Open(runDir, []string{"a", "b"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	op := state.StageGraphOperation{
		ID: "op-1",
		Payload: state.StageGraphPayload{
			Stage:     flow.Stage{ID: "n", DependsOn: []string{"a"}},
			ExtraDeps: map[string][]string{"b": {"n"}},
			Order:     []string{"a", "n", "b"},
		},
	}
	if _, err := store.AddStageToGraph(op); err != nil {
		t.Fatalf("AddStageToGraph: %v", err)
	}
	store.Close()

	// "Restart": re-parse the (unchanged) flow.yaml, restore the frozen registry, fold the dynamic addition
	// back on top — the exact sequence run.go's restart path uses.
	frozenAgain, err := graph.RestoreOrPersist(runDir, parsed)
	if err != nil {
		t.Fatalf("RestoreOrPersist (resume): %v", err)
	}
	if len(frozenAgain) != len(frozen) {
		t.Fatalf("RestoreOrPersist returned a different frozen set on resume: %+v vs %+v", frozenAgain, frozen)
	}

	store2, err := state.Open(runDir, []string{"a", "b"})
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	defer store2.Close()
	snap := store2.Snapshot()
	restored := graph.Materialize(frozenAgain, snap.DynamicStages, snap.StageOrder)
	if len(restored) != 3 {
		t.Fatalf("materialized stage set = %+v, want 3 stages (a, n, b)", restored)
	}

	after := checksum()
	if before != after {
		t.Fatalf("flow.yaml was modified by the dynamic-stage add+restart round trip (checksum %x -> %x)", before, after)
	}
}
