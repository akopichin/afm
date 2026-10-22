package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/clock"
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

// A legacy run is recognized after a restart too: Open restores LastSeq from the journal, so a reopened run
// with records but no marker is still refused (and run.go sees it as resumed).
func TestRecordBoardTaskProtocolRefusesReopenedOldRun(t *testing.T) {
	dir := t.TempDir()
	store := openProtocolStore(t, dir)
	if _, err := store.EnsurePolicyRecorded(depthPolicy(3, "rev-1"), protocolActor); err != nil {
		t.Fatalf("EnsurePolicyRecorded: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := openProtocolStore(t, dir)
	if got := reopened.Snapshot().LastSeq; got != 1 {
		t.Fatalf("LastSeq after reopen = %d, want 1 from the journal", got)
	}
	err := reopened.RecordBoardTaskProtocol(agentcomm.BoardTaskProtocolTypedV1, protocolActor)
	var rejected *OperationRejectedError
	if !errors.As(err, &rejected) || rejected.Code != assistants.CodeInvalidStatus {
		t.Fatalf("err = %v, want OperationRejectedError with CodeInvalidStatus", err)
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

// The protocol marker is a fresh run's first journal record, so the run starts when it was written: live,
// after a reopen and in a read-only load alike.
func TestRunStartedAtIsTheProtocolMarkerTime(t *testing.T) {
	opened := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	fc := clock.NewFake(opened)
	withFakeClock(t, fc)
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	fc.Advance(time.Second)
	marked := fc.Now()
	if err := store.RecordBoardTaskProtocol(agentcomm.BoardTaskProtocolTypedV1, protocolActor); err != nil {
		t.Fatalf("RecordBoardTaskProtocol: %v", err)
	}
	fc.Advance(time.Second)
	if err := store.BeginRun(); err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if got := store.Snapshot().StartedAt; !got.Equal(marked) {
		t.Errorf("live StartedAt = %v, want the marker's %v", got, marked)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	loaded, err := LoadRunState(dir)
	if err != nil {
		t.Fatalf("LoadRunState: %v", err)
	}
	if !loaded.StartedAt.Equal(marked) {
		t.Errorf("LoadRunState StartedAt = %v, want the marker's %v", loaded.StartedAt, marked)
	}

	fc.Advance(time.Hour)
	reopened := openProtocolStore(t, dir)
	if got := reopened.Snapshot().StartedAt; !got.Equal(marked) {
		t.Errorf("StartedAt after reopen = %v, want the marker's %v", got, marked)
	}
}
