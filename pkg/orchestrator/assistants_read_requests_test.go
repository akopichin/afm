package orchestrator

import (
	"context"
	"testing"
)

// A helper with no requests reads as an empty queue; an unknown task id is reported as such, not as an empty one.
func TestTaskRequests_EmptyQueueOfARestingOnMessageHelper(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	taskID, err := o.Attach(context.Background(), onMessageAttachRequest("op-queue"))
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	queue, ok := o.TaskRequests(taskID)
	if !ok || queue != (TaskRequestQueue{}) {
		t.Fatalf("TaskRequests(%s) = (%+v, %v), want an empty queue", taskID, queue, ok)
	}
	if _, ok := o.TaskRequests("t-unknown"); ok {
		t.Fatal("an unknown task has no request queue")
	}
}
