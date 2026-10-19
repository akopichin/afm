package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/executor"
)

// A turn's "previous result" is the last result the journal committed for the task — never a result.json left
// by a turn whose outcome was not committed (a crash, or a CancelTask winning the finish).
func TestPreviousResult_ComesFromTheCommittedJournal(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		return executor.HelperOutcome{ProcessOK: true, RawText: `{"schema_version":1,"kind":"result","verdict":"pass","summary":"committed answer"}`}, nil
	}
	msg := "first"
	res := addTestRunAgent(t, o, "op-prev", &msg)
	waitForRunAgentTurn(t, store, res.TaskID, 2*time.Second)
	o.concurrency.WaitAgents()

	if got := o.previousTaskResultJSON(res.TaskID); !strings.Contains(got, "committed answer") {
		t.Fatalf("previous result = %q, want the committed one", got)
	}
	// A later turn wrote its answer to result.json but its outcome never committed.
	if err := os.WriteFile(filepath.Join(o.taskDir(res.TaskID), "result.json"), []byte(`{"summary":"uncommitted answer"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := o.previousTaskResultJSON(res.TaskID); strings.Contains(got, "uncommitted") || !strings.Contains(got, "committed answer") {
		t.Fatalf("previous result = %q, want the committed one, not result.json", got)
	}
	// A task with no committed result has none, whatever its directory holds.
	other := addTestRunAgent(t, o, "op-none", nil)
	if err := os.MkdirAll(o.taskDir(other.TaskID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(o.taskDir(other.TaskID), "result.json"), []byte(`{"summary":"uncommitted"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := o.previousTaskResultJSON(other.TaskID); got != "" {
		t.Fatalf("previous result = %q, want none", got)
	}
}
