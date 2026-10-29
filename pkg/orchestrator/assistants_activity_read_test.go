package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/state"
)

func TestTaskActivity_ReadsOnlyThePublishedHistoryOfEachTask(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	a := attachTestTask(context.Background(), t, o, "op-a")
	b := attachTestTask(context.Background(), t, o, "op-b")
	run := addTestRunAgent(t, o, "op-run", nil)
	activityOf(t, o, a)("Bash", "ls")
	activityOf(t, o, b)("text", "from b")
	activityOf(t, o, run.TaskID)("text", "standing role")
	activityOf(t, o, a)("Bash", "ls")

	for task, want := range map[assistants.TaskID]int{a: 2, b: 1, run.TaskID: 1} {
		page, err := o.TaskActivity(task, stagefiles.ActivityQuery{Limit: 10})
		if err != nil {
			t.Fatalf("TaskActivity(%s): %v", task, err)
		}
		if len(page.Entries) != want {
			t.Fatalf("task %s: entries = %d, want %d", task, len(page.Entries), want)
		}
		for _, e := range page.Entries {
			if e.TaskID != string(task) {
				t.Errorf("task %s page holds %s's entry", task, e.TaskID)
			}
		}
	}
	runPage, _ := o.TaskActivity(run.TaskID, stagefiles.ActivityQuery{Limit: 10})
	if runPage.Entries[0].StageID != "" {
		t.Errorf("run-scoped entry stage = %q, want empty", runPage.Entries[0].StageID)
	}
}

func TestTaskActivity_TaskWithoutHistoryIsEmptyNotMissing(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	a := attachTestTask(context.Background(), t, o, "op-a")
	page, err := o.TaskActivity(a, stagefiles.ActivityQuery{Limit: 10})
	if err != nil || len(page.Entries) != 0 {
		t.Fatalf("page, err = %+v, %v; want an empty page", page, err)
	}
}

func TestTaskActivity_UnknownTaskIsNotFoundEvenWhenTheFileHasItsID(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	stagefiles.AppendNotice(o.opts.RunDir, "", "assistant_activity",
		stagefiles.ActivityData{EventID: "act-1", TaskID: "t-ghost", Detail: "left over from another run"})

	_, err := o.TaskActivity("t-ghost", stagefiles.ActivityQuery{Limit: 10})
	var rej *state.OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != assistants.CodeNotFound {
		t.Fatalf("err = %v, want a not_found rejection", err)
	}
}

func TestTaskActivity_BadCursorIsReported(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	a := attachTestTask(context.Background(), t, o, "op-a")
	activityOf(t, o, a)("text", "x")
	bad := uint64(3)
	if _, err := o.TaskActivity(a, stagefiles.ActivityQuery{Limit: 10, After: &bad}); !errors.Is(err, stagefiles.ErrInvalidCursor) {
		t.Fatalf("err = %v, want ErrInvalidCursor", err)
	}
}
