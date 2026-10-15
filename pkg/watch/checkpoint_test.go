package watch

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCheckpoint_ContextDurableBeforeReferenceCommitted verifies Commit writes the context package file
// BEFORE the cursor file references it: if we stop reading right after the checkpoint file lands but before
// Commit returns, the cursor must not yet say "processed" for it.
func TestCheckpoint_ContextDurableBeforeReferenceCommitted(t *testing.T) {
	dir := t.TempDir()
	s := NewCheckpointStore(dir)

	if _, ok := s.Cursor(); ok {
		t.Fatal("a brand new store must report no cursor yet")
	}

	if err := s.Commit(Checkpoint{UpToSeq: 5, Content: "hello"}); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	content, ok := s.ReadCheckpoint(5)
	if !ok || content != "hello" {
		t.Fatalf("checkpoint context package missing or wrong: %q, ok=%v", content, ok)
	}
	upTo, ok := s.Cursor()
	if !ok || upTo != 5 {
		t.Fatalf("cursor after commit = %d,%v, want 5,true", upTo, ok)
	}

	// Simulate "the reference commit itself never happened" by writing JUST the checkpoint file to a fresh
	// store directory, with no cursor.json at all — Cursor() must say "not processed".
	dir2 := t.TempDir()
	s2 := NewCheckpointStore(dir2)
	if err := os.MkdirAll(dir2, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir2, "checkpoint-00000005.md"), []byte("orphaned"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.Cursor(); ok {
		t.Fatal("an orphaned checkpoint file with no cursor reference must not be treated as processed")
	}
}

// TestCheckpoint_CursorAdvancesMonotonically: committing a seq <= the current cursor must be rejected, never
// silently accepted or silently move the cursor backward.
func TestCheckpoint_CursorAdvancesMonotonically(t *testing.T) {
	s := NewCheckpointStore(t.TempDir())
	if err := s.Commit(Checkpoint{UpToSeq: 10, Content: "a"}); err != nil {
		t.Fatalf("first commit: %v", err)
	}
	if err := s.Commit(Checkpoint{UpToSeq: 10, Content: "b (same seq again)"}); err == nil {
		t.Fatal("committing the SAME seq twice must be rejected")
	}
	if err := s.Commit(Checkpoint{UpToSeq: 5, Content: "c (older seq)"}); err == nil {
		t.Fatal("committing an OLDER seq must be rejected")
	}
	upTo, _ := s.Cursor()
	if upTo != 10 {
		t.Fatalf("cursor moved despite rejected commits: %d", upTo)
	}
	if err := s.Commit(Checkpoint{UpToSeq: 11, Content: "d"}); err != nil {
		t.Fatalf("committing a strictly larger seq must succeed: %v", err)
	}
	upTo, _ = s.Cursor()
	if upTo != 11 {
		t.Fatalf("cursor after valid advance = %d, want 11", upTo)
	}
}

// TestCheckpoint_RestartDoesNotReprocessProcessedRange: reopening a CheckpointStore from the SAME directory
// (simulating a process restart) must see the durable cursor and refuse to recommit the already-processed
// range — the collector-level consumer uses this to seed "already shown up to N" after a crash.
func TestCheckpoint_RestartDoesNotReprocessProcessedRange(t *testing.T) {
	dir := t.TempDir()
	s1 := NewCheckpointStore(dir)
	if err := s1.Commit(Checkpoint{UpToSeq: 42, Content: "first lifetime"}); err != nil {
		t.Fatal(err)
	}

	// "Restart": a brand new CheckpointStore value, same directory.
	s2 := NewCheckpointStore(dir)
	upTo, ok := s2.Cursor()
	if !ok || upTo != 42 {
		t.Fatalf("restarted store cursor = %d,%v, want 42,true", upTo, ok)
	}
	if err := s2.Commit(Checkpoint{UpToSeq: 42, Content: "duplicate re-delivery after reconnect"}); err == nil {
		t.Fatal("restarted store must still refuse to reprocess the same already-committed range")
	}
	if err := s2.Commit(Checkpoint{UpToSeq: 60, Content: "genuinely new content"}); err != nil {
		t.Fatalf("a genuinely new range after restart must still commit: %v", err)
	}
}

// TestCheckpoint_MissingLogMeansNotVerified: Cursor() on a directory whose cursor.json is missing OR corrupt
// must report ok=false ("not verified"), never fabricate a position.
func TestCheckpoint_MissingLogMeansNotVerified(t *testing.T) {
	dir := t.TempDir()
	s := NewCheckpointStore(dir)
	if _, ok := s.Cursor(); ok {
		t.Fatal("missing cursor.json must report ok=false")
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, cursorFileName), []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Cursor(); ok {
		t.Fatal("corrupt cursor.json must report ok=false, never a fabricated position")
	}
}

// TestCheckpoint_StorageBounded: a checkpoint's context package is capped at MaxCheckpointBytes regardless of
// how much content a long, chatty stage run accumulated.
func TestCheckpoint_StorageBounded(t *testing.T) {
	dir := t.TempDir()
	s := NewCheckpointStore(dir)
	huge := make([]byte, MaxCheckpointBytes*3)
	for i := range huge {
		huge[i] = 'a'
	}
	if err := s.Commit(Checkpoint{UpToSeq: 1, Content: string(huge)}); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	content, ok := s.ReadCheckpoint(1)
	if !ok {
		t.Fatal("expected checkpoint to be readable")
	}
	if len(content) > MaxCheckpointBytes+200 { // small allowance for the truncation notice appended
		t.Fatalf("checkpoint content not bounded: got %d bytes", len(content))
	}
}

// TestDedup_AfterReconnectNoDuplicateResult: re-feeding the SAME observation range after a reconnect must not
// produce a second checkpoint — Commit's monotonic-cursor rejection IS the dedup guard the orchestrator relies
// on (see watch_tasks.go's fire path, which only ever calls Commit once per fired Bundle).
func TestDedup_AfterReconnectNoDuplicateResult(t *testing.T) {
	dir := t.TempDir()
	s := NewCheckpointStore(dir)
	if err := s.Commit(Checkpoint{UpToSeq: 7, Content: "result A"}); err != nil {
		t.Fatal(err)
	}
	// Reconnect: a NEW store instance over the SAME directory re-delivers the SAME bundle (e.g. a client that
	// replayed observations it had already shown once).
	s2 := NewCheckpointStore(dir)
	err := s2.Commit(Checkpoint{UpToSeq: 7, Content: "result A, redelivered"})
	if err == nil {
		t.Fatal("expected the redelivered checkpoint to be rejected as a duplicate")
	}
	content, _ := s2.ReadCheckpoint(7)
	if content != "result A" {
		t.Fatalf("the ORIGINAL checkpoint content must be preserved, got %q", content)
	}
}
