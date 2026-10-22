package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
)

var protocolActor = Actor{Kind: "system", ID: "afm"}

func openProtocolStore(t *testing.T, dir string) *Store {
	t.Helper()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestRecordBoardTaskProtocolFreshRun(t *testing.T) {
	dir := t.TempDir()
	store := openProtocolStore(t, dir)
	if err := store.RecordBoardTaskProtocol(agentcomm.BoardTaskProtocolTypedV1, protocolActor); err != nil {
		t.Fatalf("RecordBoardTaskProtocol: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := openProtocolStore(t, dir)
	if got := reopened.Snapshot().Assistants.Mesh.BoardTaskProtocol; got != agentcomm.BoardTaskProtocolTypedV1 {
		t.Fatalf("protocol after reopen = %q, want %q", got, agentcomm.BoardTaskProtocolTypedV1)
	}
}

func TestRecordBoardTaskProtocolRefusesOldRun(t *testing.T) {
	dir := t.TempDir()
	store := openProtocolStore(t, dir)
	if _, err := store.EnsurePolicyRecorded(depthPolicy(3, "rev-1"), protocolActor); err != nil {
		t.Fatalf("EnsurePolicyRecorded: %v", err)
	}
	seqBefore := store.Snapshot().LastSeq

	err := store.RecordBoardTaskProtocol(agentcomm.BoardTaskProtocolTypedV1, protocolActor)
	var rejected *OperationRejectedError
	if !errors.As(err, &rejected) || rejected.Code != assistants.CodeInvalidStatus {
		t.Fatalf("err = %v, want OperationRejectedError with CodeInvalidStatus", err)
	}
	snap := store.Snapshot()
	if snap.LastSeq != seqBefore {
		t.Fatalf("journal grew: LastSeq %d -> %d", seqBefore, snap.LastSeq)
	}
	if snap.Assistants.Mesh.BoardTaskProtocol != "" {
		t.Fatalf("old run must stay legacy, got %q", snap.Assistants.Mesh.BoardTaskProtocol)
	}
}

func TestBoardTaskProtocolSurvivesMissingSnapshot(t *testing.T) {
	dir := t.TempDir()
	store := openProtocolStore(t, dir)
	if err := store.RecordBoardTaskProtocol(agentcomm.BoardTaskProtocolTypedV1, protocolActor); err != nil {
		t.Fatalf("RecordBoardTaskProtocol: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "state.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remove state.json: %v", err)
	}

	reopened := openProtocolStore(t, dir)
	if got := reopened.Snapshot().Assistants.Mesh.BoardTaskProtocol; got != agentcomm.BoardTaskProtocolTypedV1 {
		t.Fatalf("protocol after snapshot loss = %q, want %q", got, agentcomm.BoardTaskProtocolTypedV1)
	}
}

func TestRecordBoardTaskProtocolIdempotent(t *testing.T) {
	store := openProtocolStore(t, t.TempDir())
	for i := 0; i < 2; i++ {
		if err := store.RecordBoardTaskProtocol(agentcomm.BoardTaskProtocolTypedV1, protocolActor); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := store.Snapshot().LastSeq; got != 1 {
		t.Fatalf("LastSeq = %d, want 1 (second call must not write)", got)
	}
}
