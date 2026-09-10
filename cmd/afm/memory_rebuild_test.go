package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/memorypipeline"
	"github.com/akopichin/afm/pkg/state"
)

func TestMemoryRebuildCmd_Registered(t *testing.T) {
	c, _, err := newRootCmd().Find([]string{"memory", "rebuild"})
	if err != nil || c.Name() != "rebuild" {
		t.Fatalf("got %v %v", c, err)
	}
}

func TestMemoryRebuildCmd_CommitFlagsMutuallyExclusive(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"memory", "rebuild", "flow.yaml", "--commit", "--no-commit"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "exclusive") {
		t.Fatalf("want mutual-exclusion error, got %v", err)
	}
}

// TestParseCommitFlags_NoCommitFalseDoesNotForceNoCommit covers the
// symmetry fix: --commit already only forces a commit when its value is
// true (GetBool("commit")); --no-commit used to force Commit=false on bare
// Changed() alone, so an explicit "--no-commit=false" wrongly behaved like
// "--no-commit". It must now be a no-op, exactly like "--commit=false" is.
func TestParseCommitFlags_NoCommitFalseDoesNotForceNoCommit(t *testing.T) {
	cmd := newMemoryRebuildCmd()
	if err := cmd.Flags().Parse([]string{"--no-commit=false"}); err != nil {
		t.Fatal(err)
	}
	commitSet, commit, err := parseCommitFlags(cmd)
	if err != nil {
		t.Fatalf("parseCommitFlags: %v", err)
	}
	if commitSet {
		t.Error("commitSet = true, want false (--no-commit=false must be a no-op)")
	}
	if commit {
		t.Error("commit = true, want false")
	}
}

// TestParseCommitFlags_NoCommitTrueForcesNoCommit is the positive control:
// a bare --no-commit (value true) still forces no-commit as before.
func TestParseCommitFlags_NoCommitTrueForcesNoCommit(t *testing.T) {
	cmd := newMemoryRebuildCmd()
	if err := cmd.Flags().Parse([]string{"--no-commit"}); err != nil {
		t.Fatal(err)
	}
	commitSet, commit, err := parseCommitFlags(cmd)
	if err != nil {
		t.Fatalf("parseCommitFlags: %v", err)
	}
	if !commitSet {
		t.Error("commitSet = false, want true")
	}
	if commit {
		t.Error("commit = true, want false (no-commit forced)")
	}
}

// TestParseCommitFlags_MutualExclusionStillFires re-proves the mutual
// exclusion at the parseCommitFlags level (not just through the full
// command in TestMemoryRebuildCmd_CommitFlagsMutuallyExclusive above), for
// the bare "--commit --no-commit" combination.
func TestParseCommitFlags_MutualExclusionStillFires(t *testing.T) {
	cmd := newMemoryRebuildCmd()
	if err := cmd.Flags().Parse([]string{"--commit", "--no-commit"}); err != nil {
		t.Fatal(err)
	}
	_, _, err := parseCommitFlags(cmd)
	if err == nil || !strings.Contains(err.Error(), "exclusive") {
		t.Fatalf("want mutual-exclusion error, got %v", err)
	}
}

func TestResolveExplicitRunDir(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "flow-20260101-000000-aaaa"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveExplicitRunDir(base, "flow", "flow-20260101-000000-aaaa"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"../escape", "sub/flow-x", `flow-2026\escape`, "flow\\x", ".", "..", "/abs",
		"other-20260101-000000-aaaa", "flow-20260101-000000-zzzz",
	} {
		if _, err := resolveExplicitRunDir(base, "flow", bad); err == nil {
			t.Errorf("want error for %q", bad)
		}
	}
}

func TestResolveExplicitRunDir_RejectsSymlink(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(base, "flow-20260101-000000-aaaa")); err != nil {
		t.Skip("symlink unsupported")
	}
	if _, err := resolveExplicitRunDir(base, "flow", "flow-20260101-000000-aaaa"); err == nil {
		t.Fatal("symlinked run id must be rejected")
	}
}

// --- rebuildHandler integration test helpers -------------------------------

// nonEmptyDatasetYAML — валидный reflect_dataset.yaml (та же форма, что и
// pkg/memorypipeline's аналог), используется fakeMemoryRunner для KindReflect.
const nonEmptyDatasetYAML = `project_level:
  - prompt: "p1"
    chosen: "c1"
    rejected: "r1"
    source: log
session_level: []
`

// fakeMemoryRunner — стаб AgentRunner, покрывающий все четыре шага
// конвейера памяти (reflect/aggregate/prioritize/update) без запуска
// реального агента: тесты подменяют newRebuildPipeline на
// memorypipeline.New(p, a, memorypipeline.WithRunner(fakeMemoryRunner())).
func fakeMemoryRunner() memorypipeline.AgentRunner {
	return func(_ context.Context, spec memorypipeline.AgentSpec) error {
		switch spec.Kind {
		case memorypipeline.KindReflect:
			return os.WriteFile(spec.DatasetOut, []byte(nonEmptyDatasetYAML), 0644)
		case memorypipeline.KindAggregate:
			return os.WriteFile(spec.Out, []byte("1. Pattern — a description\n"), 0644)
		case memorypipeline.KindPrioritize:
			return os.WriteFile(spec.Out, []byte("## High\n\n1. Pattern — a description\n\n## Medium\n\n## Low\n"), 0644)
		case memorypipeline.KindUpdate:
			return os.WriteFile(spec.TargetFile, []byte("# Project rules\n\n## Pattern\n\nA description.\n"), 0644)
		default:
			return errors.New("fakeMemoryRunner: unexpected kind " + spec.Kind)
		}
	}
}

// isolateHome points $HOME at an empty temp dir for the duration of the
// test, so config.LoadFrom's global layer (~/.afm/config.yaml) can never
// leak the developer's real, possibly Docker-enabled, machine config into a
// rebuildHandler test (Task 13: rebuildHandler now actually reads
// cfg.Docker to decide whether to re-exec — before that it was loaded but
// never consulted, so a Docker-enabled global config was harmless here;
// now, on a dev machine with `docker.enabled: true` in ~/.afm/config.yaml,
// every test below would try to auth-check/re-exec into Docker instead of
// exercising the behavior under test).
func isolateHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

// withFakeMemoryPipeline overrides newRebuildPipeline for the duration of the
// test so rebuildHandler never spawns a real agent process; restores the
// original seam on cleanup (same pattern as TestFmDir's rootDir save/restore).
func withFakeMemoryPipeline(t *testing.T) {
	t.Helper()
	prev := newRebuildPipeline
	t.Cleanup(func() { newRebuildPipeline = prev })
	newRebuildPipeline = func(p memorypipeline.Prompts, a memorypipeline.AgentConfig) *memorypipeline.Pipeline {
		return memorypipeline.New(p, a, memorypipeline.WithRunner(fakeMemoryRunner()))
	}
}

// writeFlowFile writes yaml content to a flow file under dir and returns its
// absolute path.
func writeFlowFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// mkRunCLI creates a run directory under base with real FSM transitions
// (pending -> statuses[id]) for each given stage id, mirroring
// pkg/state's own mkRun test helper (unexported there, so reimplemented
// here against the exported state.Open/Apply API). Stages absent from
// statuses never appear in the log, exactly like a stage that never ran.
func mkRunCLI(t *testing.T, base, name string, statuses map[string]state.StageStatus) string {
	t.Helper()
	runDir := filepath.Join(base, name)
	ids := make([]string, 0, len(statuses))
	for id := range statuses {
		ids = append(ids, id)
	}
	store, err := state.Open(runDir, ids)
	if err != nil {
		t.Fatalf("mkRunCLI: Open: %v", err)
	}
	for id, status := range statuses {
		if status == state.StatusPending {
			continue
		}
		tr := state.Transition{StageID: id, From: state.StatusPending, To: status, Event: "test_setup"}
		if err := store.Apply(&tr); err != nil {
			t.Fatalf("mkRunCLI: Apply(%s -> %s): %v", id, status, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("mkRunCLI: Close: %v", err)
	}
	return runDir
}

// writeStageSession creates <runDir>/<stageID>/execution_summary.md — the
// minimal artifact SourceInventory.HasAgentSession() recognizes as a real
// agent session (see pkg/orchestrator/memory_integration_test.go's
// newMemoryIntegrationOrchestrator, same fixture shape).
func writeStageSession(t *testing.T, runDir, stageID string) {
	t.Helper()
	dir := filepath.Join(runDir, stageID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "execution_summary.md"), []byte("## Summary\ndone\n"), 0644); err != nil {
		t.Fatal(err)
	}
}

const singleStageFlowYAML = `name: testflow
memory:
  path: memory
stages:
  - id: s1
    name: Build
    agents: [auto]
    reflect:
      file: s1.md
      mode: rw
`

// --- Step 1: preflight gates, before any pipeline/agent is touched ---------

func TestRebuildHandler_MissingMemoryConfig(t *testing.T) {
	isolateHome(t)
	dir := chdirTemp(t)
	flowPath := writeFlowFile(t, dir, "flow.yaml", `name: testflow
stages:
  - id: s1
    name: Build
    agents: [auto]
`)
	err := rebuildHandler(context.Background(), rebuildOptions{FlowArg: flowPath})
	if err == nil || !strings.Contains(err.Error(), "no memory.path") {
		t.Fatalf("want 'no memory.path' error, got %v", err)
	}
}

func TestRebuildHandler_NoWritableTarget(t *testing.T) {
	isolateHome(t)
	dir := chdirTemp(t)
	// memory.mode:w set, but no stage declares reflect at all — review #12:
	// a bare mode:w is not "writable" without an actual write-reflect stage.
	flowPath := writeFlowFile(t, dir, "flow.yaml", `name: testflow
memory:
  path: memory
  mode: w
stages:
  - id: s1
    name: Build
    agents: [auto]
`)
	err := rebuildHandler(context.Background(), rebuildOptions{FlowArg: flowPath})
	if err == nil || !strings.Contains(err.Error(), "no writable memory target") {
		t.Fatalf("want 'no writable memory target' error, got %v", err)
	}
}

// --- stage-set mismatch (independent of AllDone, review #1) ----------------

func TestCheckStageSetMatchesExact_SortedMissingAndExtra(t *testing.T) {
	rs := state.RunState{Stages: map[string]state.StageState{
		"b": {Status: state.StatusDone},
		"z": {Status: state.StatusDone},
		"a": {Status: state.StatusDone},
	}}
	err := checkStageSetMatchesExact(rs, []string{"a", "b", "c", "d"})
	if err == nil {
		t.Fatal("expected mismatch error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "missing [c d]") || !strings.Contains(msg, "extra [z]") {
		t.Fatalf("expected sorted missing/extra lists, got: %s", msg)
	}
}

func TestRebuildHandler_StageSetMismatch(t *testing.T) {
	isolateHome(t)
	dir := chdirTemp(t)
	// current flow declares TWO stages (s1, s2); the historical run only
	// ever ran s1 — checkStageSetMatchesExact must catch this even though
	// resolveExplicitRunDir (unlike FindLatestCompletedRunDir) does not
	// filter by topology.
	flowPath := writeFlowFile(t, dir, "flow.yaml", `name: testflow
memory:
  path: memory
stages:
  - id: s1
    name: Build
    agents: [auto]
    reflect:
      file: s1.md
      mode: rw
  - id: s2
    name: Test
    agents: [auto]
`)
	runDir := mkRunCLI(t, filepath.Join(dir, ".afm", "runs"), "testflow-20260101-000000-aaaa",
		map[string]state.StageStatus{"s1": state.StatusDone})
	writeStageSession(t, runDir, "s1")

	err := rebuildHandler(context.Background(), rebuildOptions{
		FlowArg: flowPath, RunID: "testflow-20260101-000000-aaaa",
	})
	if err == nil {
		t.Fatal("expected stage-set mismatch error")
	}
	if !strings.Contains(err.Error(), "missing [s2]") {
		t.Fatalf("expected sorted missing list, got: %v", err)
	}
}

// --- explicit --run on an incomplete run (AllDone check) --------------------

func TestRebuildHandler_ExplicitRunIncomplete(t *testing.T) {
	isolateHome(t)
	dir := chdirTemp(t)
	flowPath := writeFlowFile(t, dir, "flow.yaml", singleStageFlowYAML)
	runDir := mkRunCLI(t, filepath.Join(dir, ".afm", "runs"), "testflow-20260101-000000-aaaa",
		map[string]state.StageStatus{"s1": state.StatusRunning})
	writeStageSession(t, runDir, "s1")

	err := rebuildHandler(context.Background(), rebuildOptions{
		FlowArg: flowPath, RunID: "testflow-20260101-000000-aaaa",
	})
	if err == nil || !strings.Contains(err.Error(), "not completed") {
		t.Fatalf("want 'not completed' error, got %v", err)
	}
}

// --- an active run (live afm process holding the flock) ---------------------

func TestRebuildHandler_ActiveRunLocked(t *testing.T) {
	isolateHome(t)
	dir := chdirTemp(t)
	flowPath := writeFlowFile(t, dir, "flow.yaml", singleStageFlowYAML)
	runDir := mkRunCLI(t, filepath.Join(dir, ".afm", "runs"), "testflow-20260101-000000-aaaa",
		map[string]state.StageStatus{"s1": state.StatusDone})
	writeStageSession(t, runDir, "s1")

	// Simulate a live `afm run` still holding the run's flock.
	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	defer store.Close()

	err = rebuildHandler(context.Background(), rebuildOptions{FlowArg: flowPath})
	if err == nil {
		t.Fatal("expected error while run is locked by another process")
	}
	if !strings.Contains(err.Error(), "run") || !strings.Contains(err.Error(), "active") {
		t.Errorf("expected friendly ErrRunLocked message, got: %v", err)
	}
}

// --- stale "promoting" manifest from a prior attempt: warning, not error ----

func TestRebuildHandler_StalePromotingManifestWarns(t *testing.T) {
	isolateHome(t)
	withFakeMemoryPipeline(t)
	dir := chdirTemp(t)
	flowPath := writeFlowFile(t, dir, "flow.yaml", singleStageFlowYAML)
	runDir := mkRunCLI(t, filepath.Join(dir, ".afm", "runs"), "testflow-20260101-000000-aaaa",
		map[string]state.StageStatus{"s1": state.StatusDone})
	writeStageSession(t, runDir, "s1")

	priorAttempt := filepath.Join(runDir, "memory-rebuild", "prior-attempt")
	if err := os.MkdirAll(priorAttempt, 0755); err != nil {
		t.Fatal(err)
	}
	priorManifest := filepath.Join(priorAttempt, "manifest.json")
	m := memorypipeline.NewRunningManifest(memorypipeline.OperationMeta{RunID: "testflow-20260101-000000-aaaa"})
	m.Status = memorypipeline.StatusPromoting
	if err := memorypipeline.WriteManifest(priorManifest, m); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if err := rebuildHandler(context.Background(), rebuildOptions{FlowArg: flowPath, DryRun: true}); err != nil {
			t.Fatalf("rebuildHandler: %v", err)
		}
	})
	if !strings.Contains(out, "unfinished promotion") {
		t.Fatalf("expected stale-promotion warning in stdout, got: %s", out)
	}
}

// --- headline guarantee: dry-run touches neither the FSM nor any target ----

func TestRebuildHandler_DryRun_NoFSMMutation(t *testing.T) {
	isolateHome(t)
	withFakeMemoryPipeline(t)
	dir := chdirTemp(t)
	flowPath := writeFlowFile(t, dir, "flow.yaml", singleStageFlowYAML)
	runDir := mkRunCLI(t, filepath.Join(dir, ".afm", "runs"), "testflow-20260101-000000-aaaa",
		map[string]state.StageStatus{"s1": state.StatusDone})
	writeStageSession(t, runDir, "s1")

	eventsPath := filepath.Join(runDir, "events.jsonl")
	statePath := filepath.Join(runDir, "state.json")
	beforeEvents, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	beforeState, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state.json: %v", err)
	}

	memDir := filepath.Join(dir, "memory")
	stageTarget := filepath.Join(memDir, "s1.md")
	projectTarget := filepath.Join(memDir, "memory.md")

	if err := rebuildHandler(context.Background(), rebuildOptions{FlowArg: flowPath, DryRun: true}); err != nil {
		t.Fatalf("rebuildHandler (dry-run): %v", err)
	}

	afterEvents, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read events.jsonl after: %v", err)
	}
	afterState, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state.json after: %v", err)
	}
	if !bytes.Equal(beforeEvents, afterEvents) {
		t.Errorf("events.jsonl mutated by dry-run rebuild:\nbefore=%q\nafter=%q", beforeEvents, afterEvents)
	}
	if !bytes.Equal(beforeState, afterState) {
		t.Errorf("state.json mutated by dry-run rebuild:\nbefore=%q\nafter=%q", beforeState, afterState)
	}

	if _, err := os.Stat(stageTarget); !os.IsNotExist(err) {
		t.Errorf("dry-run must not write the stage target %s (err=%v)", stageTarget, err)
	}
	if _, err := os.Stat(projectTarget); !os.IsNotExist(err) {
		t.Errorf("dry-run must not write the project target %s (err=%v)", projectTarget, err)
	}
}

// --- persistent --dir resolves runsDir/memory under it ----------------------

func TestRebuildHandler_PersistentDirResolvesRunsDir(t *testing.T) {
	isolateHome(t)
	withFakeMemoryPipeline(t)
	chdirTemp(t) // an unrelated cwd — the handler must NOT fall back to it
	custom := t.TempDir()

	prevRootDir := rootDir
	t.Cleanup(func() { rootDir = prevRootDir })
	rootDir = custom

	flowPath := writeFlowFile(t, custom, "flow.yaml", singleStageFlowYAML)
	runDir := mkRunCLI(t, filepath.Join(custom, ".afm", "runs"), "testflow-20260101-000000-aaaa",
		map[string]state.StageStatus{"s1": state.StatusDone})
	writeStageSession(t, runDir, "s1")

	if err := rebuildHandler(context.Background(), rebuildOptions{FlowArg: flowPath, DryRun: true}); err != nil {
		t.Fatalf("rebuildHandler: %v", err)
	}

	// The attempt's work dir must have been allocated under the CUSTOM
	// dir's run, proving runsDir()/resolveMemoryDir honored rootDir rather
	// than the process's actual cwd.
	matches, err := filepath.Glob(filepath.Join(runDir, "memory-rebuild", "*", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly one manifest under custom --dir run, got %v", matches)
	}
}

// --- Task 12: commit path hardening -----------------------------------

// initGitRepoCLI initializes a git repo in a fresh t.TempDir() with a local
// user.email/name configured, so commits work without touching global git
// config. Returns the repo root.
func initGitRepoCLI(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for _, args := range [][]string{{"init"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		c := exec.Command("git", args...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return repo
}

func TestEffectiveCommit_Matrix(t *testing.T) {
	commitTrue := &flow.Flow{Memory: flow.MemoryConfig{Commit: true}}
	commitFalse := &flow.Flow{Memory: flow.MemoryConfig{Commit: false}}

	cases := []struct {
		name string
		o    rebuildOptions
		f    *flow.Flow
		want bool
	}{
		{"--commit wins over config false", rebuildOptions{CommitSet: true, Commit: true}, commitFalse, true},
		{"--no-commit wins over config true", rebuildOptions{CommitSet: true, Commit: false}, commitTrue, false},
		{"no flag, config true", rebuildOptions{}, commitTrue, true},
		{"no flag, config false", rebuildOptions{}, commitFalse, false},
		{"dry-run forces false even with --commit", rebuildOptions{DryRun: true, CommitSet: true, Commit: true}, commitTrue, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := effectiveCommit(c.o, c.f); got != c.want {
				t.Errorf("effectiveCommit(%+v, commit=%v) = %v, want %v", c.o, c.f.Memory.Commit, got, c.want)
			}
		})
	}
}

func TestCommitPreflight_RejectsPreStagedChangesUnderMemDir(t *testing.T) {
	repo := initGitRepoCLI(t)
	memDir := filepath.Join(repo, "memory")
	if err := os.MkdirAll(memDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "memory.md"), []byte("# rules\n"), 0644); err != nil {
		t.Fatal(err)
	}
	addCmd := exec.Command("git", "-C", repo, "add", "memory")
	if out, err := addCmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v %s", err, out)
	}

	if err := commitPreflight(memDir); err == nil {
		t.Fatal("expected error for pre-staged changes under memDir")
	}
}

func TestCommitPreflight_ClonesCleanlyWhenNothingStaged(t *testing.T) {
	repo := initGitRepoCLI(t)
	memDir := filepath.Join(repo, "memory")
	if err := os.MkdirAll(memDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "memory.md"), []byte("# rules\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Not staged (no `git add`) -> preflight must pass.
	if err := commitPreflight(memDir); err != nil {
		t.Fatalf("commitPreflight: %v", err)
	}
}

func TestCommitPreflight_WorksWhenMemDirDoesNotExistYet(t *testing.T) {
	repo := initGitRepoCLI(t)
	memDir := filepath.Join(repo, "does", "not", "exist", "memory")

	if err := commitPreflight(memDir); err != nil {
		t.Fatalf("commitPreflight on a not-yet-created memDir: %v", err)
	}
	if _, err := os.Stat(memDir); !os.IsNotExist(err) {
		t.Fatal("commitPreflight must not create memDir")
	}
}

func TestCommitPreflight_NonGitAncestorIsNoOp(t *testing.T) {
	// Not inside any git repo at all -> preflight defers to the real commit
	// attempt to surface the error, not itself.
	dir := t.TempDir()
	memDir := filepath.Join(dir, "memory")
	if err := commitPreflight(memDir); err != nil {
		t.Fatalf("commitPreflight outside any repo should be a no-op, got: %v", err)
	}
}

// newAwaitingCommitManifest writes a manifest already parked at
// StatusAwaitingCommit (Finalize's terminal, non-"nonTerminalStatuses" exit
// point before the CLI's own commit decision) — the exact state maybeCommit
// receives it in from rebuildHandler.
func newAwaitingCommitManifest(t *testing.T, manifestPath, runID string) {
	t.Helper()
	m := memorypipeline.NewRunningManifest(memorypipeline.OperationMeta{RunID: runID})
	m.Status = memorypipeline.StatusAwaitingCommit
	if err := memorypipeline.WriteManifest(manifestPath, m); err != nil {
		t.Fatal(err)
	}
}

func TestMaybeCommit_FailureMarksManifestFailed(t *testing.T) {
	// memDir does not exist and is not a git repo -> CommitPaths' own `git
	// add -- <path>` fails immediately (a real error, not "no diff").
	dir := t.TempDir()
	memDir := filepath.Join(dir, "memory")
	manifestPath := filepath.Join(dir, "manifest.json")
	newAwaitingCommitManifest(t, manifestPath, "testflow-20260101-000000-aaaa")

	report := memorypipeline.Report{
		RunID: "testflow-20260101-000000-aaaa",
		Targets: []memorypipeline.TargetResult{
			{FinalPath: filepath.Join(memDir, "memory.md"), Changed: true, Published: true},
		},
	}

	err := maybeCommit(memDir, report, manifestPath)
	if err == nil || !strings.Contains(err.Error(), "memory files updated but commit failed") {
		t.Fatalf("want 'memory files updated but commit failed' error, got %v", err)
	}

	got, lerr := memorypipeline.LoadManifest(manifestPath)
	if lerr != nil {
		t.Fatalf("LoadManifest: %v", lerr)
	}
	// Carried-forward fix (Task 11 review): a commit failure must leave the
	// manifest terminally "failed" with the error recorded and FinishedAt
	// set — not stuck at "awaiting_commit", which the deferred
	// FinalizeManifestOnExit no-ops on (awaiting_commit is not in
	// nonTerminalStatuses).
	if got.Status != memorypipeline.StatusFailed {
		t.Fatalf("want manifest Status=%q, got %q", memorypipeline.StatusFailed, got.Status)
	}
	if len(got.Errors) == 0 {
		t.Fatal("want the commit error appended to manifest Errors")
	}
	if got.FinishedAt == "" {
		t.Fatal("want manifest FinishedAt set on commit failure")
	}
}

func TestMaybeCommit_SuccessRecordsCommitCreatedAndSHA(t *testing.T) {
	repo := initGitRepoCLI(t)
	memDir := filepath.Join(repo, "memory")
	if err := os.MkdirAll(memDir, 0755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(memDir, "memory.md")
	if err := os.WriteFile(target, []byte("# rules\n"), 0644); err != nil {
		t.Fatal(err)
	}

	manifestPath := filepath.Join(t.TempDir(), "manifest.json")
	newAwaitingCommitManifest(t, manifestPath, "testflow-20260101-000000-aaaa")

	report := memorypipeline.Report{
		RunID: "testflow-20260101-000000-aaaa",
		Targets: []memorypipeline.TargetResult{
			{FinalPath: target, Changed: true, Published: true},
			// An unpublished/unchanged target must not be swept into the
			// commit's pathspec.
			{FinalPath: filepath.Join(memDir, "other.md"), Changed: false, Published: false},
		},
	}

	if err := maybeCommit(memDir, report, manifestPath); err != nil {
		t.Fatalf("maybeCommit: %v", err)
	}

	got, err := memorypipeline.LoadManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if !got.CommitCreated {
		t.Fatal("want manifest CommitCreated=true")
	}
	if got.CommitSHA == "" {
		t.Fatal("want manifest CommitSHA populated")
	}
	if got.CommitError != "" {
		t.Fatalf("want no CommitError on success, got %q", got.CommitError)
	}
}

func TestMaybeCommit_NoPublishedChangesIsNoOp(t *testing.T) {
	repo := initGitRepoCLI(t)
	memDir := filepath.Join(repo, "memory")
	manifestPath := filepath.Join(t.TempDir(), "manifest.json")
	newAwaitingCommitManifest(t, manifestPath, "testflow-20260101-000000-aaaa")

	report := memorypipeline.Report{
		RunID: "testflow-20260101-000000-aaaa",
		Targets: []memorypipeline.TargetResult{
			{FinalPath: filepath.Join(memDir, "memory.md"), Changed: false, Published: false},
		},
	}

	if err := maybeCommit(memDir, report, manifestPath); err != nil {
		t.Fatalf("maybeCommit: %v", err)
	}
	got, err := memorypipeline.LoadManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if got.CommitCreated {
		t.Fatal("want CommitCreated=false when nothing was published")
	}
	if got.Status != memorypipeline.StatusAwaitingCommit {
		t.Fatalf("manifest status must be untouched when there's nothing to commit, got %q", got.Status)
	}
}
