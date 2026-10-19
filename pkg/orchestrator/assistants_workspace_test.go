package orchestrator

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
)

// gitT runs git in dir with a fixed identity and returns its trimmed stdout.
func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newGitRepo creates a repository with one commit and returns its directory and that commit.
func newGitRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "add", ".")
	gitT(t, dir, "commit", "-q", "-m", "first")
	return dir, gitT(t, dir, "rev-parse", "HEAD")
}

func TestDiffBaseRevision(t *testing.T) {
	t.Run("plain checkout: HEAD", func(t *testing.T) {
		repo, head := newGitRepo(t)
		if got := diffBaseRevision(repo); got != head {
			t.Fatalf("base = %q, want HEAD %q", got, head)
		}
	})
	t.Run("linked worktree: the branch's fork point, so committed branch work shows", func(t *testing.T) {
		repo, forkPoint := newGitRepo(t)
		wt := filepath.Join(t.TempDir(), "wt")
		gitT(t, repo, "worktree", "add", "-q", "-b", "feature", wt)
		if err := os.WriteFile(filepath.Join(wt, "b.txt"), []byte("b\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitT(t, wt, "add", ".")
		gitT(t, wt, "commit", "-q", "-m", "feature work")
		if got := diffBaseRevision(wt); got != forkPoint {
			t.Fatalf("base = %q, want the fork point %q", got, forkPoint)
		}
	})
	t.Run("not a git checkout: none", func(t *testing.T) {
		if got := diffBaseRevision(t.TempDir()); got != "" {
			t.Fatalf("base = %q, want none outside git", got)
		}
	})
}

// TestTaskContext_WorkspaceSection: a helper's prompt says where the project is, which revision to diff
// against, and where its scratch directory is — as a place for notes, never as a write-protection promise.
func TestTaskContext_WorkspaceSection(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	repo, head := newGitRepo(t)
	o.opts.RootDir = repo
	taskID := assistants.TaskID("t-workspace")
	task := assistants.Task{ID: taskID, RootStageID: assistantsTestStageID, Instruction: "review"}

	tc, err := o.buildTaskContext(task, *o.reg().Stage(assistantsTestStageID))
	if err != nil {
		t.Fatalf("buildTaskContext: %v", err)
	}
	for _, want := range []string{"## Workspace", repo, "git diff " + head, o.taskScratchDir(taskID), "AFM_SCRATCH_DIR"} {
		if !strings.Contains(tc.Prompt, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, tc.Prompt)
		}
	}
	for _, promise := range []string{"only writable", "read-only", "cannot write"} {
		if strings.Contains(strings.ToLower(tc.Prompt), promise) {
			t.Fatalf("the workspace section must not promise write protection (%q):\n%s", promise, tc.Prompt)
		}
	}
}

// TestTaskContext_WorkspaceSection_ReadOnlySandbox: a helper in an OS read-only sandbox (the host codex
// adapter) is told plainly that it cannot write — not even its scratch directory — instead of being offered a
// place for notes it cannot use. The test pins the host environment: run inside a container (a Linux CI
// image has /.dockerenv) the same task honestly has no OS sandbox.
func TestTaskContext_WorkspaceSection_ReadOnlySandbox(t *testing.T) {
	t.Setenv("AFM_IN_DOCKER", "")
	t.Cleanup(config.SwapContainerMarkerPresent(func() bool { return false }))
	o, _ := newAssistantsTestOrchestrator(t, true)
	o.opts.RootDir = t.TempDir()
	taskID := assistants.TaskID("t-readonly")
	task := assistants.Task{ID: taskID, RootStageID: assistantsTestStageID, Instruction: "review",
		ProfileSnapshot: assistants.Profile{Command: "codex-as-claude", ReadonlyEnforcement: assistants.ReadonlyEnforcementOS}}

	tc, err := o.buildTaskContext(task, *o.reg().Stage(assistantsTestStageID))
	if err != nil {
		t.Fatalf("buildTaskContext: %v", err)
	}
	if !strings.Contains(tc.Prompt, "read-only sandbox") || !strings.Contains(tc.Prompt, "cannot write") {
		t.Fatalf("a read-only helper must be told it cannot write its notes:\n%s", tc.Prompt)
	}
	if strings.Contains(tc.Prompt, "keep your notes and temporary files there") {
		t.Fatalf("a read-only helper must not be offered a scratch it cannot write:\n%s", tc.Prompt)
	}
}

// TestTaskContext_WorkspaceSection_SandboxFromCurrentEnvironment: the sandbox claim follows the environment the
// turn runs in now, not the admission snapshot — a task admitted on the host under the codex read-only sandbox
// and resumed in a container has no OS sandbox, so it is offered its scratch directory instead.
func TestTaskContext_WorkspaceSection_SandboxFromCurrentEnvironment(t *testing.T) {
	t.Setenv("AFM_IN_DOCKER", "1")
	o, _ := newAssistantsTestOrchestrator(t, true)
	o.opts.RootDir = t.TempDir()
	task := assistants.Task{ID: "t-resumed", RootStageID: assistantsTestStageID, Instruction: "review",
		ProfileSnapshot: assistants.Profile{Command: "codex-as-claude", ReadonlyEnforcement: assistants.ReadonlyEnforcementOS}}

	tc, err := o.buildTaskContext(task, *o.reg().Stage(assistantsTestStageID))
	if err != nil {
		t.Fatalf("buildTaskContext: %v", err)
	}
	if strings.Contains(tc.Prompt, "read-only sandbox") {
		t.Fatalf("a helper without an OS sandbox now must not be told it runs in one:\n%s", tc.Prompt)
	}
	if !strings.Contains(tc.Prompt, "keep your notes and temporary files there") {
		t.Fatalf("a helper that can write must be offered its scratch directory:\n%s", tc.Prompt)
	}
}

// TestHelperTurn_RecordsTheSandboxItRanUnder: the turn-start record carries the read-only guarantee the turn
// actually ran under — the current environment's (a container: none), not the admission snapshot's.
func TestHelperTurn_RecordsTheSandboxItRanUnder(t *testing.T) {
	t.Setenv("AFM_IN_DOCKER", "1")
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return passResultOutcome(), nil
	}

	taskID := attachTestTask(context.Background(), t, o, "op-sandbox")
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)
	o.concurrency.WaitAgents()

	if got := store.Snapshot().Assistants.Tasks[taskID].TurnReadonlyEnforcement; got != assistants.ReadonlyEnforcementNone {
		t.Fatalf("turn read-only enforcement = %q, want the container's none", got)
	}
}

// TestHelperTreeModified_NamesTheAgentAndKeepsVerdict: a working tree that changed during a helper's turn is
// reported (durably, for the feed) with the agent's name — an observation that never changes the verdict.
func TestHelperTreeModified_NamesTheAgentAndKeepsVerdict(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	var calls atomic.Int32
	o.fingerprintFn = func(string) (string, bool) { return fmt.Sprint(calls.Add(1)), true } // differs before/after
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return passResultOutcome(), nil
	}

	taskID := attachTestTask(context.Background(), t, o, "op-tree")
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)
	o.concurrency.WaitAgents()

	modified := noticesOfType(readVerifyNotices(t, o.opts.RunDir), bus.EventHelperTreeModified)
	if len(modified) != 1 {
		t.Fatalf("helper_tree_modified notices = %d, want 1", len(modified))
	}
	if modified[0].Data["task_id"] != string(taskID) || modified[0].Data["agent"] != "Reviewer" {
		t.Fatalf("notice data = %v, want task_id %s and agent %q", modified[0].Data, taskID, "Reviewer")
	}
}
