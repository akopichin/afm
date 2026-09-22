package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/docker"
	"github.com/akopichin/afm/pkg/state"
)

func boolPtr(b bool) *bool { return &b }

// dockerDisabledConfig — a config.Config with Docker mode explicitly off
// (Enabled: false, not nil — nil would fall back to reading AFM_USE_DOCKER
// from the real environment, which we don't want in a unit test).
func dockerDisabledConfig() config.Config {
	cfg := config.Default()
	cfg.Docker.Enabled = boolPtr(false)
	return cfg
}

// --- gate: disabled / already-in-container -----------------------------

func TestRebuildDockerReExec_SkippedWhenDockerDisabled(t *testing.T) {
	docker.SetExecFunc(func(string, []string, []string) error {
		t.Fatal("execFunc must not be called when Docker mode is disabled")
		return nil
	})
	defer docker.ResetExecFunc()

	cfg := dockerDisabledConfig()
	dir := t.TempDir()
	if err := rebuildDockerReExec(cfg, rebuildOptions{}, filepath.Join(dir, "flow.yaml"), dir, filepath.Join(dir, "memory"), filepath.Join(dir, ".afm", "runs", "r1")); err != nil {
		t.Fatalf("expected nil (no-op) error, got %v", err)
	}
}

func TestRebuildDockerReExec_SkippedWhenInDocker(t *testing.T) {
	t.Setenv("AFM_IN_DOCKER", "1")
	docker.SetExecFunc(func(string, []string, []string) error {
		t.Fatal("execFunc must not be called when already inside the container")
		return nil
	})
	defer docker.ResetExecFunc()

	cfg := config.Default()
	cfg.Docker.Enabled = boolPtr(true) // even "enabled" — AFM_IN_DOCKER wins
	dir := t.TempDir()
	if err := rebuildDockerReExec(cfg, rebuildOptions{}, filepath.Join(dir, "flow.yaml"), dir, filepath.Join(dir, "memory"), filepath.Join(dir, ".afm", "runs", "r1")); err != nil {
		t.Fatalf("expected nil (no-op) error, got %v", err)
	}
}

// --- happy path: only the global command feeds mount/recipe computation -

// TestRebuildDockerReExec_OnlyGlobalCommandFeedsMounts проверяет, что
// rebuildDockerReExec монтирует ТОЛЬКО глобальную команду агента памяти
// (cfg.Client.Command) — rebuild не выполняет стадий флоу, поэтому нет
// *flow.Flow, из которого можно было бы (ошибочно) насканировать команды
// стадий; docker.ScanCommands/UsedRecipes/UsesCodex вызываются с nil-флоу
// (Task 13's nil-flow tolerance).
func TestRebuildDockerReExec_OnlyGlobalCommandFeedsMounts(t *testing.T) {
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "myagent")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Setenv("ANTHROPIC_API_KEY", "test-key") // CheckClaudeDockerAuth only gates "claude"

	var capturedArgs []string
	docker.SetExecFunc(func(_ string, argv []string, _ []string) error {
		capturedArgs = argv
		return nil
	})
	defer docker.ResetExecFunc()

	root := t.TempDir()
	cfg := config.Default()
	cfg.Docker.Enabled = boolPtr(true)
	cfg.Client.Command = "myagent"

	prevRootDir := rootDir
	t.Cleanup(func() { rootDir = prevRootDir })
	rootDir = root

	flowPath := filepath.Join(root, "flow.yaml")
	agentRoot := root
	memDir := filepath.Join(root, "memory")
	runDir := filepath.Join(root, ".afm", "runs", "r1")
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := rebuildDockerReExec(cfg, rebuildOptions{}, flowPath, agentRoot, memDir, runDir); err != nil {
		t.Fatalf("rebuildDockerReExec: %v", err)
	}

	joined := strings.Join(capturedArgs, " ")
	if !strings.Contains(joined, binPath+":/usr/local/bin/myagent:ro") {
		t.Errorf("expected myagent to be mounted, args: %v", capturedArgs)
	}
	if !strings.Contains(joined, "--dir="+root) {
		t.Errorf("expected absolute --dir=%s in args, got: %v", root, capturedArgs)
	}
	if !strings.Contains(joined, flowPath) {
		t.Errorf("expected the resolved flow path %q in args, got: %v", flowPath, capturedArgs)
	}
	if !strings.Contains(joined, "memory rebuild") {
		t.Errorf("expected the 'memory rebuild' subcommand in args, got: %v", capturedArgs)
	}
}

// TestRebuildDockerReExec_ClaudeCommandRequiresAuth — CheckClaudeDockerAuth
// is invoked for the (default) "claude" client command, same as run.go.
func TestRebuildDockerReExec_ClaudeCommandRequiresAuth(t *testing.T) {
	// Герметичность: preflight считает «auth есть» по любому непустому
	// CLAUDE_CODE_OAUTH_TOKEN/ANTHROPIC_* из окружения. В интерактивном shell
	// гонщика эти переменные экспортированы — без зачистки тест ложно падал
	// («execFunc must not be called»). Пустое значение для проверки
	// os.Getenv(key) != "" эквивалентно отсутствию переменной.
	for _, key := range config.ClaudeAuthEnvVars {
		t.Setenv(key, "")
	}
	docker.SetExecFunc(func(string, []string, []string) error {
		t.Fatal("execFunc must not be called when auth preflight fails")
		return nil
	})
	defer docker.ResetExecFunc()

	cfg := config.Default()
	cfg.Docker.Enabled = boolPtr(true)
	cfg.Client.Command = config.ClaudeCommand

	root := t.TempDir()
	prevRootDir := rootDir
	t.Cleanup(func() { rootDir = prevRootDir })
	rootDir = root

	err := rebuildDockerReExec(cfg, rebuildOptions{}, filepath.Join(root, "flow.yaml"), root, filepath.Join(root, "memory"), filepath.Join(root, ".afm", "runs", "r1"))
	if err == nil || !strings.Contains(err.Error(), "auth") {
		t.Fatalf("expected an auth error, got %v", err)
	}
}

// --- ExtraArgs reconstruction (review #14) ------------------------------

func TestRebuildDockerArgs_ReconstructsFromOptions(t *testing.T) {
	got := rebuildDockerArgs(rebuildOptions{
		RunID: "flow-20260101-000000-aaaa", DryRun: true, ForceReflect: true,
		CommitSet: true, Commit: false,
	}, "/abs/flow.yaml", "/abs/root")
	want := []string{"memory", "rebuild", "--run", "flow-20260101-000000-aaaa", "--dry-run", "--force-reflect", "--no-commit", "/abs/flow.yaml", "--dir=/abs/root"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestRebuildDockerArgs_CommitTrue(t *testing.T) {
	got := rebuildDockerArgs(rebuildOptions{CommitSet: true, Commit: true}, "/abs/flow.yaml", "/abs/root")
	want := []string{"memory", "rebuild", "--commit", "/abs/flow.yaml", "--dir=/abs/root"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestRebuildDockerArgs_MinimalOptions(t *testing.T) {
	got := rebuildDockerArgs(rebuildOptions{}, "/abs/flow.yaml", "/abs/root")
	want := []string{"memory", "rebuild", "/abs/flow.yaml", "--dir=/abs/root"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %v, want %v", got, want)
	}
}

// --- reachability preflight ----------------------------------------------

func TestRebuildDockerPreflight_AllUnderProjectDir(t *testing.T) {
	root := "/abs/root"
	err := rebuildDockerPreflight(root, nil, map[string]string{
		"run dir":    "/abs/root/.afm/runs/r1",
		"agent root": "/abs/root",
		"flow file":  "/abs/root/flow.yaml",
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestRebuildDockerPreflight_EmptyPathsSkipped(t *testing.T) {
	err := rebuildDockerPreflight("/abs/root", nil, map[string]string{
		"prompts dir": "", // cfg.PromptsDir == "" → embedded defaults, nothing to check
	})
	if err != nil {
		t.Fatalf("expected empty path to be skipped, got %v", err)
	}
}

// TestRebuildDockerPreflight_RelativePathUnderRootPasses reproduces the
// critical bug: on the default invocation rootDir defaults to "." and
// runDir = runsDir() = "<rootDir>/.afm/runs/<id>" is therefore RELATIVE,
// while the reachable roots (projectDir/extra_mounts) are always absolute —
// a raw string comparison between a relative and an absolute path always
// fails containment, breaking `afm memory rebuild` under Docker for the
// default case. rebuildDockerPreflight must normalize named paths to
// absolute (relative to the same cwd absRoot was resolved from) before the
// containment check.
func TestRebuildDockerPreflight_RelativePathUnderRootPasses(t *testing.T) {
	chdirTemp(t)
	// os.Getwd() (not the raw t.TempDir() string) is the correct baseline
	// here: filepath.Abs resolves a relative path against os.Getwd()
	// internally, and on macOS t.TempDir() returns a "/var/folders/..."
	// path that is itself a symlink to "/private/var/folders/..." — so
	// filepath.Abs(dir) and filepath.Abs(relRunDir) would disagree unless
	// both go through os.Getwd().
	absRoot, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relRunDir := filepath.Join(".afm", "runs", "flow-20260101-000000-aaaa")

	err = rebuildDockerPreflight(absRoot, nil, map[string]string{
		"run dir": relRunDir,
	})
	if err != nil {
		t.Fatalf("expected a relative path under the cwd-resolved root to pass, got %v", err)
	}
}

// TestRebuildDockerPreflight_RelativePathOutsideRootFails proves the Abs
// normalization does not just always pass: a relative path that resolves
// (against cwd) to somewhere outside the project root must still fail.
func TestRebuildDockerPreflight_RelativePathOutsideRootFails(t *testing.T) {
	chdirTemp(t)
	absRoot, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	err = rebuildDockerPreflight(absRoot, nil, map[string]string{
		"run dir": "../outside/.afm/runs/r1",
	})
	if err == nil {
		t.Fatal("expected an error for a relative path resolving outside the project root")
	}
	if !strings.Contains(err.Error(), "run dir") {
		t.Errorf("expected error to name the label, got: %v", err)
	}
}

func TestRebuildDockerPreflight_UnreachablePathFails(t *testing.T) {
	err := rebuildDockerPreflight("/abs/root", nil, map[string]string{
		"agent root": "/somewhere/else",
	})
	if err == nil {
		t.Fatal("expected an error for a path outside the project dir and any extra_mounts")
	}
	if !strings.Contains(err.Error(), "agent root") || !strings.Contains(err.Error(), "/somewhere/else") {
		t.Errorf("expected error to name the label and the path, got: %v", err)
	}
}

func TestRebuildDockerPreflight_CoveredByExtraMount(t *testing.T) {
	mounts := config.ExtraMounts{{Path: "/external/data"}}
	err := rebuildDockerPreflight("/abs/root", mounts, map[string]string{
		"agent root": "/external/data/sub",
	})
	if err != nil {
		t.Fatalf("expected extra_mounts coverage to satisfy reachability, got %v", err)
	}
}

// --- writable-memory-dir contract (review #14) --------------------------

func TestRebuildDockerMemoryWritable_UnderProjectDir(t *testing.T) {
	if err := rebuildDockerMemoryWritable("/abs/root", "/abs/root/memory"); err != nil {
		t.Errorf("expected memory dir under project root to be writable, got %v", err)
	}
}

func TestRebuildDockerMemoryWritable_ExternalPathFailsEvenIfMounted(t *testing.T) {
	// External memory.path — even if it happens to be reachable via a
	// (read-only) extra_mounts entry, it can never be WRITTEN to from inside
	// the container, because ReExec always mounts extra_mounts :ro.
	err := rebuildDockerMemoryWritable("/abs/root", "/external/memory")
	if err == nil {
		t.Fatal("expected an error for memory.path outside the project directory")
	}
	if !strings.Contains(err.Error(), "read-only") {
		t.Errorf("expected error to explain the read-only extra_mounts contract, got: %v", err)
	}
}

// --- end-to-end through rebuildHandler: AFM_IN_DOCKER=1 skips re-exec ----

// TestRebuildHandler_DockerEnabled_SkipsReExecInsideContainer wires Docker
// mode on through a REAL project-level .afm/config.yaml (loaded by
// rebuildHandler itself via config.LoadFrom, not injected), then sets
// AFM_IN_DOCKER=1 (as docker-entrypoint.sh would inside the container) and
// asserts the whole handler runs to completion exactly as it would on the
// host without Docker — proving rebuildDockerReExec's gate is actually
// wired into rebuildHandler, not just correct in isolation.
func TestRebuildHandler_DockerEnabled_SkipsReExecInsideContainer(t *testing.T) {
	isolateHome(t)
	withFakeMemoryPipeline(t)
	dir := chdirTemp(t)
	t.Setenv("AFM_IN_DOCKER", "1")

	docker.SetExecFunc(func(string, []string, []string) error {
		t.Fatal("execFunc must not be called: AFM_IN_DOCKER=1 must short-circuit rebuildDockerReExec")
		return nil
	})
	defer docker.ResetExecFunc()

	if err := os.MkdirAll(filepath.Join(dir, ".afm"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".afm", "config.yaml"), []byte("docker:\n  enabled: true\n"), 0644); err != nil {
		t.Fatal(err)
	}

	flowPath := writeFlowFile(t, dir, "flow.yaml", singleStageFlowYAML)
	runDir := mkRunCLI(t, filepath.Join(dir, ".afm", "runs"), "testflow-20260101-000000-aaaa",
		map[string]state.StageStatus{"s1": state.StatusDone})
	writeStageSession(t, runDir, "s1")

	if err := rebuildHandler(context.Background(), rebuildOptions{FlowArg: flowPath, DryRun: true}); err != nil {
		t.Fatalf("rebuildHandler: %v", err)
	}
}

// --- isUnderDir ------------------------------------------------------------

func TestIsUnderDir(t *testing.T) {
	cases := []struct {
		path, root string
		want       bool
	}{
		{"/a/b", "/a", true},
		{"/a", "/a", true},
		{"/a/bc", "/a/b", false}, // must not match on a bare string prefix
		{"/ab", "/a", false},
		{"/x/y", "/a", false},
	}
	for _, tc := range cases {
		if got := isUnderDir(tc.path, tc.root); got != tc.want {
			t.Errorf("isUnderDir(%q, %q) = %v, want %v", tc.path, tc.root, got, tc.want)
		}
	}
}
