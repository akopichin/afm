package main

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	implgraph "github.com/akopichin/afm-engine/testkit/orchestrator/graph"
	implstate "github.com/akopichin/afm-engine/testkit/state"
	"github.com/akopichin/afm/contracts/flow"
)

// Dynamic graph changes survive restart without rewriting the original flow.yaml.

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
	frozen, err := implgraph.RestoreOrPersist(runDir, parsed)
	if err != nil {
		t.Fatalf("RestoreOrPersist (first start): %v", err)
	}

	store, err := implstate.Open(runDir, []string{"a", "b"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	op := implstate.StageGraphOperation{
		ID: "op-1",
		Payload: implstate.StageGraphPayload{
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
	frozenAgain, err := implgraph.RestoreOrPersist(runDir, parsed)
	if err != nil {
		t.Fatalf("RestoreOrPersist (resume): %v", err)
	}
	if len(frozenAgain) != len(frozen) {
		t.Fatalf("RestoreOrPersist returned a different frozen set on resume: %+v vs %+v", frozenAgain, frozen)
	}

	store2, err := implstate.Open(runDir, []string{"a", "b"})
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	defer store2.Close()
	snap := store2.Snapshot()
	restored := implgraph.Materialize(frozenAgain, snap.DynamicStages, snap.StageOrder)
	if len(restored) != 3 {
		t.Fatalf("materialized stage set = %+v, want 3 stages (a, n, b)", restored)
	}

	after := checksum()
	if before != after {
		t.Fatalf("flow.yaml was modified by the dynamic-stage add+restart round trip (checksum %x -> %x)", before, after)
	}
}
