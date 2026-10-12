package memorypipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/akopichin/afm/pkg/memory"
)

// maxRulesBytes bounds generated Markdown; maxDatasetBytes separately bounds input reflect datasets.
const maxRulesBytes = 1 << 20

// DistillTarget defines datasets, staging paths, and the target for aggregate → prioritize → SelectHigh →
// update.
type DistillTarget struct {
	// Name identifies the target for logs/errors (e.g. "memory.md" or a
	// stage id) — it is NOT a filesystem path.
	Name string
	// Datasets — absolute paths to reflect_dataset.yaml files to aggregate.
	Datasets []string
	// StagingDir — a fresh, exclusive directory (e.g. under a memory-rebuild
	// attempt dir) where this run's intermediate/candidate files live.
	StagingDir string
	// SeedFrom — path whose bytes seed the staging candidate: the real target file or a prior stage's
	// candidate. May not exist yet (a brand-new target) — treated as empty.
	SeedFrom string
	// MaxRules caps the pattern-block count the "update" step is allowed to
	// leave in the candidate (0 = unbounded).
	MaxRules int
}

// StepArtifacts are the files one DistillTarget run produced under StagingDir.
type StepArtifacts struct {
	PatternsPath    string
	PrioritizedPath string
	HighPath        string
	CandidatePath   string
	// NoHigh is true when there was nothing to promote (all datasets empty, or prioritize produced no "##
	// High" section) — CandidatePath still exists and equals the SeedFrom bytes unchanged.
	NoHigh bool
}

// DistillTarget runs the staging aggregate -> prioritize -> SelectHigh -> update chain for one target.
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

// requireRegularCandidate proves the "update" step left the candidate file in a safe state: present, not a
// symlink, and a regular file.
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

// seedCandidate copies seedFrom's bytes to candidate. A missing seedFrom (a brand-new target with no prior
// file) is not an error — the candidate starts empty.
func seedCandidate(seedFrom, candidate string) error {
	data, err := os.ReadFile(seedFrom)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return memory.AtomicWrite(candidate, data)
}

// datasetsAllEmpty reports whether EVERY dataset in paths has empty project_level AND session_level
// sections.
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

// requireFreshFile proves an agent call actually produced output THIS call: the caller removes path before
// the call, so a present file must be new.
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
