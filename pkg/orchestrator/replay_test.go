package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// A retried request is answered from the operation its request id committed: the request is compared with it
// in full, and the answer is rebuilt from it, never from the current state.

func requireConflict(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, state.ErrOperationIDConflict) {
		t.Fatalf("%s: err = %v, want a request id conflict", what, err)
	}
}

// TestReplay_OpenThreadAnswersWithTheOpenedThread: a retry after the thread was closed still answers with the
// thread as it was opened.
func TestReplay_OpenThreadAnswersWithTheOpenedThread(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	stage := meshStageParticipantID(assistantsTestStageID)
	req := OpenThreadRequest{OperationID: "op-open", Title: "plan", Members: []agentcomm.ParticipantID{meshUserParticipantID, stage}, Scope: agentcomm.ScopeStage}
	if _, err := o.OpenThread(ctx, req); err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	th, err := o.OpenThread(ctx, req)
	if err != nil {
		t.Fatalf("OpenThread retry: %v", err)
	}
	if err := o.CloseThread(ctx, CloseThreadRequest{OperationID: "op-close", ThreadID: th.ID}); err != nil {
		t.Fatalf("CloseThread: %v", err)
	}

	replay, err := o.OpenThread(ctx, req)
	if err != nil {
		t.Fatalf("OpenThread retry after close: %v", err)
	}
	if replay.Status != agentcomm.ThreadOpen || replay.ID != th.ID || replay.Title != "plan" {
		t.Fatalf("retry after close = %+v, want the original open receipt", replay)
	}
	changed := req
	changed.Title = "another"
	_, err = o.OpenThread(ctx, changed)
	requireConflict(t, "open retry with another title", err)
}

// TestReplay_AttachAndSendComparesTheWholeRequest: a retry with fewer recipients, another first message or no
// first message is not the committed attach.
func TestReplay_AttachAndSendComparesTheWholeRequest(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	stubPassingHelper(o)
	t.Cleanup(o.concurrency.WaitAgents)
	other := addTestRunAgent(t, o, "op-other", nil)
	msg := "hello"
	req := AttachAndSendRequest{
		OperationID: "op-attach", ProfileID: "qa", Scope: assistants.ScopeRun, Title: "Second QA", Instruction: "review",
		FirstMessage: &msg, Recipients: []agentcomm.ParticipantID{other.ParticipantID}, Actor: state.Actor{Kind: "user"},
	}
	first, err := o.AttachAndSend(ctx, req)
	if err != nil {
		t.Fatalf("AttachAndSend: %v", err)
	}
	again, err := o.AttachAndSend(ctx, req)
	if err != nil || again.TaskID != first.TaskID || again.ThreadID != first.ThreadID || again.MessageID == nil || *again.MessageID != *first.MessageID {
		t.Fatalf("retry = %+v, %v; want the original %+v", again, err, first)
	}

	fewer := req
	fewer.Recipients = nil
	_, err = o.AttachAndSend(ctx, fewer)
	requireConflict(t, "retry with fewer recipients", err)

	otherText := "bye"
	reworded := req
	reworded.FirstMessage = &otherText
	_, err = o.AttachAndSend(ctx, reworded)
	requireConflict(t, "retry with another first message", err)

	silent := req
	silent.FirstMessage = nil
	_, err = o.AttachAndSend(ctx, silent)
	requireConflict(t, "retry without the first message", err)
}

// TestReplay_DiscussionPostComparesEveryField: role, round, parent, context version and evidence are part of
// the post; a retry that changes any of them is a conflict.
func TestReplay_DiscussionPostComparesEveryField(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	req := PostMessageRequest{
		OperationID: "op-post", StageID: assistantsTestStageID, AuthorKind: assistants.AuthorHuman, AuthorID: "user",
		Role: "human", Round: assistants.RoundFreeform, ContextVersion: "v1", Evidence: []string{"plan.md"}, Body: "looks good",
		Actor: state.Actor{Kind: "user"},
	}
	id, err := o.PostDiscussionMessage(ctx, req)
	if err != nil {
		t.Fatalf("PostDiscussionMessage: %v", err)
	}
	if again, err := o.PostDiscussionMessage(ctx, req); err != nil || again != id {
		t.Fatalf("retry = %q, %v; want %q", again, err, id)
	}
	for name, change := range map[string]func(*PostMessageRequest){
		"role":            func(r *PostMessageRequest) { r.Role = "reviewer" },
		"context version": func(r *PostMessageRequest) { r.ContextVersion = "v2" },
		"evidence":        func(r *PostMessageRequest) { r.Evidence = []string{"other.md"} },
		"parent":          func(r *PostMessageRequest) { r.ParentMessageID = id },
		"body":            func(r *PostMessageRequest) { r.Body = "not good" },
	} {
		changed := req
		change(&changed)
		_, err := o.PostDiscussionMessage(ctx, changed)
		requireConflict(t, "post retry with another "+name, err)
	}
}

// TestReplay_SendSurvivesANewSecret: a secret that appears after the commit (a newly minted token) does not
// turn a faithful retry into a conflict, and the receipt is the committed one.
func TestReplay_SendSurvivesANewSecret(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	stubPassingHelper(o)
	t.Cleanup(o.concurrency.WaitAgents)
	agent := addTestRunAgent(t, o, "op-add", nil)
	req := SendRequest{
		OperationID: "op-send", ThreadID: agent.ThreadID, Sender: meshUserParticipantID, Recipients: []agentcomm.ParticipantID{agent.ParticipantID},
		Kind: agentcomm.MessageKindMessage, Body: "use the key tok-later-secret", Actor: state.Actor{Kind: "user"},
	}
	first, err := o.Send(ctx, req)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	o.opts.SecretValues = append(o.opts.SecretValues, "tok-later-secret")

	again, err := o.Send(ctx, req)
	if err != nil || again != first {
		t.Fatalf("retry = %+v, %v; want the committed receipt %+v", again, err, first)
	}
	changed := req
	changed.Body = "something else"
	_, err = o.Send(ctx, changed)
	requireConflict(t, "send retry with another body", err)
}

// TestAdmission_SharedByBothAttachEntryPoints: Attach and AttachAndSend admit a task by the same rules — an
// unknown stage, a finished stage and an unknown profile are refused with the same code, and a task without a
// title is named after its profile either way.
func TestAdmission_SharedByBothAttachEntryPoints(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true, flow.Stage{ID: "done-stage", Command: "claude"})
	ctx := context.Background()
	if err := store.Apply(&state.Transition{StageID: "done-stage", From: state.StatusPending, To: state.StatusDone, Event: "test"}); err != nil {
		t.Fatalf("finish stage: %v", err)
	}
	user := state.Actor{Kind: "user"}
	attach := func(opID, stageID string, profile assistants.ProfileID) (assistants.TaskID, error) {
		return o.Attach(ctx, AttachRequest{OperationID: opID, StageID: stageID, ProfileID: profile, Instruction: "x", Actor: user})
	}
	attachAndSend := func(opID, stageID string, profile assistants.ProfileID) (assistants.TaskID, error) {
		res, err := o.AttachAndSend(ctx, AttachAndSendRequest{OperationID: opID, StageID: stageID, ProfileID: profile, Instruction: "x", Actor: user})
		return res.TaskID, err
	}
	for name, entry := range map[string]func(string, string, assistants.ProfileID) (assistants.TaskID, error){"Attach": attach, "AttachAndSend": attachAndSend} {
		for _, tc := range []struct {
			what    string
			stageID string
			profile assistants.ProfileID
			want    assistants.ErrorCode
		}{
			{"unknown stage", "nope", "reviewer", assistants.CodeNotFound},
			{"finished stage", "done-stage", "reviewer", assistants.CodeInvalidStatus},
			{"unknown profile", assistantsTestStageID, "nobody", assistants.CodeCapabilityUnavailable},
		} {
			_, err := entry("op-"+name+"-"+tc.what, tc.stageID, tc.profile)
			var rej *state.OperationRejectedError
			if !errors.As(err, &rej) || rej.Code != tc.want {
				t.Fatalf("%s, %s: err = %v, want %s", name, tc.what, err, tc.want)
			}
		}
		taskID, err := entry("op-"+name+"-ok", assistantsTestStageID, "reviewer")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := store.Snapshot().Assistants.Tasks[taskID].Title; got != "Reviewer" {
			t.Fatalf("%s: task title = %q, want the profile's title", name, got)
		}
	}
}

// TestReplay_RequestIDOfAnotherKindConflicts: a request id committed by one kind of request never answers a
// request of another kind, even when their effects look alike (both attaches create a task).
func TestReplay_RequestIDOfAnotherKindConflicts(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	user := state.Actor{Kind: "user"}
	attach := AttachRequest{OperationID: "op-a", StageID: assistantsTestStageID, ProfileID: "reviewer", Instruction: "x", Actor: user}
	attachAndSend := AttachAndSendRequest{OperationID: "op-a", StageID: assistantsTestStageID, ProfileID: "reviewer", Instruction: "x", Actor: user}

	if _, err := o.AttachAndSend(ctx, attachAndSend); err != nil {
		t.Fatalf("AttachAndSend: %v", err)
	}
	_, err := o.Attach(ctx, attach)
	requireConflict(t, "Attach under an AttachAndSend request id", err)

	attach.OperationID, attachAndSend.OperationID = "op-b", "op-b"
	if _, err := o.Attach(ctx, attach); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	_, err = o.AttachAndSend(ctx, attachAndSend)
	requireConflict(t, "AttachAndSend under an Attach request id", err)

	_, err = o.Publish(ctx, PublishRequest{OperationID: "op-b", Author: meshUserParticipantID, Key: "k", Status: "s", Actor: user})
	requireConflict(t, "Publish under an Attach request id", err)
}
