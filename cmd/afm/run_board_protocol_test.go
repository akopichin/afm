package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/state"
)

// openRunStore opens runDir's store and closes it when the test ends.
func openRunStore(t *testing.T, runDir string) *state.Store {
	t.Helper()
	store, err := state.Open(runDir, []string{"a"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// reopenRunStore closes store and opens runDir's journal again, as a restarted afm would.
func reopenRunStore(t *testing.T, store *state.Store, runDir string) *state.Store {
	t.Helper()
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return openRunStore(t, runDir)
}

func TestRecordFreshRunProtocol_FreshRunIsTyped(t *testing.T) {
	runDir := t.TempDir()
	store := openRunStore(t, runDir)

	resumed, err := recordFreshRunProtocol(store)
	if err != nil || resumed {
		t.Fatalf("recordFreshRunProtocol = %v, %v; want a fresh run marked without error", resumed, err)
	}
	store = reopenRunStore(t, store, runDir)
	if got := store.Snapshot().Assistants.Mesh.BoardTaskProtocol; got != agentcomm.BoardTaskProtocolTypedV1 {
		t.Fatalf("protocol after reopen = %q, want %q", got, agentcomm.BoardTaskProtocolTypedV1)
	}

	// The restarted run is resumed and keeps its marker; nothing is written again.
	before := store.Snapshot().LastSeq
	if resumed, err := recordFreshRunProtocol(store); err != nil || !resumed {
		t.Fatalf("recordFreshRunProtocol after restart = %v, %v; want resumed", resumed, err)
	}
	if got := store.Snapshot().LastSeq; got != before {
		t.Fatalf("LastSeq = %d, want %d: a resumed run writes nothing", got, before)
	}
}

func TestRecordFreshRunProtocol_ExistingRunStaysLegacy(t *testing.T) {
	runDir := t.TempDir()
	store := openRunStore(t, runDir)
	if _, err := store.EnsurePolicyRecorded(assistants.DepthPolicy{MaxDepth: 3, Source: assistants.DepthSourceDefault, Revision: "rev-1"}, state.Actor{Kind: "system"}); err != nil {
		t.Fatalf("EnsurePolicyRecorded: %v", err)
	}
	store = reopenRunStore(t, store, runDir)
	journal := readJournal(t, runDir)

	resumed, err := recordFreshRunProtocol(store)
	if err != nil || !resumed {
		t.Fatalf("recordFreshRunProtocol = %v, %v; want a resumed run left alone", resumed, err)
	}
	store = reopenRunStore(t, store, runDir)
	if got := store.Snapshot().Assistants.Mesh.BoardTaskProtocol; got != "" {
		t.Fatalf("protocol = %q, want a legacy run to stay legacy", got)
	}
	if got := readJournal(t, runDir); got != journal {
		t.Fatalf("journal changed:\nbefore %s\nafter  %s", journal, got)
	}
}

func readJournal(t *testing.T, runDir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	return string(b)
}
