package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
)

var (
	journalTaskID    = regexp.MustCompile(`t-[0-9a-f]{20}`)
	journalMessageID = regexp.MustCompile(`mm-[0-9a-f]{20}`)
	journalOpSuffix  = regexp.MustCompile(`(task-[a-z-]+:TASK:(?:[a-z0-9]+:)?)[0-9a-f]{16}`)
	journalContext   = regexp.MustCompile(`"context_version":"[0-9a-f]+"`)
	journalTime      = regexp.MustCompile(`\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?Z`)
	journalDeliverID = regexp.MustCompile(`mesh-deliver-[0-9a-f]{20}`)
)

// normalizedJournal is the run's events.jsonl in order, with only what the clock and the random id generator decide
// replaced: times, the context version hash and the generated task, message and operation ids. Everything else — kinds, targets,
// payloads — stays, so any business difference shows.
func normalizedJournal(t *testing.T, runDir string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("journal line %q: %v", line, err)
		}
		delete(rec, "time")
		enc, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		norm := journalTaskID.ReplaceAllString(string(enc), "TASK")
		norm = journalMessageID.ReplaceAllString(norm, "MSG")
		norm = journalDeliverID.ReplaceAllString(norm, "mesh-deliver-ID")
		norm = journalOpSuffix.ReplaceAllString(norm, "${1}HASH")
		norm = journalContext.ReplaceAllString(norm, `"context_version":"CTX"`)
		norm = journalTime.ReplaceAllString(norm, "TIME")
		out = append(out, norm)
	}
	return out
}

// The same helper scenario — a usage-limit park, then a wake and a pass — gives the same business events whether
// the turn publishes activity, publishes nothing, or cannot write notices at all.
func TestAssistantActivity_DoesNotChangeBusinessEvents(t *testing.T) {
	scenario := func(t *testing.T, publish, brokenNotices bool) (journal []string, verdict string) {
		o, store, clk := newQuotaOrch(t)
		if brokenNotices {
			if err := os.Mkdir(filepath.Join(o.opts.RunDir, "notices.jsonl"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		limited := limitFirst(holdNow.Add(2 * time.Minute))
		o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
			if publish {
				emit := o.helperExecutorConfig(assistants.TaskID(taskID), cmd).OnActivity
				emit(executor.ActivityEvent{Kind: executor.KindMessage, InvocationID: "inv", ItemID: "msg:1", Revision: 1, Detail: "working"})
				emit(executor.ActivityEvent{Kind: executor.KindToolStarted, InvocationID: "inv", ItemID: "tool:c1", Revision: 1, Tool: "Bash", Detail: "ls"})
				emit(executor.ActivityEvent{Kind: executor.KindInvocationFinished, InvocationID: "inv", Outcome: executor.OutcomeCompleted})
			}
			return limited.run(ctx, taskID, profileID, cmd, prompt, logFile, resultFile)
		}
		msg := "please review"
		res := addTestRunAgent(t, o, "op-fsm", &msg)
		waitForQuotaPark(t, store, res.TaskID)
		clk.BlockUntil(1)
		advanceUntil(t, clk, time.Minute, "the helper wakes", func() bool { return limited.calls.Load() >= 2 })
		tk := waitForRunAgentTurn(t, store, res.TaskID, 5*time.Second)
		if publish && !brokenNotices {
			if saved := len(noticesOf(t, o, activityNoticeType)); saved != 6 {
				t.Fatalf("saved activity notices = %d, want 6 (three per call)", saved)
			}
		}
		return normalizedJournal(t, o.opts.RunDir), string(tk.LastVerdict)
	}

	quiet, quietVerdict := scenario(t, false, false)
	if len(quiet) == 0 {
		t.Fatal("the scenario recorded no journal")
	}
	for name, c := range map[string]struct{ publish, broken bool }{
		"with activity":                     {true, false},
		"with activity, notices unwritable": {true, true},
	} {
		journal, verdict := scenario(t, c.publish, c.broken)
		if verdict != quietVerdict || !reflect.DeepEqual(journal, quiet) {
			t.Errorf("%s: verdict %q, want %q; journal differs:\n got:  %s\n want: %s", name, verdict, quietVerdict,
				strings.Join(journal, "\n       "), strings.Join(quiet, "\n       "))
		}
	}
}
