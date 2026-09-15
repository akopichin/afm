package accounting

import (
	"errors"
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

// TestOpenPreExistingCorruptFileReturnsUsableUnavailableStore is a
// regression test for the "never block FSM resume" contract: Open must
// return a USABLE, non-nil *Store together with a non-nil error when the
// pre-existing usage.jsonl has a corrupt interior line — a caller that did
// `if err != nil { return err }` and discarded the store would silently
// reintroduce a resume-blocking bug.
func TestOpenPreExistingCorruptFileReturnsUsableUnavailableStore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.jsonl")
	good := `{"record_version":1,"stage_id":"backend","phase":"implementation","metered":true,"priced":false,"tokens":{"output":1130}}` + "\n"
	bad := `{not valid json at all}` + "\n"
	original := good + bad
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	r := NewResolver(PricingConfig{})
	s, err := Open(dir, r)
	if err == nil {
		t.Fatal("expected Open to return a non-nil error for a pre-existing corrupt file")
	}
	if !errors.Is(err, ErrCorruptUsage) {
		t.Fatalf("expected err to be/wrap ErrCorruptUsage, got %v", err)
	}
	if s == nil {
		t.Fatal("Open must still return a usable *Store — discarding it on error would block FSM resume")
	}
	if !s.Unavailable() {
		t.Fatal("store opened over a corrupt file must start Unavailable")
	}

	// A subsequent Append must be a safe no-op: it must not panic, must
	// return an error, and must not touch the already-corrupt file further.
	if err := s.Append(obsGLM(), "backend", "implementation", ""); err == nil {
		t.Fatal("expected Append to fail on an unavailable store")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	after, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(after) != original {
		t.Fatal("Append on an unavailable store must never modify the file further")
	}
}

// openTestStore opens a fresh Store over a temp dir with a plain (no
// config-override) Resolver — the common setup every CostSnapshot test
// needs.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	r := NewResolver(PricingConfig{})
	s, err := Open(t.TempDir(), r)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// unmeteredObs is an Observation for a process that produced no usable
// usage at all (Metered=false) — still one invocation, still attributable
// to a stage, just with nothing to price.
func unmeteredObs() Observation {
	return Observation{
		Metered: false,
		Reason:  ReasonMissingTerminal,
	}
}

// sameBundleIdentity reports whether both CostBundles were produced by the
// same CostSnapshot build (i.e. the second call reused the cache instead of
// re-aggregating), by comparing the Store revision each was built from.
func sameBundleIdentity(a, b CostBundle) bool {
	return a.builtAt == b.builtAt
}

func TestCostSnapshot_RevisionCacheReusesBuild(t *testing.T) {
	s := openTestStore(t)
	if err := s.Append(obsGLM(), "backend", "implementation", ""); err != nil {
		t.Fatal(err)
	}
	b1 := s.CostSnapshot()
	b2 := s.CostSnapshot() // no Append between
	if !sameBundleIdentity(b1, b2) {
		t.Fatal("expected cached bundle reuse without rebuild")
	}

	if err := s.Append(obsGLM(), "backend", "review", ""); err != nil {
		t.Fatal(err)
	}
	b3 := s.CostSnapshot()
	if sameBundleIdentity(b2, b3) {
		t.Fatal("append must invalidate the cache")
	}
}

// TestCostSnapshot_CacheInvalidatesOnUnavailableLatch covers the other
// revision-bump trigger besides Append: a write failure that latches the
// store unavailable must also invalidate any cached bundle, so Health
// flips to HealthUnavailable on the very next CostSnapshot call.
func TestCostSnapshot_CacheInvalidatesOnUnavailableLatch(t *testing.T) {
	s := openTestStore(t)
	if err := s.Append(obsGLM(), "backend", "implementation", ""); err != nil {
		t.Fatal(err)
	}
	b1 := s.CostSnapshot()
	if b1.Health != HealthOK {
		t.Fatalf("expected HealthOK, got %v", b1.Health)
	}

	// Force the next write to fail, latching unavailable.
	if err := s.f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(obsGLM(), "backend", "implementation", ""); err == nil {
		t.Fatal("expected a write error once the file handle is closed")
	}

	b2 := s.CostSnapshot()
	if sameBundleIdentity(b1, b2) {
		t.Fatal("marking unavailable must invalidate the cache")
	}
	if b2.Health != HealthUnavailable {
		t.Fatalf("expected HealthUnavailable after latch, got %v", b2.Health)
	}
}

func TestCostSnapshot_AllUnmeteredIsData(t *testing.T) {
	s := openTestStore(t)
	if err := s.Append(unmeteredObs(), "backend", "implementation", ""); err != nil {
		t.Fatal(err)
	}
	b := s.CostSnapshot()
	if !b.HasData {
		t.Fatal("one record ⇒ has_data")
	}
	if b.Run == nil || b.Run.Coverage != string(CoverageNone) {
		t.Fatalf("run=%+v", b.Run)
	}
	if b.Stages["backend"] == nil {
		t.Fatal("stage-attributed record ⇒ non-nil stage cost")
	}
}

// TestCostSnapshot_NoDataYieldsNilRun covers the opposite edge: a Store with
// no records at all must report HasData=false and a nil Run, not a
// zero-value CostView masquerading as real coverage.
func TestCostSnapshot_NoDataYieldsNilRun(t *testing.T) {
	s := openTestStore(t)
	b := s.CostSnapshot()
	if b.HasData {
		t.Fatal("empty store must report HasData=false")
	}
	if b.Run != nil {
		t.Fatal("empty store must report a nil Run")
	}
	if b.Stages != nil {
		t.Fatal("empty store must report a nil Stages map")
	}
}

// TestCostSnapshot_CallerMutationDoesNotCorruptLaterSnapshot is the
// immutability guarantee: a caller that mutates the map/slice it got back
// from CostSnapshot must never affect what a later CostSnapshot call
// returns, cached or freshly built.
func TestCostSnapshot_CallerMutationDoesNotCorruptLaterSnapshot(t *testing.T) {
	s := openTestStore(t)
	if err := s.Append(obsGLM(), "backend", "implementation", ""); err != nil {
		t.Fatal(err)
	}

	b1 := s.CostSnapshot()
	b1.Stages["injected"] = &CostView{DisplayCost: "corrupted"}
	b1.Issues = append(b1.Issues, CoverageIssue{Kind: "corrupted"})

	b2 := s.CostSnapshot() // cache hit — must not see the mutation above
	if _, ok := b2.Stages["injected"]; ok {
		t.Fatal("mutating a returned Stages map corrupted a later cached snapshot")
	}
	if len(b2.Issues) != 0 {
		t.Fatalf("mutating a returned Issues slice corrupted a later cached snapshot: %+v", b2.Issues)
	}

	b2.Stages["injected-2"] = &CostView{DisplayCost: "corrupted"}
	if err := s.Append(obsGLM(), "backend", "review", ""); err != nil {
		t.Fatal(err)
	}
	b3 := s.CostSnapshot() // fresh rebuild — must not see the mutation above either
	if _, ok := b3.Stages["injected-2"]; ok {
		t.Fatal("mutating a returned Stages map corrupted a later rebuilt snapshot")
	}
}

func TestStaticUnavailable(t *testing.T) {
	p := StaticUnavailable()
	b := p.CostSnapshot()
	if b.Health != HealthUnavailable {
		t.Fatalf("expected HealthUnavailable, got %v", b.Health)
	}
	if b.HasData {
		t.Fatal("expected HasData=false")
	}
	if b.Run != nil || b.Stages != nil || b.Overhead != nil || b.Issues != nil {
		t.Fatalf("expected an entirely empty bundle, got %+v", b)
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
