package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
)

func TestTaskDir_RejectsUnsafeIds(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	runDir, err := filepath.Abs(o.opts.RunDir)
	if err != nil {
		t.Fatal(err)
	}

	unsafe := []assistants.TaskID{
		"../../etc/passwd",
		"..",
		"a/b",
		"a\x00b",
		"/absolute",
	}
	for _, id := range unsafe {
		dir := o.taskDir(id)
		abs, err := filepath.Abs(dir)
		if err != nil {
			t.Fatalf("Abs(%q): %v", dir, err)
		}
		rel, err := filepath.Rel(runDir, abs)
		if err != nil || strings.HasPrefix(rel, "..") {
			t.Fatalf("taskDir(%q) = %q escapes RunDir (rel=%q)", id, dir, rel)
		}
		if !strings.HasPrefix(abs, filepath.Join(runDir, assistantsNamespaceDir)+string(filepath.Separator)) &&
			abs != filepath.Join(runDir, assistantsNamespaceDir) {
			t.Fatalf("taskDir(%q) = %q is not confined under %s/", id, dir, assistantsNamespaceDir)
		}
	}
}

func TestTaskDir_CreatedWithMkdirAll(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	taskID := assistants.TaskID("t-mkdir-test")
	dir := o.taskDir(taskID)
	scratch := o.taskScratchDir(taskID)

	if err := ensureTaskWorkspace(dir, scratch); err != nil {
		t.Fatalf("ensureTaskWorkspace: %v", err)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("task dir not created: %v", err)
	}
	if fi, err := os.Stat(scratch); err != nil || !fi.IsDir() {
		t.Fatalf("scratch dir not created: %v", err)
	}
}

func TestScratch_IsOnlyWritablePath(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	taskID := assistants.TaskID("t-scratch-test")
	cfg := o.helperExecutorConfig(taskID, "helper-cmd")
	want := o.taskScratchDir(taskID)
	if cfg.Dir != want {
		t.Fatalf("helper executor Dir = %q, want scratch dir %q", cfg.Dir, want)
	}
}

func TestTaskDir_AnchoredToRunDirNotCwd(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	o.opts.RootDir = "/some/completely/different/project/root"

	taskID := assistants.TaskID("t-anchor-test")
	dir := o.taskDir(taskID)
	if !strings.HasPrefix(dir, o.opts.RunDir) {
		t.Fatalf("taskDir = %q, want it anchored under RunDir %q regardless of root_dir", dir, o.opts.RunDir)
	}
	if strings.Contains(dir, "different/project/root") {
		t.Fatalf("taskDir leaked root_dir into its path: %q", dir)
	}
}

func TestTaskDir_NoAccessToRunControlFiles(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	taskID := assistants.TaskID("t-control-test")
	stage := *o.reg().Stage(assistantsTestStageID)
	task := assistants.Task{ID: taskID, RootStageID: assistantsTestStageID, Instruction: "x"}

	tc, err := o.buildTaskContext(task, stage)
	if err != nil {
		t.Fatalf("buildTaskContext: %v", err)
	}
	for _, forbidden := range []string{"events.jsonl", "state.json"} {
		if strings.Contains(tc.Prompt, forbidden) {
			t.Fatalf("task context must never list/mention run-control files, found %q in prompt", forbidden)
		}
	}
	// And structurally: nothing in this package ever reads events.jsonl/state.json to build a task context —
	// buildTaskContext only touches plan.md/artifacts/the task's own result.json (see assistants_context.go).
}
