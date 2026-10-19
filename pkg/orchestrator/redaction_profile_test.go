package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
)

// journalOfTask is every journaled effect payload of taskID, as one string.
func journalOfTask(o *Orchestrator, taskID assistants.TaskID) string {
	var b strings.Builder
	for _, e := range o.opts.Store.TaskEvents(taskID) {
		b.WriteString(e.Kind + " " + string(e.Payload) + "\n")
	}
	return b.String()
}

// The frozen profile snapshot in task_created — top-level and delegated — and a delegation's continuation note
// never carry a known secret.
func TestRedaction_ProfileSnapshotAndContinuationNote(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	const note = "s3cret-continuation-note"
	o.opts.SecretValues = []string{"Reviewer", note} // "Reviewer" is the profile's title
	var calls atomic.Int32
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		if calls.Add(1) == 1 {
			raw := `{"schema_version":1,"kind":"delegate","children":[` + childJSON(assistants.ModeAdvisory) +
				`],"continuation":{"trigger":"on_completion","note":"` + note + `"}}`
			return executor.HelperOutcome{ProcessOK: true, RawText: raw}, nil
		}
		return passResultOutcome(), nil
	}
	parentID := attachTestTask(context.Background(), t, o, "op-secret-parent")
	waitForTaskStatus(t, store, parentID, assistants.StatusCompleted, 3*time.Second)
	o.concurrency.WaitAgents()
	childID := store.Snapshot().Assistants.ByParent[parentID][0]

	for _, id := range []assistants.TaskID{parentID, childID} {
		journal := journalOfTask(o, id)
		if !strings.Contains(journal, "task_created") {
			t.Fatalf("no task_created for %s", id)
		}
		if strings.Contains(journal, "Reviewer") || strings.Contains(journal, note) {
			t.Fatalf("task %s journal leaks a secret:\n%s", id, journal)
		}
	}
	if !strings.Contains(journalOfTask(o, parentID), "task_continuation_saved") {
		t.Fatal("the continuation was not journaled")
	}
}

// helper_tree_modified names the agent by a title that may predate title redaction: it is masked again.
func TestRedaction_HelperTreeModifiedTitle(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	var fp atomic.Int32
	o.fingerprintFn = func(string) (string, bool) { return fmt.Sprint(fp.Add(1)), true }
	release := make(chan struct{})
	started := make(chan struct{})
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		close(started)
		awaitRelease(o, release)
		return passResultOutcome(), nil
	}
	taskID := attachTestTask(context.Background(), t, o, "op-old-title") // titled "Reviewer", unredacted
	<-started
	o.opts.SecretValues = []string{"Reviewer"} // known only now, like a title journaled by an older afm
	close(release)
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)
	o.concurrency.WaitAgents()

	for _, n := range noticesOfType(readVerifyNotices(t, o.opts.RunDir), bus.EventHelperTreeModified) {
		if strings.Contains(fmt.Sprint(n.Data), "Reviewer") {
			t.Fatalf("helper_tree_modified leaks the title: %v", n.Data)
		}
	}
}

// The workspace paths in the task context are masked like every other part of it.
func TestRedaction_WorkspacePathsInContext(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	const secretDir = "s3cret-dir-name"
	o.opts.RootDir = filepath.Join(t.TempDir(), secretDir)
	if err := os.MkdirAll(o.opts.RootDir, 0o755); err != nil {
		t.Fatal(err)
	}
	o.opts.SecretValues = []string{secretDir}
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		return passResultOutcome(), nil
	}
	taskID := attachTestTask(context.Background(), t, o, "op-secret-root")
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)
	o.concurrency.WaitAgents()
	raw, err := os.ReadFile(filepath.Join(o.taskDir(taskID), "context.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secretDir) || !strings.Contains(string(raw), "Project root") {
		t.Fatalf("context.md leaks the workspace path:\n%s", raw)
	}
}
