package server

// T11.2 (§19.4) — TestHistory_DozensOfAssistantsDoNotMakeStatusPollScanAllLogs: dozens of helper tasks
// attached to a run must not make building /api/status's StageViews scale with the size (or even the
// existence) of any task's own on-disk log files (<runDir>/assistants/<taskID>/agent.log, agent.jsonl,
// result.json — see pkg/orchestrator/assistants_files.go's doc comment). buildStageViews' awaiting-assistants
// computation (state.AssistantsState.NeedsHumanDecision/RequiredBlocker) must be driven purely by the
// in-memory projection already carried on state.RunState — see dev-plan.md's T11.2 code-map notes.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/state"
)

func TestHistory_DozensOfAssistantsDoNotMakeStatusPollScanAllLogs(t *testing.T) {
	const stageID = "impl"
	const taskCount = 40 // "dozens"

	dir := t.TempDir()
	store, err := state.Open(dir, []string{stageID})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	defer store.Close()

	seq := uint64(1)
	appendControl := func(c state.Control) {
		t.Helper()
		c.Seq = seq
		seq++
		c.SchemaVersion = state.ControlSchemaVersion
		if c.Time.IsZero() {
			c.Time = time.Now()
		}
		if err := store.AppendControl(&c); err != nil {
			t.Fatalf("AppendControl(%s): %v", c.Kind, err)
		}
	}

	// Attach "dozens" of required tasks to the SAME stage — all but the last already closed their gate
	// (completed, no verdict needed for this projection path), the LAST one failed and is what the barrier is
	// actually waiting on. Deliberately NOT creating <dir>/assistants/<taskID>/* at all for ANY of them: if
	// buildStageViews ever needed to open/read a task's on-disk log to answer "is this stage blocked", this
	// test would fail outright (file not found) rather than merely run slow — a stronger, non-flaky guarantee
	// than a wall-clock timing assertion.
	for i := 0; i < taskCount; i++ {
		taskID := "t" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		appendControl(state.Control{
			Kind:   "task_created",
			Actor:  state.Actor{Kind: "user"},
			Target: state.Target{TaskID: taskID, StageID: stageID},
			Payload: mustMarshalControl(t, state.TaskCreatedPayload{
				ProfileID: "reviewer", Title: "check " + taskID, Mode: "required", CreatedBy: "user",
			}),
		})
		status := "completed"
		if i == taskCount-1 {
			status = "failed" // the one genuine blocker
		}
		appendControl(state.Control{
			Kind:    "task_status_changed",
			Target:  state.Target{TaskID: taskID},
			Payload: mustMarshalControl(t, state.TaskStatusChangedPayload{Status: status}),
		})
	}

	if err := store.Apply(&state.Transition{
		StageID: stageID, From: state.StatusPending, To: state.StatusAwaitingAssistants, Event: "main_done",
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	rs := store.Snapshot()
	views := buildStageViews(rs, dir, map[string]StageConfig{stageID: {}}, nil)

	var view *StageView
	for i := range views {
		if views[i].ID == stageID {
			view = &views[i]
		}
	}
	if view == nil {
		t.Fatalf("stage %q missing from StageViews", stageID)
	}
	if view.Status != state.StatusAwaitingAssistants {
		t.Fatalf("status = %v, want awaiting_assistants", view.Status)
	}
	if !view.AwaitingAssistantsBlocked {
		t.Fatal("AwaitingAssistantsBlocked = false, want true — the last (failed) task should still be blocking, " +
			"computed purely from the in-memory projection despite NO on-disk log for any of the 40 tasks existing")
	}
}

func mustMarshalControl(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
