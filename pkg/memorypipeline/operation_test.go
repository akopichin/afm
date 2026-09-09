package memorypipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testMeta() OperationMeta {
	return OperationMeta{
		RunID:           "flow-20260909-abcd",
		RunPath:         "/afm/.afm/runs/flow-20260909-abcd",
		FlowPath:        "/proj/flow.yaml",
		FlowSHA256:      "deadbeef",
		RootDir:         "/proj",
		MemoryDir:       "/proj/.afm-memory",
		MemoryMode:      "rw",
		MaxRules:        25,
		EffectiveCommit: true,
		PromptsDir:      "",
		PromptSHA256:    map[string]string{"reflect": "aaa", "aggregate": "bbb", "prioritize": "ccc", "update": "ddd"},
		StartedAt:       "2026-09-09T10:00:00Z",
	}
}

// --- WriteManifest / LoadManifest round trip -------------------------------

func TestWriteManifestLoadManifest_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")

	m := NewRunningManifest(testMeta())
	m.Datasets = []DatasetResult{{
		StageID:       "stage1",
		Source:        "generated",
		DatasetPath:   "/run/stage1/reflect_dataset.yaml",
		SourceFiles:   []string{"/run/stage1/autonomous.log"},
		SourceHashes:  []string{"hash1"},
		DatasetSHA256: "dshash",
		CanonicalPath: "/run/stage1/reflect_dataset.yaml",
		Published:     true,
	}}
	m.Targets = []TargetResult{{
		Label:         "memory.md",
		FinalPath:     "/proj/.afm-memory/memory.md",
		CandidatePath: "/tmp/candidate-memory.md",
		OldSHA256:     "old",
		NewSHA256:     "new",
		Changed:       true,
		NoHigh:        false,
		Published:     true,
		Diff:          "--- a\n+++ b\n",
	}}
	m.Errors = []string{"warning: something minor"}

	if err := WriteManifest(path, m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}

	got, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}

	if got.RunID != m.RunID || got.FlowPath != m.FlowPath || got.MaxRules != m.MaxRules {
		t.Errorf("OperationMeta fields did not round-trip: %+v", got.OperationMeta)
	}
	if got.Status != StatusRunning {
		t.Errorf("Status = %q, want %q", got.Status, StatusRunning)
	}
	if len(got.Datasets) != 1 || got.Datasets[0].StageID != "stage1" || !got.Datasets[0].Published {
		t.Errorf("Datasets did not round-trip: %+v", got.Datasets)
	}
	if len(got.Targets) != 1 || got.Targets[0].Label != "memory.md" || got.Targets[0].Diff == "" {
		t.Errorf("Targets did not round-trip: %+v", got.Targets)
	}
	if len(got.Errors) != 1 || got.Errors[0] != "warning: something minor" {
		t.Errorf("Errors did not round-trip: %+v", got.Errors)
	}
	if got.PromptSHA256["reflect"] != "aaa" {
		t.Errorf("PromptSHA256 did not round-trip: %+v", got.PromptSHA256)
	}
}

func TestWriteManifest_AtomicNoLeftoverTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")

	m := NewRunningManifest(testMeta())
	if err := WriteManifest(path, m); err != nil {
		t.Fatal(err)
	}
	if err := WriteManifest(path, m); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "tmp-") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

// --- StepError ---------------------------------------------------------

func TestStepError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("boom")
	e := &StepError{
		StageID: "stage1",
		Target:  "memory.md",
		Step:    KindReflect,
		LogPath: "/run/stage1/reflect.log",
		Err:     inner,
	}

	msg := e.Error()
	for _, want := range []string{"stage1", "memory.md", KindReflect, "/run/stage1/reflect.log", "boom"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Error() = %q, missing %q", msg, want)
		}
	}
	if !errors.Is(e, inner) {
		t.Error("errors.Is(e, inner) = false, want true (Unwrap must expose Err)")
	}
}

// --- NewUniqueAttemptDir -------------------------------------------------

func TestNewUniqueAttemptDir_CreatesMissingParent(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "memory-rebuild") // does not exist yet

	dir, err := NewUniqueAttemptDir(base, func() string { return "attempt-1" })
	if err != nil {
		t.Fatalf("NewUniqueAttemptDir: %v", err)
	}
	if dir != filepath.Join(base, "attempt-1") {
		t.Errorf("dir = %q, want %q", dir, filepath.Join(base, "attempt-1"))
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Errorf("attempt dir was not created: %v", err)
	}
}

func TestNewUniqueAttemptDir_RetriesOnCollision(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "id-1"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(base, "id-2"), 0755); err != nil {
		t.Fatal(err)
	}

	ids := []string{"id-1", "id-2", "id-3"}
	i := 0
	gen := func() string {
		id := ids[i]
		i++
		return id
	}

	dir, err := NewUniqueAttemptDir(base, gen)
	if err != nil {
		t.Fatalf("NewUniqueAttemptDir: %v", err)
	}
	if dir != filepath.Join(base, "id-3") {
		t.Errorf("dir = %q, want %q (should have skipped colliding id-1/id-2)", dir, filepath.Join(base, "id-3"))
	}
}

func TestNewUniqueAttemptDir_ExhaustsRetries(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "stuck"), 0755); err != nil {
		t.Fatal(err)
	}

	_, err := NewUniqueAttemptDir(base, func() string { return "stuck" })
	if err == nil {
		t.Fatal("expected an error when gen() always collides")
	}
}

// --- ScanUnfinishedPromotions -------------------------------------------

func writeTestManifest(t *testing.T, path, status string) {
	t.Helper()
	m := NewRunningManifest(testMeta())
	m.Status = status
	if err := WriteManifest(path, m); err != nil {
		t.Fatal(err)
	}
}

func TestScanUnfinishedPromotions_FindsOnlyPromoting(t *testing.T) {
	runDir := t.TempDir()
	base := filepath.Join(runDir, "memory-rebuild")

	promotingDir := filepath.Join(base, "attempt-promoting")
	completedDir := filepath.Join(base, "attempt-completed")
	noManifestDir := filepath.Join(base, "attempt-no-manifest")
	for _, d := range []string{promotingDir, completedDir, noManifestDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	writeTestManifest(t, filepath.Join(promotingDir, "manifest.json"), StatusPromoting)
	writeTestManifest(t, filepath.Join(completedDir, "manifest.json"), StatusCompleted)

	got := ScanUnfinishedPromotions(runDir)
	if len(got) != 1 || got[0] != promotingDir {
		t.Errorf("ScanUnfinishedPromotions = %v, want [%s]", got, promotingDir)
	}
}

func TestScanUnfinishedPromotions_NoAttemptsReturnsEmpty(t *testing.T) {
	runDir := t.TempDir()
	got := ScanUnfinishedPromotions(runDir)
	if len(got) != 0 {
		t.Errorf("ScanUnfinishedPromotions = %v, want empty", got)
	}
}

// --- manifest lifecycle helpers ------------------------------------------

func TestNewRunningManifest_StatusAndMeta(t *testing.T) {
	meta := testMeta()
	m := NewRunningManifest(meta)
	if m.Status != StatusRunning {
		t.Errorf("Status = %q, want %q", m.Status, StatusRunning)
	}
	if m.RunID != meta.RunID || m.StartedAt != meta.StartedAt {
		t.Errorf("meta not copied into manifest: %+v", m.OperationMeta)
	}
	if m.FinishedAt != "" {
		t.Errorf("FinishedAt should be empty for a fresh running manifest, got %q", m.FinishedAt)
	}
}

func TestMarkManifestCompleted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	writeTestManifest(t, path, StatusAwaitingCommit)

	if err := MarkManifestCompleted(path); err != nil {
		t.Fatalf("MarkManifestCompleted: %v", err)
	}

	got, err := LoadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusCompleted {
		t.Errorf("Status = %q, want %q", got.Status, StatusCompleted)
	}
	if _, err := time.Parse(time.RFC3339, got.FinishedAt); err != nil {
		t.Errorf("FinishedAt = %q is not RFC3339: %v", got.FinishedAt, err)
	}
}

func TestFinalizeManifestOnExit_CancelledWhenCtxCancelled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	writeTestManifest(t, path, StatusDistilling)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var finalErr error
	FinalizeManifestOnExit(path, &finalErr, ctx)

	got, err := LoadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusCancelled {
		t.Errorf("Status = %q, want %q", got.Status, StatusCancelled)
	}
	if _, err := time.Parse(time.RFC3339, got.FinishedAt); err != nil {
		t.Errorf("FinishedAt = %q is not RFC3339: %v", got.FinishedAt, err)
	}
}

func TestFinalizeManifestOnExit_FailedWhenCtxLiveAndErrSet(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	writeTestManifest(t, path, StatusCapturing)

	ctx := context.Background()
	finalErr := errors.New("something exploded")

	FinalizeManifestOnExit(path, &finalErr, ctx)

	got, err := LoadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusFailed {
		t.Errorf("Status = %q, want %q", got.Status, StatusFailed)
	}
	found := false
	for _, e := range got.Errors {
		if strings.Contains(e, "something exploded") {
			found = true
		}
	}
	if !found {
		t.Errorf("Errors = %v, want an entry mentioning %q", got.Errors, "something exploded")
	}
}

func TestFinalizeManifestOnExit_NoOpWhenAlreadyTerminal(t *testing.T) {
	for _, status := range []string{StatusPublished, StatusCompleted, StatusDryRun, StatusFailed, StatusCancelled, StatusAwaitingCommit} {
		dir := t.TempDir()
		path := filepath.Join(dir, "manifest.json")
		writeTestManifest(t, path, status)

		ctx := context.Background()
		var finalErr error
		FinalizeManifestOnExit(path, &finalErr, ctx)

		got, err := LoadManifest(path)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != status {
			t.Errorf("status %q was mutated to %q, should be left alone", status, got.Status)
		}
		if got.FinishedAt != "" {
			t.Errorf("FinishedAt should stay empty for an already-terminal manifest, got %q", got.FinishedAt)
		}
	}
}

func TestFinalizeManifestOnExit_NonTerminalPromoting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	writeTestManifest(t, path, StatusPromoting)

	ctx := context.Background()
	var finalErr error
	FinalizeManifestOnExit(path, &finalErr, ctx)

	got, err := LoadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusFailed {
		t.Errorf("promoting must be treated as non-terminal: Status = %q, want %q", got.Status, StatusFailed)
	}
}
