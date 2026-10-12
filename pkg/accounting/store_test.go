package accounting

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// writeUsageFixture appends a valid record and an unterminated JSON tail to model a torn write.
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

// openTestStore opens a fresh Store over a temp dir with a plain (no config-override) Resolver — the
// common setup every CostSnapshot test needs.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	r := NewResolver(PricingConfig{})
	s, err := Open(t.TempDir(), r)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// unmeteredObs is an Observation for a process that produced no usable usage at all (Metered=false) —
// still one invocation, still attributable to a stage, just with nothing to price.
func unmeteredObs() Observation {
	return Observation{
		Metered: false,
		Reason:  ReasonMissingTerminal,
	}
}

// sameBundleIdentity reports whether both CostBundles were produced by the same CostSnapshot build, by
// comparing the Store revision each was built from.
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

func TestCostSnapshot_CallerMutatingNestedCostViewDoesNotCorrupt(t *testing.T) {
	s := openTestStore(t)
	if err := s.Append(obsGLM(), "backend", "implementation", ""); err != nil {
		t.Fatal(err)
	}

	b1 := s.CostSnapshot()
	stage := b1.Stages["backend"]
	if stage == nil {
		t.Fatal("expected a non-nil stage cost view")
	}
	if stage.CacheHitRatio == nil {
		t.Fatal("obsGLM has cache_read>0 — expected a non-nil CacheHitRatio")
	}

	stage.Phases["injected-phase"] = 999
	stage.Models = append(stage.Models, "injected-model")
	*stage.CacheHitRatio = -1

	b2 := s.CostSnapshot() // same revision — cache hit
	stage2 := b2.Stages["backend"]
	if _, ok := stage2.Phases["injected-phase"]; ok {
		t.Fatal("mutating a returned CostView.Phases corrupted a later cached snapshot")
	}
	for _, m := range stage2.Models {
		if m == "injected-model" {
			t.Fatal("mutating a returned CostView.Models corrupted a later cached snapshot")
		}
	}
	if stage2.CacheHitRatio == nil || *stage2.CacheHitRatio == -1 {
		t.Fatalf("mutating *CacheHitRatio corrupted a later cached snapshot: %+v", stage2.CacheHitRatio)
	}
}

func TestCostSnapshot_ColdBuildMatchesDirectLedgerAggregation(t *testing.T) {
	s := openTestStore(t)
	if err := s.Append(obsGLM(), "backend", "implementation", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(obsGLM(), "backend", "review", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(obsGLM(), "frontend", "implementation", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(obsMemory(), "", "memory_update", ScopeRunOverhead); err != nil {
		t.Fatal(err)
	}

	// Compute the expected byStage/run the old way, directly against the live ledger, for comparison against
	// what CostSnapshot (cache miss) actually built.
	wantByStage := s.ledger.SummaryByStage()
	wantRun := s.ledger.RunSummary()
	wantBundle := buildCostBundle(s.ledger.Records(), wantByStage, wantRun, s.unavailable, s.revision)

	got := s.CostSnapshot()

	if len(got.Stages) != len(wantBundle.Stages) {
		t.Fatalf("Stages length mismatch: got %d, want %d", len(got.Stages), len(wantBundle.Stages))
	}
	for id, wantView := range wantBundle.Stages {
		gotView, ok := got.Stages[id]
		if !ok {
			t.Fatalf("missing stage %q in CostSnapshot result", id)
		}
		if gotView.DisplayCost != wantView.DisplayCost || gotView.Coverage != wantView.Coverage {
			t.Fatalf("stage %q mismatch: got %+v, want %+v", id, gotView, wantView)
		}
	}
	if got.Run == nil || wantBundle.Run == nil || got.Run.DisplayCost != wantBundle.Run.DisplayCost {
		t.Fatalf("Run mismatch: got %+v, want %+v", got.Run, wantBundle.Run)
	}
	if got.Overhead == nil || wantBundle.Overhead == nil || got.Overhead.DisplayCost != wantBundle.Overhead.DisplayCost {
		t.Fatalf("Overhead mismatch: got %+v, want %+v", got.Overhead, wantBundle.Overhead)
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

func TestCostSnapshot_VerifyUsageAggregatesUnderParentStagePhaseWithoutDoubleCounting(t *testing.T) {
	s := openTestStore(t)
	if err := s.Append(obsGLM(), "build", "verify", ""); err != nil {
		t.Fatal(err)
	}

	view := s.CostSnapshot().Stages["build"]
	if view == nil {
		t.Fatal(`expected a CostView for stage "build"`)
	}
	if got := view.Phases["verify"]; got != 1 {
		t.Fatalf(`Phases["verify"] = %d, want 1`, got)
	}

	for i := range 3 {
		b := s.CostSnapshot()
		if got := b.Stages["build"].Phases["verify"]; got != 1 {
			t.Fatalf(`reload %d: Phases["verify"] = %d, want 1 (must not double-count)`, i, got)
		}
	}
}
