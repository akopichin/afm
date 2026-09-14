package memorypipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/akopichin/afm/pkg/memory"
)

// maxRulesBytes bounds any Markdown a distill step writes under a target's
// StagingDir (patterns.md/prioritized.md/high.md/target.md) — distinct from
// maxDatasetBytes (dataset.go), which bounds the reflect_dataset.yaml that
// FEEDS this pipeline, not what it produces.
const maxRulesBytes = 1 << 20

// DistillTarget describes one staging distill run of the aggregate ->
// prioritize -> SelectHigh -> update chain: which reflect datasets to feed
// it, where to stage intermediate files, and which file seeds (and,
// eventually, is replaced by) the resulting candidate.
type DistillTarget struct {
	// Name identifies the target for logs/errors (e.g. "memory.md" or a
	// stage id) — it is NOT a filesystem path.
	Name string
	// Datasets — absolute paths to reflect_dataset.yaml files to aggregate.
	Datasets []string
	// StagingDir — a fresh, exclusive directory (e.g. under a memory-rebuild
	// attempt dir) where this run's intermediate/candidate files live.
	StagingDir string
	// SeedFrom — path whose bytes seed the staging candidate: the real
	// target file (if this is the only/first distill pass for it) or a
	// prior stage's candidate (if targets are chained). May not exist yet
	// (a brand-new target) — treated as empty.
	SeedFrom string
	// MaxRules caps the pattern-block count the "update" step is allowed to
	// leave in the candidate (0 = unbounded).
	MaxRules int
}

// StepArtifacts are the files one DistillTarget run produced under
// StagingDir. CandidatePath always exists after a successful run (seeded
// first, then possibly rewritten by "update") — callers never need to
// special-case NoHigh to find "the current best candidate".
type StepArtifacts struct {
	PatternsPath    string
	PrioritizedPath string
	HighPath        string
	CandidatePath   string
	// NoHigh is true when there was nothing to promote (all datasets empty,
	// or prioritize produced no "## High" section) — CandidatePath still
	// exists and equals the SeedFrom bytes unchanged.
	NoHigh bool
}

// DistillTarget runs the staging aggregate -> prioritize -> SelectHigh ->
// update chain for one target. The candidate file
// (<StagingDir>/target.md) is ALWAYS seeded from SeedFrom first — so on
// NoHigh (empty datasets, or prioritize producing no High section) the
// candidate still exists and equals the seed unchanged: a downstream
// consumer sharing this file never loses prior rules.
//
// Freshness: each agent call that must produce a NEW file (aggregate ->
// patterns.md, prioritize -> prioritized.md) has its target removed BEFORE
// the call and is verified fresh (requireFreshFile) AFTER — a stale
// leftover file from an earlier attempt, or an agent that silently writes
// nothing, is rejected rather than silently accepted. "update" is the
// exception: its output IS the seeded candidate, so it may legitimately
// leave the bytes unchanged; instead of demanding freshness we Lstat (reject
// a symlink swap) and ValidateRules the result.
func (p *Pipeline) DistillTarget(ctx context.Context, t DistillTarget) (StepArtifacts, error) {
	if err := os.MkdirAll(t.StagingDir, 0755); err != nil {
		return StepArtifacts{}, err
	}
	a := StepArtifacts{
		PatternsPath:    filepath.Join(t.StagingDir, "patterns.md"),
		PrioritizedPath: filepath.Join(t.StagingDir, "prioritized.md"),
		HighPath:        filepath.Join(t.StagingDir, "high.md"),
		CandidatePath:   filepath.Join(t.StagingDir, "target.md"),
	}

	if err := seedCandidate(t.SeedFrom, a.CandidatePath); err != nil {
		return a, err
	}

	empty, err := datasetsAllEmpty(t.Datasets)
	if err != nil {
		return a, &StepError{Target: t.Name, Step: KindAggregate, Err: err}
	}
	if empty {
		a.NoHigh = true
		return a, nil
	}

	_ = os.Remove(a.PatternsPath)
	if err := p.run(ctx, AgentSpec{
		Kind: KindAggregate, StageName: t.Name, Phase: PhaseAggregate, Scope: ScopeRunOverhead,
		InPaths: t.Datasets, Out: a.PatternsPath, LogFile: filepath.Join(t.StagingDir, "aggregate.log"),
	}); err != nil {
		return a, &StepError{Target: t.Name, Step: KindAggregate, LogPath: filepath.Join(t.StagingDir, "aggregate.log"), Err: err}
	}
	if err := requireFreshFile(a.PatternsPath, maxRulesBytes); err != nil {
		return a, &StepError{Target: t.Name, Step: KindAggregate, Err: err}
	}

	_ = os.Remove(a.PrioritizedPath)
	if err := p.run(ctx, AgentSpec{
		Kind: KindPrioritize, StageName: t.Name, Phase: PhasePrioritize, Scope: ScopeRunOverhead,
		In: a.PatternsPath, Out: a.PrioritizedPath, LogFile: filepath.Join(t.StagingDir, "prioritize.log"),
	}); err != nil {
		return a, &StepError{Target: t.Name, Step: KindPrioritize, LogPath: filepath.Join(t.StagingDir, "prioritize.log"), Err: err}
	}
	if err := requireFreshFile(a.PrioritizedPath, maxRulesBytes); err != nil {
		return a, &StepError{Target: t.Name, Step: KindPrioritize, Err: err}
	}

	pr, err := os.ReadFile(a.PrioritizedPath)
	if err != nil {
		return a, err
	}
	high := memory.SelectHigh(string(pr))
	if strings.TrimSpace(high) == "" {
		a.NoHigh = true
		return a, nil // candidate already == seed
	}
	if err := memory.AtomicWrite(a.HighPath, []byte(high)); err != nil {
		return a, err
	}

	if err := p.run(ctx, AgentSpec{
		Kind: KindUpdate, StageName: t.Name, Phase: PhaseUpdate, Scope: ScopeRunOverhead,
		HighPath: a.HighPath, TargetFile: a.CandidatePath, MaxRules: t.MaxRules,
		LogFile: filepath.Join(t.StagingDir, "update.log"),
	}); err != nil {
		return a, &StepError{Target: t.Name, Step: KindUpdate, LogPath: filepath.Join(t.StagingDir, "update.log"), Err: err}
	}

	// update's output IS the seeded candidate — do not remove-before; it may
	// legitimately be unchanged. Reject a symlink swap (Lstat, not Stat).
	if err := requireRegularCandidate(a.CandidatePath); err != nil {
		return a, &StepError{Target: t.Name, Step: KindUpdate, Err: err}
	}
	cand, err := os.ReadFile(a.CandidatePath)
	if err != nil {
		return a, &StepError{Target: t.Name, Step: KindUpdate, Err: err}
	}
	if err := ValidateRules(string(cand), t.MaxRules); err != nil {
		return a, &StepError{Target: t.Name, Step: KindUpdate, Err: err}
	}
	return a, nil
}

// requireRegularCandidate proves the "update" step left the candidate file in
// a safe state: present, not a symlink (Lstat, not Stat — a symlink swap must
// not be followed), and a regular file. Unlike requireFreshFile it tolerates
// the bytes being unchanged (update's output IS the seeded candidate). Each
// failure mode gets its own message — a bare Lstat error (missing/unreadable)
// never had a non-nil err to report when the cause was actually a symlink or
// an irregular file, which used to render as "...: <nil>".
func requireRegularCandidate(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("candidate %s missing: %w", filepath.Base(path), err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("candidate %s is a symlink", filepath.Base(path))
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("candidate %s is not a regular file", filepath.Base(path))
	}
	return nil
}

// seedCandidate copies seedFrom's bytes to candidate (atomically). A
// missing seedFrom (a brand-new target with no prior file) is not an error
// — the candidate starts empty. Containment of seedFrom under the memory
// dir is enforced by the caller (validateTargetUnderMemoryDir at the
// Finalize call site, under the memory lock) — this helper trusts its
// input path.
func seedCandidate(seedFrom, candidate string) error {
	data, err := os.ReadFile(seedFrom)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return memory.AtomicWrite(candidate, data)
}

// datasetsAllEmpty reports whether EVERY dataset in paths has empty
// project_level AND session_level sections. A read or parse error is
// surfaced to the caller as-is (never masked as "non-empty") — an unreadable
// dataset must fail the run, not silently skip straight to aggregate on
// whatever datasets did parse.
func datasetsAllEmpty(paths []string) (bool, error) {
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return false, err
		}
		ds, err := ParseDataset(b)
		if err != nil {
			return false, err
		}
		if len(ds.ProjectLevel) > 0 || len(ds.SessionLevel) > 0 {
			return false, nil
		}
	}
	return true, nil
}

// requireFreshFile proves an agent call actually produced output THIS call:
// the caller removes path before the call, so a present file must be new.
// Lstat (not Stat) so a symlink swap is rejected rather than followed; the
// file must be a non-empty regular file within maxBytes.
func requireFreshFile(path string, maxBytes int64) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("expected output %s: %w", filepath.Base(path), err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink", filepath.Base(path))
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("%s is empty or not a regular file", filepath.Base(path))
	}
	if info.Size() > maxBytes {
		return fmt.Errorf("%s exceeds size bound %d", filepath.Base(path), maxBytes)
	}
	return nil
}
