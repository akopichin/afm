package memorypipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- test helpers ----------------------------------------------------------

const validRulesMD = "# Project rules\n\n## Old Pattern\n\nDescription of the old pattern.\n"

const nonEmptyDatasetYAML = `project_level:
  - prompt: "p1"
    chosen: "c1"
    rejected: "r1"
    source: log
session_level: []
`

const emptyDatasetYAML = `project_level: []
session_level: []
`

// recordingRunner dispatches by AgentSpec.Kind to a caller-supplied behavior
// map, appending every invoked Kind (in order) to *calls. Any Kind not
// present in behavior fails the test immediately — this is how tests assert
// "aggregate/prioritize/update was never called".
func recordingRunner(t *testing.T, calls *[]string, behavior map[string]func(spec AgentSpec) error) AgentRunner {
	t.Helper()
	return func(_ context.Context, spec AgentSpec) error {
		*calls = append(*calls, spec.Kind)
		fn, ok := behavior[spec.Kind]
		if !ok {
			t.Fatalf("unexpected agent call for kind %q (spec=%+v)", spec.Kind, spec)
		}
		return fn(spec)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writeFile(%s): %v", path, err)
	}
}

// --- TestDistillTarget_SeedsBeforeAggregate_NoHighKeepsCandidate -----------

func TestDistillTarget_SeedsBeforeAggregate_NoHighKeepsCandidate(t *testing.T) {
	dir := t.TempDir()
	seed := filepath.Join(dir, "seed.md")
	writeFile(t, seed, validRulesMD)

	dataset := filepath.Join(dir, "reflect_dataset.yaml")
	writeFile(t, dataset, nonEmptyDatasetYAML)

	staging := filepath.Join(dir, "staging")

	var calls []string
	runner := recordingRunner(t, &calls, map[string]func(spec AgentSpec) error{
		KindAggregate: func(spec AgentSpec) error {
			return os.WriteFile(spec.Out, []byte("1. Some Pattern — a description\n"), 0644)
		},
		KindPrioritize: func(spec AgentSpec) error {
			// No "## High" section at all -> SelectHigh returns "".
			return os.WriteFile(spec.Out, []byte("## Medium\n\n1. Some Pattern — a description\n\n## Low\n"), 0644)
		},
	})

	p := New(Prompts{}, AgentConfig{}, WithRunner(runner))
	art, err := p.DistillTarget(context.Background(), DistillTarget{
		Name:       "memory.md",
		Datasets:   []string{dataset},
		StagingDir: staging,
		SeedFrom:   seed,
		MaxRules:   25,
	})
	if err != nil {
		t.Fatalf("DistillTarget: %v", err)
	}
	if !art.NoHigh {
		t.Error("NoHigh = false, want true (prioritize produced no High section)")
	}
	for _, kind := range calls {
		if kind == KindUpdate {
			t.Errorf("update must not be called when NoHigh, calls=%v", calls)
		}
	}
	got, err := os.ReadFile(art.CandidatePath)
	if err != nil {
		t.Fatalf("read candidate: %v", err)
	}
	if string(got) != validRulesMD {
		t.Errorf("candidate = %q, want unchanged seed %q", got, validRulesMD)
	}
}

// --- TestDistillTarget_EmptyDatasetsNoOp -----------------------------------

func TestDistillTarget_EmptyDatasetsNoOp(t *testing.T) {
	dir := t.TempDir()
	seed := filepath.Join(dir, "seed.md")
	writeFile(t, seed, validRulesMD)

	ds1 := filepath.Join(dir, "s1.reflect_dataset.yaml")
	ds2 := filepath.Join(dir, "s2.reflect_dataset.yaml")
	writeFile(t, ds1, emptyDatasetYAML)
	writeFile(t, ds2, emptyDatasetYAML)

	staging := filepath.Join(dir, "staging")

	var calls []string
	// Empty behavior map: ANY call fails the test (review #11 — no
	// aggregate/prioritize/update call for all-empty datasets).
	runner := recordingRunner(t, &calls, map[string]func(spec AgentSpec) error{})

	p := New(Prompts{}, AgentConfig{}, WithRunner(runner))
	art, err := p.DistillTarget(context.Background(), DistillTarget{
		Name:       "memory.md",
		Datasets:   []string{ds1, ds2},
		StagingDir: staging,
		SeedFrom:   seed,
		MaxRules:   25,
	})
	if err != nil {
		t.Fatalf("DistillTarget: %v", err)
	}
	if !art.NoHigh {
		t.Error("NoHigh = false, want true")
	}
	if len(calls) != 0 {
		t.Errorf("calls = %v, want none", calls)
	}
	got, err := os.ReadFile(art.CandidatePath)
	if err != nil {
		t.Fatalf("read candidate: %v", err)
	}
	if string(got) != validRulesMD {
		t.Errorf("candidate = %q, want unchanged seed %q", got, validRulesMD)
	}
}

// --- TestDistillTarget_ChainOrderAndStaging --------------------------------

func TestDistillTarget_ChainOrderAndStaging(t *testing.T) {
	dir := t.TempDir()
	seed := filepath.Join(dir, "seed.md")
	writeFile(t, seed, validRulesMD)

	dataset := filepath.Join(dir, "reflect_dataset.yaml")
	writeFile(t, dataset, nonEmptyDatasetYAML)

	staging := filepath.Join(dir, "staging")

	const newRulesMD = "# Project rules\n\n## New Pattern\n\nMerged description.\n"

	var calls []string
	var updateTargetFile string
	runner := recordingRunner(t, &calls, map[string]func(spec AgentSpec) error{
		KindAggregate: func(spec AgentSpec) error {
			if len(spec.InPaths) != 1 || spec.InPaths[0] != dataset {
				t.Errorf("aggregate InPaths = %v, want [%s]", spec.InPaths, dataset)
			}
			return os.WriteFile(spec.Out, []byte("1. New Pattern — merged\n"), 0644)
		},
		KindPrioritize: func(spec AgentSpec) error {
			return os.WriteFile(spec.Out, []byte("## High\n\n1. New Pattern — merged\n\n## Medium\n\n## Low\n"), 0644)
		},
		KindUpdate: func(spec AgentSpec) error {
			updateTargetFile = spec.TargetFile
			return os.WriteFile(spec.TargetFile, []byte(newRulesMD), 0644)
		},
	})

	p := New(Prompts{}, AgentConfig{}, WithRunner(runner))
	art, err := p.DistillTarget(context.Background(), DistillTarget{
		Name:       "memory.md",
		Datasets:   []string{dataset},
		StagingDir: staging,
		SeedFrom:   seed,
		MaxRules:   25,
	})
	if err != nil {
		t.Fatalf("DistillTarget: %v", err)
	}
	wantOrder := []string{KindAggregate, KindPrioritize, KindUpdate}
	if len(calls) != len(wantOrder) {
		t.Fatalf("calls = %v, want %v", calls, wantOrder)
	}
	for i, want := range wantOrder {
		if calls[i] != want {
			t.Errorf("calls[%d] = %q, want %q (order=%v)", i, calls[i], want, calls)
		}
	}
	if updateTargetFile != art.CandidatePath {
		t.Errorf("update TargetFile = %q, want CandidatePath %q", updateTargetFile, art.CandidatePath)
	}
	if updateTargetFile == seed {
		t.Error("update must never target SeedFrom directly")
	}
	if art.NoHigh {
		t.Error("NoHigh = true, want false")
	}
	got, err := os.ReadFile(art.CandidatePath)
	if err != nil {
		t.Fatalf("read candidate: %v", err)
	}
	if string(got) != newRulesMD {
		t.Errorf("candidate = %q, want %q", got, newRulesMD)
	}
}

// --- TestDistillTarget_AggregateWritesNothingRejected ----------------------

func TestDistillTarget_AggregateWritesNothingRejected(t *testing.T) {
	dir := t.TempDir()
	seed := filepath.Join(dir, "seed.md")
	writeFile(t, seed, validRulesMD)

	dataset := filepath.Join(dir, "reflect_dataset.yaml")
	writeFile(t, dataset, nonEmptyDatasetYAML)

	staging := filepath.Join(dir, "staging")
	if err := os.MkdirAll(staging, 0755); err != nil {
		t.Fatal(err)
	}
	// Pre-place a stale patterns.md from a hypothetical earlier attempt.
	stalePatterns := filepath.Join(staging, "patterns.md")
	writeFile(t, stalePatterns, "1. Stale Pattern — leftover\n")

	var calls []string
	runner := recordingRunner(t, &calls, map[string]func(spec AgentSpec) error{
		KindAggregate: func(_ AgentSpec) error {
			return nil // writes nothing — the bug this test guards against
		},
	})

	p := New(Prompts{}, AgentConfig{}, WithRunner(runner))
	_, err := p.DistillTarget(context.Background(), DistillTarget{
		Name:       "memory.md",
		Datasets:   []string{dataset},
		StagingDir: staging,
		SeedFrom:   seed,
		MaxRules:   25,
	})
	if err == nil {
		t.Fatal("expected an error when aggregate writes nothing")
	}
	var stepErr *StepError
	if !errors.As(err, &stepErr) {
		t.Fatalf("error is not *StepError: %v", err)
	}
	if stepErr.Step != KindAggregate {
		t.Errorf("StepError.Step = %q, want %q", stepErr.Step, KindAggregate)
	}
	if _, statErr := os.Stat(stalePatterns); !os.IsNotExist(statErr) {
		t.Errorf("stale patterns.md should have been removed before the aggregate call, statErr=%v", statErr)
	}
}

// --- TestDistillTarget_UpdateSymlinkCandidateRejected ----------------------

func TestDistillTarget_UpdateSymlinkCandidateRejected(t *testing.T) {
	dir := t.TempDir()
	seed := filepath.Join(dir, "seed.md")
	writeFile(t, seed, validRulesMD)

	dataset := filepath.Join(dir, "reflect_dataset.yaml")
	writeFile(t, dataset, nonEmptyDatasetYAML)

	staging := filepath.Join(dir, "staging")

	elsewhere := filepath.Join(dir, "elsewhere.md")
	writeFile(t, elsewhere, "not the real candidate\n")

	var calls []string
	runner := recordingRunner(t, &calls, map[string]func(spec AgentSpec) error{
		KindAggregate: func(spec AgentSpec) error {
			return os.WriteFile(spec.Out, []byte("1. Pattern — desc\n"), 0644)
		},
		KindPrioritize: func(spec AgentSpec) error {
			return os.WriteFile(spec.Out, []byte("## High\n\n1. Pattern — desc\n"), 0644)
		},
		KindUpdate: func(spec AgentSpec) error {
			// Replace the candidate with a symlink instead of writing to it.
			if err := os.Remove(spec.TargetFile); err != nil && !os.IsNotExist(err) {
				return err
			}
			return os.Symlink(elsewhere, spec.TargetFile)
		},
	})

	p := New(Prompts{}, AgentConfig{}, WithRunner(runner))
	_, err := p.DistillTarget(context.Background(), DistillTarget{
		Name:       "memory.md",
		Datasets:   []string{dataset},
		StagingDir: staging,
		SeedFrom:   seed,
		MaxRules:   25,
	})
	if err == nil {
		t.Fatal("expected an error when update swaps the candidate for a symlink")
	}
	var stepErr *StepError
	if !errors.As(err, &stepErr) {
		t.Fatalf("error is not *StepError: %v", err)
	}
	if stepErr.Step != KindUpdate {
		t.Errorf("StepError.Step = %q, want %q", stepErr.Step, KindUpdate)
	}
}

// --- TestDistillTarget_DatasetReadErrorSurfaces ----------------------------

func TestDistillTarget_DatasetReadErrorSurfaces(t *testing.T) {
	dir := t.TempDir()
	seed := filepath.Join(dir, "seed.md")
	writeFile(t, seed, validRulesMD)

	missingDataset := filepath.Join(dir, "does-not-exist.yaml")
	staging := filepath.Join(dir, "staging")

	var calls []string
	// No behavior registered: any agent call fails the test — the dataset
	// read error must short-circuit before aggregate is ever invoked.
	runner := recordingRunner(t, &calls, map[string]func(spec AgentSpec) error{})

	p := New(Prompts{}, AgentConfig{}, WithRunner(runner))
	art, err := p.DistillTarget(context.Background(), DistillTarget{
		Name:       "memory.md",
		Datasets:   []string{missingDataset},
		StagingDir: staging,
		SeedFrom:   seed,
		MaxRules:   25,
	})
	if err == nil {
		t.Fatal("expected an error for an unreadable dataset")
	}
	if len(calls) != 0 {
		t.Errorf("calls = %v, want none (dataset read error must short-circuit before aggregate)", calls)
	}
	// Candidate must still have been seeded first (review #3 invariant holds
	// even on the error path).
	got, readErr := os.ReadFile(art.CandidatePath)
	if readErr != nil {
		t.Fatalf("candidate must exist even on error path: %v", readErr)
	}
	if string(got) != validRulesMD {
		t.Errorf("candidate = %q, want seed %q", got, validRulesMD)
	}
}

// --- TestValidateRules ------------------------------------------------------

func TestValidateRules(t *testing.T) {
	tests := []struct {
		name     string
		md       string
		maxRules int
		wantErr  bool
	}{
		{
			name:     "valid single pattern",
			md:       "# Project rules\n\n## Pattern A\n\nDescription.\n",
			maxRules: 25,
			wantErr:  false,
		},
		{
			name:     "valid multiple patterns",
			md:       "# Project rules\n\n## Pattern A\n\nDesc A.\n\n## Pattern B\n\nDesc B.\n",
			maxRules: 25,
			wantErr:  false,
		},
		{
			name:     "missing header entirely",
			md:       "## Pattern A\n\nDescription.\n",
			maxRules: 25,
			wantErr:  true,
		},
		{
			name:     "header not first non-blank line",
			md:       "## Pattern A\n\n# Project rules\n\nDescription.\n",
			maxRules: 25,
			wantErr:  true,
		},
		{
			name:     "header with leading blank lines is fine",
			md:       "\n\n# Project rules\n\n## Pattern A\n\nDesc.\n",
			maxRules: 25,
			wantErr:  false,
		},
		{
			name:     "wrong header text",
			md:       "# project rules\n\n## Pattern A\n\nDesc.\n",
			maxRules: 25,
			wantErr:  true,
		},
		{
			name:     "extra H1 rejected",
			md:       "# Project rules\n\n## Pattern A\n\nDesc.\n\n# Another Heading\n\nMore.\n",
			maxRules: 25,
			wantErr:  true,
		},
		{
			name:     "no pattern blocks",
			md:       "# Project rules\n\nJust prose, no patterns.\n",
			maxRules: 25,
			wantErr:  true,
		},
		{
			name:     "tier heading High rejected",
			md:       "# Project rules\n\n## High\n\n## Pattern A\n\nDesc.\n",
			maxRules: 25,
			wantErr:  true,
		},
		{
			name:     "tier heading lowercase rejected (case-insensitive)",
			md:       "# Project rules\n\n## high\n\n## Pattern A\n\nDesc.\n",
			maxRules: 25,
			wantErr:  true,
		},
		{
			name:     "tier heading Medium rejected",
			md:       "# Project rules\n\n## Medium\n\n## Pattern A\n\nDesc.\n",
			maxRules: 25,
			wantErr:  true,
		},
		{
			name:     "tier heading Low rejected",
			md:       "# Project rules\n\n## Low\n\n## Pattern A\n\nDesc.\n",
			maxRules: 25,
			wantErr:  true,
		},
		{
			name:     "cap exceeded",
			md:       "# Project rules\n\n## A\n\n1.\n\n## B\n\n2.\n\n## C\n\n3.\n",
			maxRules: 2,
			wantErr:  true,
		},
		{
			name:     "cap of 0 means unbounded",
			md:       "# Project rules\n\n## A\n\n1.\n\n## B\n\n2.\n\n## C\n\n3.\n",
			maxRules: 0,
			wantErr:  false,
		},
		{
			name:     "cap exactly at limit is fine",
			md:       "# Project rules\n\n## A\n\n1.\n\n## B\n\n2.\n",
			maxRules: 2,
			wantErr:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRules(tt.md, tt.maxRules)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateRules(%q, %d) error = %v, wantErr %v", tt.md, tt.maxRules, err, tt.wantErr)
			}
		})
	}
}

func TestValidateRules_InvalidUTF8Rejected(t *testing.T) {
	bad := "# Project rules\n\n## Pattern A\n\n" + string([]byte{0xff, 0xfe}) + "\n"
	if err := ValidateRules(bad, 25); err == nil {
		t.Error("expected an error for invalid UTF-8")
	}
}

func TestValidateRules_OversizeRejected(t *testing.T) {
	var b strings.Builder
	b.WriteString("# Project rules\n\n## Pattern A\n\n")
	// Pad well past the 1<<20 bound.
	for b.Len() < (1<<20)+1024 {
		b.WriteString("filler filler filler filler filler filler filler filler\n")
	}
	if err := ValidateRules(b.String(), 25); err == nil {
		t.Error("expected an error for an oversize rules file")
	}
}
