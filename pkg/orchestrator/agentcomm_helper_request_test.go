package orchestrator

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

func journalRecord(t *testing.T, kind string, payload any) state.TaskEvent {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal %s: %v", kind, err)
	}
	return state.TaskEvent{Kind: kind, Payload: raw}
}

// An on_message helper's journal after two answered requests, one failed one, one running and one waiting.
func onMessageHelperHistory(t *testing.T) (state.TaskState, []state.TaskEvent) {
	t.Helper()
	task := state.TaskState{
		Turn:            4,
		ActiveRequest:   "m-active",
		PendingRequests: []agentcomm.MessageID{"m-pending"},
	}
	events := []state.TaskEvent{
		journalRecord(t, taskResultEffectKind, state.TaskResultPayload{
			Result: assistants.Result{Summary: "first answer"}, RequestID: "m-first", Turn: 1,
		}),
		journalRecord(t, taskStatusChangedEffectKind, state.TaskStatusChangedPayload{
			Status: "watching", Turn: 1, RequestID: "m-first", RequestOutcome: state.RequestOutcomeResult,
		}),
		journalRecord(t, taskStatusChangedEffectKind, state.TaskStatusChangedPayload{
			Status: "watching", Reason: "agent crashed", Turn: 2, RequestID: "m-broken", RequestOutcome: state.RequestOutcomeFailed,
		}),
		journalRecord(t, taskResultEffectKind, state.TaskResultPayload{
			Result: assistants.Result{Summary: "second answer"}, RequestID: "m-second", Turn: 3,
		}),
		journalRecord(t, taskStatusChangedEffectKind, state.TaskStatusChangedPayload{
			Status: "running", Turn: 4, RequestID: "m-active",
		}),
	}
	return task, events
}

func TestHelperRequestOf_ReportsEachStageOfARequest(t *testing.T) {
	task, events := onMessageHelperHistory(t)

	cases := []struct {
		id         agentcomm.MessageID
		wantState  HelperRequestState
		wantTurn   uint64
		wantReason string
		wantResult string
	}{
		{"m-pending", HelperRequestPending, 0, "", ""},
		{"m-active", HelperRequestActive, 4, "", ""},
		{"m-first", HelperRequestResult, 1, "", "first answer"},
		{"m-second", HelperRequestResult, 3, "", "second answer"},
		{"m-broken", HelperRequestFailed, 2, "agent crashed", ""},
	}
	for _, tc := range cases {
		t.Run(string(tc.id), func(t *testing.T) {
			got, result, found := helperRequestOf(task, events, tc.id)
			if !found {
				t.Fatalf("request %s not found", tc.id)
			}
			if got.ID != tc.id || got.State != tc.wantState || got.Turn != tc.wantTurn || got.Reason != tc.wantReason {
				t.Fatalf("request = %+v, want state %s, turn %d, reason %q", got, tc.wantState, tc.wantTurn, tc.wantReason)
			}
			if tc.wantResult == "" && result != nil {
				t.Fatalf("result = %s, want none", result)
			}
			if tc.wantResult != "" && !strings.Contains(string(result), tc.wantResult) {
				t.Fatalf("result = %s, want it to carry %q", result, tc.wantResult)
			}
		})
	}
}

// The turn-start record of a request carries its id but no outcome: it is not an answer.
func TestHelperRequestOf_UnknownRequestIsNotFound(t *testing.T) {
	task, events := onMessageHelperHistory(t)
	task.ActiveRequest = ""

	for _, id := range []agentcomm.MessageID{"m-active", "m-of-another-helper", ""} {
		if got, _, found := helperRequestOf(task, events, id); found {
			t.Fatalf("request %q = %+v, want not found", id, got)
		}
	}
}

func TestHelperResult_RequestSelectorNeedsAnOnMessageHelper(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	installGatedHelpers(o, func(string) (executor.HelperOutcome, error) { return passResultOutcome(), nil })
	oneShot := mustSpawn(t, o, "op-sel-one-shot", "look")

	onMessageReq := spawnReq("op-sel-on-message", "answer questions")
	onMessageReq.Trigger = assistants.TriggerOnMessage
	onMessage, err := o.SpawnHelper(context.Background(), onMessageReq)
	if err != nil {
		t.Fatalf("SpawnHelper: %v", err)
	}

	_, err = o.HelperResult(spawnStageActor, oneShot.TaskID, "mm-any")
	requireRejection(t, err, assistants.CodeInvalidRequest)

	_, err = o.HelperResult(spawnStageActor, onMessage.TaskID, "mm-never-sent")
	requireRejection(t, err, assistants.CodeNotFound)
}

func TestHelperResult_RequestSelectorViewCarriesTheRequestAndGeneration(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	countHelperRuns(o)
	req := spawnReq("op-sel-view", "answer questions")
	req.Trigger = assistants.TriggerOnMessage
	res, err := o.SpawnHelper(context.Background(), req)
	if err != nil {
		t.Fatalf("SpawnHelper: %v", err)
	}

	view, err := o.HelperResult(spawnStageActor, res.TaskID, "")
	if err != nil {
		t.Fatalf("HelperResult: %v", err)
	}
	if view.Request != nil {
		t.Fatalf("Request = %+v, want none without a selector", view.Request)
	}
	if want := store.Snapshot().Assistants.Tasks[res.TaskID].RootGeneration; view.Generation != want {
		t.Fatalf("Generation = %d, want %d", view.Generation, want)
	}
}

// The stage reads its own on_message helpers however they came: spawned by its agent or attached by the human.
func TestHelperResult_StageReadsItsOnMessageHelpers(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	countHelperRuns(o)
	spawnedReq := spawnReq("op-acc-spawned", "answer questions")
	spawnedReq.Trigger = assistants.TriggerOnMessage
	spawned, err := o.SpawnHelper(context.Background(), spawnedReq)
	if err != nil {
		t.Fatalf("SpawnHelper: %v", err)
	}
	attached, err := o.Attach(context.Background(), onMessageAttachRequest("op-acc-attached"))
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	for name, taskID := range map[string]assistants.TaskID{"spawned": spawned.TaskID, "attached": attached} {
		t.Run(name, func(t *testing.T) {
			view, err := o.HelperResult(spawnStageActor, taskID, "")
			if err != nil {
				t.Fatalf("HelperResult: %v", err)
			}
			if view.TaskID != taskID || view.Status != assistants.StatusWatching {
				t.Fatalf("view = %+v, want the helper resting in watching", view)
			}
			_, err = o.HelperResult(spawnStageActor, taskID, "mm-never-sent")
			requireRejection(t, err, assistants.CodeNotFound)
		})
	}
}

// Another stage's agent and a stage's own agent after a fresh attempt (Revise) learn nothing of the helper.
func TestHelperResult_OnMessageHelperHiddenFromOtherStageAndOldGeneration(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	installGatedHelpers(o, func(string) (executor.HelperOutcome, error) { return passResultOutcome(), nil })
	oneShot := mustSpawn(t, o, "op-gen-one-shot", "look")
	attached, err := o.Attach(context.Background(), onMessageAttachRequest("op-gen-attached"))
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	otherStage := state.Actor{Kind: "agent", ID: string(meshStageParticipantID("other"))}

	for name, taskID := range map[string]assistants.TaskID{"one-shot": oneShot.TaskID, "on_message": attached} {
		t.Run(name, func(t *testing.T) {
			_, err := o.HelperResult(otherStage, taskID, "")
			requireRejection(t, err, assistants.CodeNotFound)
		})
	}
	if err := o.BumpRootGeneration(assistantsTestStageID); err != nil {
		t.Fatalf("BumpRootGeneration: %v", err)
	}
	for name, taskID := range map[string]assistants.TaskID{"one-shot": oneShot.TaskID, "on_message": attached} {
		t.Run("old generation "+name, func(t *testing.T) {
			_, err := o.HelperResult(spawnStageActor, taskID, "")
			requireRejection(t, err, assistants.CodeNotFound)
		})
	}
}

// <your_helpers> lists the stage's on_message helpers too, attached ones included, so the agent can address them.
func TestStageInbox_ListsOnMessageHelpers(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	countHelperRuns(o)
	attached, err := o.Attach(context.Background(), onMessageAttachRequest("op-list-attached"))
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	block := o.agentCommInboxBlock(flow.Stage{ID: assistantsTestStageID})
	if !strings.Contains(block, "<your_helpers>") || !strings.Contains(block, string(attached)) {
		t.Fatalf("block lacks the attached on_message helper %s:\n%s", attached, block)
	}
}
