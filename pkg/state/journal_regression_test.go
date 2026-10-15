package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// This file (T2.8) re-proves the journal's pre-existing reliability guarantees (AGENTS.md "State
// persistence & run lifecycle") on a log that now contains control records interleaved with stage
// transitions — the new record format (T2.1) must not have weakened flock, non-destructive replay, durable
// snapshot, or storage-fatal handling.

// buildMixedLog opens a fresh store, applies one stage transition AND one control record (an
// ApplyRunOperation, which requires a policy to be recorded first — T2.6), then closes it. Returns the
// resulting events.jsonl path so the caller can corrupt/truncate it directly, exactly like the pre-T2.1
// torn-tail/corruption tests already do for legacy-only logs.
func buildMixedLog(t *testing.T) (dir string, eventsPath string) {
	t.Helper()
	dir = t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.Apply(&Transition{StageID: "a", From: StatusPending, To: StatusPlanning, Event: "start_planning"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	mustRecordPolicy(t, store)
	op := Operation{ID: "op-1", Kind: "create_children", Effects: []Effect{taskCreatedEffect(`{"profile_id":"p"}`, "t1", "a")}}
	if _, err := store.ApplyRunOperation(op, nil); err != nil {
		t.Fatalf("ApplyRunOperation: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return dir, filepath.Join(dir, "events.jsonl")
}

func TestTornTailTruncatedSafely(t *testing.T) {
	dir, eventsPath := buildMixedLog(t)

	orig, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	// Simulate a crash mid-write of a THIRD (torn) record, appended after the mixed legacy+control prefix.
	f, err := os.OpenFile(eventsPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	torn := `{"seq":3,"time":"2026-01-01T00:00:00Z","kind":"task_status_changed","schema_ver`
	if _, err := f.WriteString(torn); err != nil {
		t.Fatalf("write torn tail: %v", err)
	}
	f.Close()

	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open on torn tail after a mixed log: want success, got %v", err)
	}
	defer store.Close()

	if got := store.Get("a"); got != StatusPlanning {
		t.Fatalf("stage status after torn-tail replay = %q, want planning (the prior mixed prefix must still apply)", got)
	}
	if _, ok := store.Snapshot().Assistants.Tasks["t1"]; !ok {
		t.Fatal("task t1 (from the control record before the torn tail) is missing after replay")
	}

	after, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read events.jsonl after Open: %v", err)
	}
	if len(after) != len(orig) {
		t.Fatalf("events.jsonl size after Open = %d, want %d (truncated back to the last committed record)", len(after), len(orig))
	}
}

func TestCorruptCompleteLineQuarantined(t *testing.T) {
	dir, eventsPath := buildMixedLog(t)

	orig, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	// A COMPLETE but malformed line in the middle, with a valid record after it.
	corrupted := string(orig) + "NOT JSON AT ALL\n" +
		`{"seq":3,"time":"2026-01-01T00:00:00Z","stage_id":"a","from":"planning","to":"done","event":"complete"}` + "\n"
	if err := os.WriteFile(eventsPath, []byte(corrupted), 0644); err != nil {
		t.Fatalf("write corrupted log: %v", err)
	}

	_, err = Open(dir, []string{"a"})
	if !errors.Is(err, ErrCorruptLog) {
		t.Fatalf("Open on a mid-corrupted mixed log: want ErrCorruptLog, got %v", err)
	}

	after, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read events.jsonl after failed Open: %v", err)
	}
	if string(after) != corrupted {
		t.Fatal("original events.jsonl was modified despite the corruption — it must be left untouched")
	}
	matches, _ := filepath.Glob(eventsPath + ".corrupt-*")
	if len(matches) != 1 {
		t.Fatalf("want exactly 1 quarantine copy, got %d: %v", len(matches), matches)
	}
	quarantined, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read quarantine copy: %v", err)
	}
	if string(quarantined) != corrupted {
		t.Fatal("quarantine copy does not match the original corrupted content")
	}
}

func TestSnapshotWriteFailureNotFatal(t *testing.T) {
	dir, _ := buildMixedLog(t)

	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store.Close()

	if err := os.Mkdir(filepath.Join(dir, "state.json.tmp"), 0755); err != nil {
		t.Fatalf("mkdir state.json.tmp: %v", err)
	}

	// Both write paths (FSM-style Apply and the new AppendControl) must tolerate a snapshot write failure.
	if err := store.Apply(&Transition{StageID: "a", From: StatusPlanning, To: StatusAwaitingApproval, Event: "plan_ready"}); err != nil {
		t.Fatalf("Apply with a broken snapshot path: %v, want nil (snapshot failure must not be fatal)", err)
	}
	payload, err := json.Marshal(taskStatusChangedPayload{Status: "running"})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	policyCtl := &Control{Kind: "task_status_changed", SchemaVersion: ControlSchemaVersion, Target: Target{TaskID: "t1"}, Payload: payload}
	if err := store.AppendControl(policyCtl); err != nil {
		t.Fatalf("AppendControl with a broken snapshot path: %v, want nil", err)
	}

	// The authoritative log still advanced correctly despite the snapshot cache failing to write.
	if got := store.Get("a"); got != StatusAwaitingApproval {
		t.Fatalf("stage status = %q, want awaiting_approval", got)
	}
}

// TestStorageFatalEndsRun proves a real write failure on the NEW control-record path is reported as a
// distinct, typed *StorageError — never silently reinterpreted as a benign rejection (OperationRejectedError
// / ErrOperationIDConflict) that a caller might mistake for "nothing to do here" instead of "the run must
// end" (AGENTS.md: "Storage-fatal завершает ран").
func TestStorageFatalEndsRun(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { store.lock.Unlock() }() // eventsLog is closed below; avoid double-close via Store.Close()
	mustRecordPolicy(t, store)

	store.eventsLog.Close() // simulate a real OS-level write failure on the next record

	op := Operation{ID: "op-1", Kind: "create_children", Effects: []Effect{taskCreatedEffect(`{"profile_id":"p"}`, "t1", "a")}}
	_, err = store.ApplyRunOperation(op, nil)
	if err == nil {
		t.Fatal("ApplyRunOperation against a failed log file: want error, got nil")
	}
	var storageErr *StorageError
	if !errors.As(err, &storageErr) {
		t.Fatalf("err = %v (%T), want *StorageError", err, err)
	}
	var rej *OperationRejectedError
	if errors.As(err, &rej) {
		t.Fatalf("err is ALSO an *OperationRejectedError (%+v) — a storage-fatal failure must never be classified as a benign rejection", rej)
	}
	if errors.Is(err, ErrOperationIDConflict) {
		t.Fatal("err is ErrOperationIDConflict — a storage-fatal failure must not be mistaken for an idempotency conflict")
	}
}

// TestRunLockedForCliWhileRunActive proves CLI-style TryLockRun still correctly fails against an active run
// (ErrRunLocked) when that run's journal contains control records, not just legacy transitions — the flock
// itself is content-agnostic, but this guards against any accidental coupling introduced while adding the
// new write paths (e.g. a bug that closed/reopened the file handle per-record).
func TestRunLockedForCliWhileRunActive(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	mustRecordPolicy(t, store)
	op := Operation{ID: "op-1", Kind: "create_children", Effects: []Effect{taskCreatedEffect(`{"profile_id":"p"}`, "t1", "a")}}
	if _, err := store.ApplyRunOperation(op, nil); err != nil {
		t.Fatalf("ApplyRunOperation: %v", err)
	}

	if _, err := TryLockRun(dir); !errors.Is(err, ErrRunLocked) {
		t.Fatalf("TryLockRun against an active run with control records in its journal: want ErrRunLocked, got %v", err)
	}
	if _, err := Open(dir, []string{"a"}); !errors.Is(err, ErrRunLocked) {
		t.Fatalf("Open against an active run with control records in its journal: want ErrRunLocked, got %v", err)
	}
}
