package accounting

import (
	"os"
	"path/filepath"
	"testing"
)

// writeUsageFixture writes one full, valid usage.jsonl record line followed
// by a partial JSON fragment with NO trailing newline — a torn tail, as if a
// crash or a concurrent live writer caught the file mid-append.
func writeUsageFixture(t *testing.T, path string) {
	t.Helper()
	full := `{"record_version":1,"stage_id":"backend","phase":"implementation","metered":true,"priced":false,"tokens":{"uncached_input":48583,"cache_read":47616,"output":1130}}` + "\n"
	torn := `{"record_version":1,"stage_id":"backend","phase":"review","metered":tr`
	if err := os.WriteFile(path, []byte(full+torn), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestStoreAppendAndLoad(t *testing.T) {
	dir := t.TempDir()
	r := NewResolver(PricingConfig{})
	s, err := Open(dir, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(obsGLM(), "backend", "implementation", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	l, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if l.RunSummary().Tokens.Output != 1130 {
		t.Fatalf("reloaded output = %d", l.RunSummary().Tokens.Output)
	}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	l, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("missing usage.jsonl must be empty, not error: %v", err)
	}
	if l.RunSummary().Tokens.Total() != 0 {
		t.Fatal("expected empty")
	}
}

func TestLoadTornTailTolerated(t *testing.T) {
	dir := t.TempDir()
	// one valid line + a truncated line without newline
	writeUsageFixture(t, filepath.Join(dir, "usage.jsonl"))
	l, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if l.RunSummary().Tokens.Output == 0 {
		t.Fatal("valid line before torn tail must load")
	}
}

// TestLoadCorruptInteriorLineErrors covers the other half of the torn-tail
// contract: a COMPLETE (newline-terminated) line that fails to parse is a
// real corruption, not a benign torn tail, and must surface as
// ErrCorruptUsage without touching the file on disk.
func TestLoadCorruptInteriorLineErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.jsonl")
	good := `{"record_version":1,"stage_id":"backend","phase":"implementation","metered":true,"priced":false,"tokens":{"output":1130}}` + "\n"
	bad := `{not valid json at all}` + "\n"
	original := good + bad
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected ErrCorruptUsage for a complete unparseable interior line")
	}
	if err.Error() == "" {
		t.Fatal("expected a descriptive error")
	}

	after, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(after) != original {
		t.Fatal("Load must never modify the original file")
	}
}

func TestStoreUnavailableAfterWriteFailureStopsWrites(t *testing.T) {
	dir := t.TempDir()
	r := NewResolver(PricingConfig{})
	s, err := Open(dir, r)
	if err != nil {
		t.Fatal(err)
	}
	if s.Unavailable() {
		t.Fatal("fresh store must not start unavailable")
	}

	// Close the underlying file out from under the store to force the next
	// write to fail, simulating a real write/fsync error.
	if err := s.f.Close(); err != nil {
		t.Fatal(err)
	}

	if err := s.Append(obsGLM(), "backend", "implementation", ""); err == nil {
		t.Fatal("expected a write error once the file handle is closed")
	}
	if !s.Unavailable() {
		t.Fatal("store must be marked unavailable after a write error")
	}

	before := s.Snapshot().RunSummary().Tokens.Total()
	if err := s.Append(obsGLM(), "backend", "implementation", ""); err == nil {
		t.Fatal("expected Append to keep failing once unavailable")
	}
	after := s.Snapshot().RunSummary().Tokens.Total()
	if after != before {
		t.Fatal("no further writes should be applied to the ledger once unavailable")
	}
}
