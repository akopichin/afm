package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/docker"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
	"github.com/akopichin/afm/pkg/worktree"
)

func TestWorktreesEnabled_Precedence(t *testing.T) {
	tr, fl := true, false
	cases := []struct {
		name string
		wt   string // --worktree value, "" = not set
		flow *bool  // flow.Worktrees
		cfg  *bool  // cfg.Worktrees.Enabled
		env  string // "" = unset
		want bool
	}{
		{name: "all unset → default OFF", flow: nil, cfg: nil, env: "", want: false},
		{name: "config on, no env/flow", flow: nil, cfg: &tr, env: "", want: true},
		{name: "env off overrides config on", flow: nil, cfg: &tr, env: "0", want: false},
		{name: "flow on overrides env off", flow: &tr, cfg: nil, env: "0", want: true},
		{name: "flow off overrides env on", flow: &fl, cfg: nil, env: "1", want: false},
		{name: "--worktree beats flow off, env off, config off", wt: "feature-x", flow: &fl, cfg: &fl, env: "0", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AFM_WORKTREES", tc.env)
			cfg := config.Config{Worktrees: config.WorktreesConfig{Enabled: tc.cfg}}
			var f *flow.Flow
			if tc.flow != nil {
				f = &flow.Flow{Worktrees: tc.flow}
			}
			if got := worktreesEnabled(tc.wt, cfg, f); got != tc.want {
				t.Errorf("worktreesEnabled: got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRunNameFor(t *testing.T) {
	if got := runNameFor("feature-x", "flow"); got != "feature-x" {
		t.Errorf("runNameFor(%q, %q) = %q, want %q", "feature-x", "flow", got, "feature-x")
	}
	if got := runNameFor("", "flow"); got != "flow" {
		t.Errorf("runNameFor(%q, %q) = %q, want %q", "", "flow", got, "flow")
	}
}

func TestWorktreeBase_UsesRootDirWhenSet(t *testing.T) {
	f := &flow.Flow{RootDir: "sub"}
	got, err := worktreeBase("/a", f)
	if err != nil {
		t.Fatalf("worktreeBase: %v", err)
	}
	if want := "/a/sub"; got != want {
		t.Errorf("worktreeBase: got %q, want %q", got, want)
	}

	// Absolute root_dir passes through, ignoring afmRoot entirely.
	fAbs := &flow.Flow{RootDir: "/elsewhere"}
	got, err = worktreeBase("/a", fAbs)
	if err != nil {
		t.Fatalf("worktreeBase: %v", err)
	}
	if want := "/elsewhere"; got != want {
		t.Errorf("worktreeBase (absolute root_dir): got %q, want %q", got, want)
	}
}

// TestWorktreeBase_FallsBackToAfmRoot proves the deliberate difference from absoluteAgentRoot: an empty
// root_dir anchors to afm-root (the parent of .afm/, per AGENTS.md), never to the process CWD.
func TestWorktreeBase_FallsBackToAfmRoot(t *testing.T) {
	tmp := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	f := &flow.Flow{}
	got, err := worktreeBase("project", f)
	if err != nil {
		t.Fatalf("worktreeBase: %v", err)
	}
	wantAfmRoot := filepath.Join(tmp, "project")
	if got != wantAfmRoot {
		t.Errorf("worktreeBase: got %q, want afm-root-anchored %q", got, wantAfmRoot)
	}
	if cwd, _ := os.Getwd(); got == cwd {
		t.Errorf("worktreeBase: got %q, must not equal os.Getwd() %q (that's absoluteAgentRoot's job, not this one)", got, cwd)
	}
}

func TestResolveWorktree_DisabledReturnsEmpty(t *testing.T) {
	t.Setenv("AFM_IN_DOCKER", "") // ensure host-mode behavior regardless of the ambient test environment
	afmRoot := t.TempDir()
	f := &flow.Flow{Name: "myflow"}
	cfg := config.Config{} // worktrees off by default

	got, lock, err := resolveWorktree(context.Background(), afmRoot, f, cfg, worktree.ClearNone, "")
	if err != nil {
		t.Fatalf("resolveWorktree: %v", err)
	}
	defer lock.Close() //nolint:errcheck
	if got != "" {
		t.Errorf("resolveWorktree: got %q, want empty", got)
	}
	if lock != nil {
		t.Error("resolveWorktree: expected a nil lock when the feature is off")
	}
	if _, statErr := os.Stat(filepath.Join(afmRoot, ".worktrees")); !os.IsNotExist(statErr) {
		t.Errorf("resolveWorktree: .worktrees was created despite the feature being off (stat err: %v)", statErr)
	}
}

func TestResolveWorktree_ClearWithoutWorktreesIsAnError(t *testing.T) {
	t.Setenv("AFM_IN_DOCKER", "") // ensure host-mode behavior regardless of the ambient test environment
	afmRoot := t.TempDir()
	f := &flow.Flow{Name: "myflow"}
	cfg := config.Config{} // worktrees off by default

	_, _, err := resolveWorktree(context.Background(), afmRoot, f, cfg, worktree.ClearWorktree, "")
	if err == nil {
		t.Fatal("resolveWorktree: expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "worktrees") {
		t.Errorf("resolveWorktree error should mention enabling worktrees, got: %v", err)
	}
}

func TestResolveWorktree_BothClearFlagsIsAnError(t *testing.T) {
	_, err := clearModeFromFlags(true, true)
	if err == nil {
		t.Fatal("clearModeFromFlags: expected an error for both flags set, got nil")
	}
}

// TestClearModeFromFlags_Mapping pins the clear/force → ClearMode mapping: a
// swapped case here would make --worktree-clear-force silently non-destructive,
// or --worktree-clear silently delete the user's branch.
func TestClearModeFromFlags_Mapping(t *testing.T) {
	cases := []struct {
		name              string
		clear, clearForce bool
		want              worktree.ClearMode
	}{
		{name: "neither flag", clear: false, clearForce: false, want: worktree.ClearNone},
		{name: "--worktree-clear", clear: true, clearForce: false, want: worktree.ClearWorktree},
		{name: "--worktree-clear-force", clear: false, clearForce: true, want: worktree.ClearAll},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := clearModeFromFlags(tc.clear, tc.clearForce)
			if err != nil {
				t.Fatalf("clearModeFromFlags(%v, %v): unexpected error: %v", tc.clear, tc.clearForce, err)
			}
			if got != tc.want {
				t.Errorf("clearModeFromFlags(%v, %v) = %v, want %v", tc.clear, tc.clearForce, got, tc.want)
			}
		})
	}
}

// TestResolveWorktree_InContainerReadsTransportEnv confirms the in-container branch never touches the
// filesystem — not even the lock file. The host already holds the flow's lock for the container's whole
// lifetime (docker.ReExec blocks on `docker run` as a child process, not a process-image replacement), so
// the container acquiring it too would deadlock against the host's own hold.
func TestResolveWorktree_InContainerReadsTransportEnv(t *testing.T) {
	t.Setenv("AFM_IN_DOCKER", "1")
	t.Setenv(docker.WorktreeEnvVar, "/w")
	chdirTemp(t) // isolates flowLocksDir() (keyed off runsDir(), not afmRoot) into a temp dir

	// A non-existent afmRoot proves resolveWorktree never touches the project filesystem in-container.
	afmRoot := filepath.Join(t.TempDir(), "does-not-exist")
	f := &flow.Flow{Name: "myflow"}
	cfg := config.Config{}

	got, lock, err := resolveWorktree(context.Background(), afmRoot, f, cfg, worktree.ClearNone, "")
	if err != nil {
		t.Fatalf("resolveWorktree: %v", err)
	}
	defer lock.Close() //nolint:errcheck
	if got != "/w" {
		t.Errorf("resolveWorktree: got %q, want %q", got, "/w")
	}
	if lock != nil {
		t.Error("resolveWorktree: expected a nil lock in-container — the host already holds it")
	}
	if _, statErr := os.Stat(flowLocksDir()); !os.IsNotExist(statErr) {
		t.Errorf("resolveWorktree: lock dir was created in-container despite not acquiring anything (stat err: %v)", statErr)
	}
	if _, statErr := os.Stat(afmRoot); !os.IsNotExist(statErr) {
		t.Errorf("resolveWorktree: afmRoot should remain untouched, stat err: %v", statErr)
	}
}

// TestResolveWorktree_CustomNameCreatesNamedWorktree proves --worktree=<name> names the branch/directory
// after the flag, not the flow — the flow keeps its own name (f.Name) for lifecycle payloads/memory/etc.
func TestResolveWorktree_CustomNameCreatesNamedWorktree(t *testing.T) {
	t.Setenv("AFM_IN_DOCKER", "")
	chdirTemp(t)

	repo := initGitRepoWithCommit(t)
	f := &flow.Flow{Name: "flow"}
	cfg := config.Config{}

	got, lock, err := resolveWorktree(context.Background(), repo, f, cfg, worktree.ClearNone, "feature-x")
	if err != nil {
		t.Fatalf("resolveWorktree: %v", err)
	}
	defer lock.Close() //nolint:errcheck
	if lock == nil {
		t.Fatal("resolveWorktree: expected a non-nil lock")
	}
	want := filepath.Join(repo, ".worktrees", "feature-x")
	if got != want {
		t.Errorf("resolveWorktree: got %q, want %q", got, want)
	}

	cmd := exec.Command("git", "rev-parse", "--verify", "refs/heads/feature-x")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("branch feature-x was not created: %v: %s", err, out)
	}
	cmd = exec.Command("git", "rev-parse", "--verify", "refs/heads/flow")
	cmd.Dir = repo
	if err := cmd.Run(); err == nil {
		t.Error("branch 'flow' (the flow name) should not exist when --worktree overrides it")
	}
}

// TestResolveWorktree_LocksByWorktreeNameNotFlowName proves the flow lock key tracks the worktree's
// effective name (the --worktree override), not f.Name — two flags on the same flow that pick different
// names must not contend with each other, and the same name must still serialize.
func TestResolveWorktree_LocksByWorktreeNameNotFlowName(t *testing.T) {
	t.Setenv("AFM_IN_DOCKER", "")
	chdirTemp(t)

	held, err := worktree.TryLockFlow(flowLocksDir(), "feature-x")
	if err != nil {
		t.Fatalf("TryLockFlow: %v", err)
	}
	defer held.Close() //nolint:errcheck

	repo := initGitRepoWithCommit(t)
	f := &flow.Flow{Name: "flow"}
	cfg := config.Config{}

	if _, _, err := resolveWorktree(context.Background(), repo, f, cfg, worktree.ClearNone, "feature-x"); err == nil {
		t.Fatal("resolveWorktree: expected an error while feature-x's lock is held, got nil")
	} else if !strings.Contains(err.Error(), "feature-x") {
		t.Errorf("resolveWorktree error should name the worktree, got: %v", err)
	}

	got, lock, err := resolveWorktree(context.Background(), repo, f, cfg, worktree.ClearNone, "feature-y")
	if err != nil {
		t.Fatalf("resolveWorktree (different name): %v", err)
	}
	defer lock.Close() //nolint:errcheck
	if got == "" {
		t.Error("resolveWorktree (different name): expected a non-empty path")
	}
}

func TestResolveWorktree_RejectsInvalidName(t *testing.T) {
	t.Setenv("AFM_IN_DOCKER", "")
	chdirTemp(t)

	repo := initGitRepoWithCommit(t)
	f := &flow.Flow{Name: "flow"}
	cfg := config.Config{}

	if _, _, err := resolveWorktree(context.Background(), repo, f, cfg, worktree.ClearNone, "../evil"); err == nil {
		t.Fatal("resolveWorktree: expected an error for an unsafe --worktree name, got nil")
	}
	if _, statErr := os.Stat(filepath.Join(repo, ".worktrees")); !os.IsNotExist(statErr) {
		t.Errorf("resolveWorktree: .worktrees was created despite the invalid name (stat err: %v)", statErr)
	}
}

// TestResolveRun_WorktreeNameIsolatesRuns proves a run started with --worktree=<name> gets its own run
// directory prefix and never resumes a run started under the plain flow name (or vice versa) — the two
// are isolated exactly like renaming `name:` in flow.yaml would isolate them today.
func TestResolveRun_WorktreeNameIsolatesRuns(t *testing.T) {
	chdirTemp(t)

	oldRunDir := makeRunState(t, "flow-20260101-100000", "stage1", state.StatusAwaitingApproval)

	f := &flow.Flow{Name: "flow", Stages: []flow.Stage{{ID: "stage1"}}}

	runDir, store, err := resolveRun(f, "feature-x", false)
	if err != nil {
		t.Fatalf("resolveRun: %v", err)
	}
	store.Close()
	if runDir == oldRunDir {
		t.Error("resolveRun(worktree=feature-x): reused the plain-flow-name run dir, want an isolated one")
	}
	if !strings.HasPrefix(filepath.Base(runDir), "feature-x-") {
		t.Errorf("resolveRun(worktree=feature-x): run dir %q does not have the feature-x- prefix", runDir)
	}

	resumedDir, store2, err := resolveRun(f, "flow", false)
	if err != nil {
		t.Fatalf("resolveRun: %v", err)
	}
	store2.Close()
	// resolveRun hands out an absolute run dir: its paths reach agents whose CWD may be a worktree.
	if want, _ := filepath.Abs(oldRunDir); resumedDir != want {
		t.Errorf("resolveRun(worktree=flow): got %q, want it to resume %q", resumedDir, want)
	}
}

func TestRunCmd_HasWorktreeFlag(t *testing.T) {
	f := newRunCmd().Flags().Lookup("worktree")
	if f == nil {
		t.Fatal("newRunCmd: expected a --worktree flag")
	}
	if f.DefValue != "" {
		t.Errorf("--worktree default = %q, want empty", f.DefValue)
	}
}

func TestResolveRun_ForceNewSkipsResume(t *testing.T) {
	chdirTemp(t)

	oldRunDir := makeRunState(t, "flow-20260101-100000", "stage1", state.StatusAwaitingApproval)
	oldEvents, err := os.ReadFile(filepath.Join(oldRunDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}

	f := &flow.Flow{Name: "flow", Stages: []flow.Stage{{ID: "stage1"}}}

	runDir, store, err := resolveRun(f, "", true)
	if err != nil {
		t.Fatalf("resolveRun: %v", err)
	}
	defer store.Close()

	if runDir == oldRunDir {
		t.Errorf("resolveRun(forceNew=true): reused the old run dir %q, want a brand-new one", oldRunDir)
	}
	if _, statErr := os.Stat(oldRunDir); statErr != nil {
		t.Errorf("resolveRun(forceNew=true): old run dir %q was removed/touched: %v", oldRunDir, statErr)
	}
	newEvents, err := os.ReadFile(filepath.Join(oldRunDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(newEvents) != string(oldEvents) {
		t.Error("resolveRun(forceNew=true): old run dir's events.jsonl was modified")
	}
}

// initGitRepoWithCommit creates a fresh git repo with one commit, so `git worktree add -b` has a HEAD to
// branch from. Skips the test if git is not on PATH.
func initGitRepoWithCommit(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=afm-test", "GIT_AUTHOR_EMAIL=afm-test@example.com",
			"GIT_COMMITTER_NAME=afm-test", "GIT_COMMITTER_EMAIL=afm-test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init")
	run("commit", "--allow-empty", "-m", "init")
	return dir
}

func TestResolveWorktree_RefusesOutsideProjectDirUnderDocker(t *testing.T) {
	// Host-mode behavior regardless of the ambient test environment (which may itself be a container, or
	// carry a leftover AFM_IN_DOCKER=1) — this test is specifically about cfg.Docker.Enabled, not about
	// afm's own containment detection.
	t.Setenv("AFM_IN_DOCKER", "")
	t.Cleanup(config.SwapContainerMarkerPresent(func() bool { return false }))
	chdirTemp(t) // isolates flowLocksDir() (keyed off runsDir()) into a temp dir

	afmRoot := initGitRepoWithCommit(t)
	outside := initGitRepoWithCommit(t)

	tr := true
	f := &flow.Flow{Name: "flow", RootDir: outside, Worktrees: &tr}

	dockerOn := config.Config{Docker: config.DockerConfig{Enabled: &tr}}
	_, _, err := resolveWorktree(context.Background(), afmRoot, f, dockerOn, worktree.ClearNone, "")
	if err == nil {
		t.Fatal("resolveWorktree: expected an error for root_dir outside afm-root under Docker, got nil")
	}
	if !strings.Contains(err.Error(), afmRoot) || !strings.Contains(err.Error(), outside) {
		t.Errorf("resolveWorktree error should name both paths, got: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, ".worktrees")); !os.IsNotExist(statErr) {
		t.Errorf("resolveWorktree: .worktrees was created despite the containment guard failing (stat err: %v)", statErr)
	}

	dockerOff := config.Config{}
	got, lock, err := resolveWorktree(context.Background(), afmRoot, f, dockerOff, worktree.ClearNone, "")
	if err != nil {
		t.Fatalf("resolveWorktree (Docker off): %v", err)
	}
	defer lock.Close() //nolint:errcheck
	want := filepath.Join(outside, ".worktrees", "flow")
	if got != want {
		t.Errorf("resolveWorktree (Docker off): got %q, want %q", got, want)
	}
}

// TestResolveWorktree_RefusesClearWhileFlowLockHeld guards against --worktree-clear(-force) tearing down
// the worktree of a flow that something else currently holds the lock for (any live `afm run`, or a
// concurrent clear): a second invocation must refuse, not race the first one's agents. The lock is keyed
// by flow name, not any run directory, so this holds regardless of what resolveRun has done with run
// directories (see TestResolveWorktree_RefusesClearWhileNewRunStarts for the scenario that specifically
// motivated that design).
func TestResolveWorktree_RefusesClearWhileFlowLockHeld(t *testing.T) {
	t.Setenv("AFM_IN_DOCKER", "")
	chdirTemp(t)

	held, err := worktree.TryLockFlow(flowLocksDir(), "myflow")
	if err != nil {
		t.Fatalf("TryLockFlow: %v", err)
	}
	defer held.Close() //nolint:errcheck

	afmRoot := initGitRepoWithCommit(t)
	tr := true
	f := &flow.Flow{Name: "myflow", RootDir: afmRoot, Worktrees: &tr}
	cfg := config.Config{}

	_, _, err = resolveWorktree(context.Background(), afmRoot, f, cfg, worktree.ClearWorktree, "")
	if err == nil {
		t.Fatal("resolveWorktree: expected an error while the flow's lock is held, got nil")
	}
	if !strings.Contains(err.Error(), "myflow") {
		t.Errorf("resolveWorktree error should name the flow, got: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(afmRoot, ".worktrees")); !os.IsNotExist(statErr) {
		t.Errorf("resolveWorktree: .worktrees was created despite the lock being held (stat err: %v)", statErr)
	}

	// Once the lock is released, the same clear mode must proceed normally.
	if err := held.Close(); err != nil {
		t.Fatalf("held.Close: %v", err)
	}
	got, _, err := resolveWorktree(context.Background(), afmRoot, f, cfg, worktree.ClearWorktree, "")
	if err != nil {
		t.Fatalf("resolveWorktree after lock release: %v", err)
	}
	if got == "" {
		t.Error("resolveWorktree after lock release: expected a non-empty path")
	}
}

// TestResolveWorktree_RefusesClearWhileNewRunStarts is the scenario that motivated keying the lock by
// flow name rather than by a snapshot of run directories: a NEW run (resolveWorktree with ClearNone)
// starts and is given a lock it's expected to hold for its whole lifetime. While that run is still
// "live" (simulated by not yet closing the returned lock), a clear for the same flow must be refused —
// even though no run directory was ever involved in the check at all.
func TestResolveWorktree_RefusesClearWhileNewRunStarts(t *testing.T) {
	t.Setenv("AFM_IN_DOCKER", "")
	chdirTemp(t)

	afmRoot := initGitRepoWithCommit(t)
	tr := true
	f := &flow.Flow{Name: "myflow", RootDir: afmRoot, Worktrees: &tr}
	cfg := config.Config{}

	// Simulates a plain `afm run myflow.yaml` starting: resolveWorktree returns a lock the caller must
	// hold for the run's lifetime.
	runPath, runLock, err := resolveWorktree(context.Background(), afmRoot, f, cfg, worktree.ClearNone, "")
	if err != nil {
		t.Fatalf("resolveWorktree (normal run start): %v", err)
	}
	if runLock == nil {
		t.Fatal("resolveWorktree (normal run start): expected a non-nil lock to hold for the run's lifetime")
	}
	defer runLock.Close() //nolint:errcheck

	// The "run" is still live (its lock is still held) when a clear is attempted for the same flow.
	_, _, err = resolveWorktree(context.Background(), afmRoot, f, cfg, worktree.ClearWorktree, "")
	if err == nil {
		t.Fatal("resolveWorktree(ClearWorktree): expected an error while the new run is still active, got nil")
	}
	if !strings.Contains(err.Error(), "myflow") {
		t.Errorf("resolveWorktree(ClearWorktree) error should name the flow, got: %v", err)
	}
	// The worktree the "run" created must survive untouched.
	if _, statErr := os.Stat(runPath); statErr != nil {
		t.Errorf("resolveWorktree(ClearWorktree): worktree %s was affected despite the active run: %v", runPath, statErr)
	}

	// Once the run finishes (releases its lock), the same clear must proceed normally.
	if err := runLock.Close(); err != nil {
		t.Fatalf("runLock.Close: %v", err)
	}
	if _, _, err := resolveWorktree(context.Background(), afmRoot, f, cfg, worktree.ClearWorktree, ""); err != nil {
		t.Fatalf("resolveWorktree(ClearWorktree) after run finished: %v", err)
	}
}

// TestResolveWorktree_ClearHoldsLockForSubsequentRun guards the companion defect: a clear flag is never
// a standalone operation ("Both additionally force a fresh run"), so the SAME invocation that clears the
// worktree immediately starts using it for a real run. The returned lock must therefore be held for that
// run's lifetime too — not just through Ensure's clear — or a second, unrelated clear could race the run
// that the first clear itself kicked off.
func TestResolveWorktree_ClearHoldsLockForSubsequentRun(t *testing.T) {
	t.Setenv("AFM_IN_DOCKER", "")
	chdirTemp(t)

	afmRoot := initGitRepoWithCommit(t)
	tr := true
	f := &flow.Flow{Name: "myflow", RootDir: afmRoot, Worktrees: &tr}
	cfg := config.Config{}

	// Simulates `afm run myflow.yaml --worktree-clear`: the clear succeeds, and resolveWorktree must
	// return a lock for the run that RunE starts right after, not nil.
	clearedPath, clearLock, err := resolveWorktree(context.Background(), afmRoot, f, cfg, worktree.ClearWorktree, "")
	if err != nil {
		t.Fatalf("resolveWorktree(ClearWorktree): %v", err)
	}
	if clearLock == nil {
		t.Fatal("resolveWorktree(ClearWorktree): expected a non-nil lock to hold for the run it starts afterward")
	}
	defer clearLock.Close() //nolint:errcheck

	// A second, unrelated clear attempt (e.g. a user running --worktree-clear in another terminal) must
	// be refused while the first clear's own subsequent run is still "live".
	_, _, err = resolveWorktree(context.Background(), afmRoot, f, cfg, worktree.ClearWorktree, "")
	if err == nil {
		t.Fatal("resolveWorktree(ClearWorktree) #2: expected an error while the first clear's run is still active, got nil")
	}
	if _, statErr := os.Stat(clearedPath); statErr != nil {
		t.Errorf("resolveWorktree(ClearWorktree) #2: worktree %s was affected: %v", clearedPath, statErr)
	}
}
