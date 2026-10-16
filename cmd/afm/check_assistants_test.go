package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// appendControlLine appends one raw state.Control record to runDir/events.jsonl with the given seq — the
// same "mixed Transition/Control lines in one log" shape AppendControl itself produces, built by hand here
// (package main cannot reach pkg/state's unexported AppendControl machinery convenience wrappers used by
// pkg/state's own tests) so these tests can seed a required-task barrier without spinning up a full
// orchestrator.
func appendControlLine(t *testing.T, runDir string, seq uint64, c state.Control) {
	t.Helper()
	c.Seq = seq
	c.SchemaVersion = state.ControlSchemaVersion
	if c.Time.IsZero() {
		c.Time = time.Now()
	}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	f, err := os.OpenFile(filepath.Join(runDir, "events.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
}

// TestCheck_ShowsAwaitingAssistantsAndBlockingTask — T11.5: a stage stuck in awaiting_assistants must name
// its actual blocker (task id, status, profile) in `afm check`'s output, not just the bare status word —
// before this test, check.go never touched rs.Assistants at all (see dev-plan.md's T11.5 code-map notes).
func TestCheck_ShowsAwaitingAssistantsAndBlockingTask(t *testing.T) {
	chdirTemp(t)
	runDir := makeRunState(t, "flow-20260101-120000", "impl", state.StatusAwaitingAssistants)

	appendControlLine(t, runDir, 2, state.Control{
		Kind:   "task_created",
		Actor:  state.Actor{Kind: "user"},
		Target: state.Target{TaskID: "sec-1", StageID: "impl"},
		Payload: mustJSON(t, state.TaskCreatedPayload{
			ProfileID: "security-reviewer",
			Title:     "Security pass on the new endpoint",
			Mode:      "required",
			CreatedBy: "user",
		}),
	})
	appendControlLine(t, runDir, 3, state.Control{
		Kind:    "task_status_changed",
		Target:  state.Target{TaskID: "sec-1"},
		Payload: mustJSON(t, state.TaskStatusChangedPayload{Status: "failed", Reason: "helper process crashed"}),
	})

	stdout := captureStdout(t, func() {
		cmd := newCheckCmd()
		if err := cmd.Execute(); err != nil {
			t.Fatalf("check: %v", err)
		}
	})

	if !strings.Contains(stdout, "awaiting_assistants") {
		t.Fatalf("expected the bare status in output, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "sec-1") {
		t.Fatalf("expected the blocking task id sec-1 in output, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "security-reviewer") {
		t.Fatalf("expected the blocking task's profile id in output, got:\n%s", stdout)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestCheck_ReadsDynamicStageIds — T11.5: a stage added at runtime via AddStageToGraph (Stage 9/T9.4) must
// show up in `afm check`'s output even though flow.yaml on disk was never edited — check.go is log-driven
// (state.LoadRunState replays events.jsonl), not flow.yaml-driven, so this pins that invariant rather than
// fixing a regression.
func TestCheck_ReadsDynamicStageIds(t *testing.T) {
	chdirTemp(t)
	runDir := filepath.Join(".afm", "runs", "flow-20260101-120000")

	store, err := state.Open(runDir, []string{"a"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	op := state.StageGraphOperation{
		ID: "add-n",
		Payload: state.StageGraphPayload{
			Stage: flow.Stage{ID: "n", DependsOn: []string{"a"}},
			Order: []string{"a", "n"},
		},
	}
	if _, err := store.AddStageToGraph(op); err != nil {
		t.Fatalf("AddStageToGraph: %v", err)
	}
	store.Close()

	stdout := captureStdout(t, func() {
		cmd := newCheckCmd()
		if err := cmd.Execute(); err != nil {
			t.Fatalf("check: %v", err)
		}
	})

	if !strings.Contains(stdout, "\nn ") && !strings.Contains(stdout, "\nn\t") {
		// The dynamically-added stage id must appear as its own row, not just incidentally as a substring
		// (e.g. inside "run" or another id) — check for the id as the first column of some line.
		found := false
		for _, line := range strings.Split(stdout, "\n") {
			fields := strings.Fields(line)
			if len(fields) > 0 && fields[0] == "n" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected dynamically-added stage %q as a row in check output, got:\n%s", "n", stdout)
		}
	}
}
