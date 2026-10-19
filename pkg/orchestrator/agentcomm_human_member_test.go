package orchestrator

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/state"
)

// D1: the human is a member of every NEW conversation — so it can read along and answer in the same thread.

func TestSendFromAgent_NewConversationIncludesTheHuman(t *testing.T) {
	o, store := newTwoStageMeshOrchestrator(t)
	impl := string(meshStageParticipantID(assistantsTestStageID))

	r, err := agentSend(o, "op-ask", "stage:docs", []string{impl}, "question?")
	if err != nil {
		t.Fatalf("SendFromAgent: %v", err)
	}
	th := store.Snapshot().Assistants.Mesh.Threads[r.ThreadID]
	want := []agentcomm.ParticipantID{meshUserParticipantID, "stage:docs", agentcomm.ParticipantID(impl)}
	if th == nil || !slices.Equal(sortedMemberSet(th.Members), sortedMemberSet(want)) {
		t.Fatalf("thread = %+v, want members %v", th, want)
	}
	msg := store.Snapshot().Assistants.Mesh.Messages[r.MessageID]
	if !slices.Equal(msg.Recipients, []agentcomm.ParticipantID{agentcomm.ParticipantID(impl)}) {
		t.Fatalf("recipients = %v, want only the addressed agent — the human reads along, it is not addressed", msg.Recipients)
	}
}

// A conversation the agents opened before D1 (no human in it) is still THE conversation of those agents after
// a resume: the next message goes into it, it does not split into a second thread.
func TestSendFromAgent_ContinuesALegacyConversationWithoutTheHuman(t *testing.T) {
	o, store := newTwoStageMeshOrchestrator(t)
	impl := meshStageParticipantID(assistantsTestStageID)
	legacy, err := o.OpenThread(context.Background(), OpenThreadRequest{
		OperationID: "op-legacy", Scope: agentcomm.ScopeRun, Members: []agentcomm.ParticipantID{"stage:docs", impl},
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}

	r, err := agentSend(o, "op-ask", "stage:docs", []string{string(impl)}, "question?")
	if err != nil {
		t.Fatalf("SendFromAgent: %v", err)
	}
	if r.ThreadID != legacy.ID {
		t.Fatalf("message went to %s, want the legacy conversation %s", r.ThreadID, legacy.ID)
	}
	if n := len(store.Snapshot().Assistants.Mesh.Threads); n != 1 {
		t.Fatalf("threads = %d, want 1", n)
	}
}

// A stage discussion thread {human, stage agent, mentioned agent} has the same agents as a conversation the
// stage agent starts with that agent — but it is the human's Discussion: the agents' own conversation must not
// land there (and so must not leak into the Discussion tab).
func TestSendFromAgent_DoesNotReuseADiscussionThread(t *testing.T) {
	o, store := newTwoStageMeshOrchestrator(t)
	ctx := context.Background()
	impl := meshStageParticipantID(assistantsTestStageID)
	if _, err := o.PostDiscussionMessage(ctx, PostMessageRequest{
		OperationID: "op-post", StageID: assistantsTestStageID, AuthorKind: assistants.AuthorHuman,
		Round: assistants.RoundFreeform, Body: "both of you, look", Mentions: []agentcomm.ParticipantID{"stage:docs"},
		Actor: state.Actor{Kind: "user"},
	}); err != nil {
		t.Fatalf("PostDiscussionMessage: %v", err)
	}
	discussion := discussionThreadID(assistantsTestStageID, []agentcomm.ParticipantID{"stage:docs"})

	r, err := agentSend(o, "op-ask", string(impl), []string{"stage:docs"}, "between us")
	if err != nil {
		t.Fatalf("SendFromAgent: %v", err)
	}
	if r.ThreadID == discussion {
		t.Fatalf("the agents' conversation went into the discussion thread %s", discussion)
	}
	if n := len(o.DiscussionMessages(assistantsTestStageID)); n != 1 {
		t.Fatalf("discussion messages = %d, want only the human's post", n)
	}
	// The next message continues the agents' own conversation, not the discussion.
	r2, err := agentSend(o, "op-answer", "stage:docs", []string{string(impl)}, "agreed")
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	if r2.ThreadID != r.ThreadID {
		t.Fatalf("answer went to %s, want the agents' conversation %s", r2.ThreadID, r.ThreadID)
	}
	if th := store.Snapshot().Assistants.Mesh.Threads[r.ThreadID]; !slices.Contains(th.Members, meshUserParticipantID) {
		t.Fatalf("agents' conversation members = %v, want the human in it", th.Members)
	}
}

func TestPublish_NoticeConversationIncludesTheHuman(t *testing.T) {
	o, store := newTwoStageMeshOrchestrator(t)
	impl := meshStageParticipantID(assistantsTestStageID)
	if _, err := o.Publish(context.Background(), PublishRequest{
		OperationID: "op-publish", Author: "stage:docs", Key: "task/x", Status: "todo",
		Notify: []agentcomm.ParticipantID{impl}, Actor: state.Actor{Kind: "agent", ID: "stage:docs"},
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	inbox := store.Snapshot().Assistants.Mesh.Inbox[impl]
	if len(inbox) != 1 {
		t.Fatalf("notices to impl = %d, want 1", len(inbox))
	}
	msg := store.Snapshot().Assistants.Mesh.Messages[inbox[0]]
	th := store.Snapshot().Assistants.Mesh.Threads[msg.ThreadID]
	if !slices.Contains(th.Members, meshUserParticipantID) {
		t.Fatalf("notice thread members = %v, want the human in it", th.Members)
	}
}

func TestDelegate_ResultThreadIncludesTheHuman(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	var calls atomic.Int32
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if calls.Add(1) == 1 {
			return delegateOutcome(childJSON(assistants.ModeAdvisory)), nil
		}
		return passResultOutcome(), nil
	}

	parentID := attachTestTask(context.Background(), t, o, "op-parent")
	waitForTaskStatus(t, store, parentID, assistants.StatusCompleted, 2*time.Second)
	childID := store.Snapshot().Assistants.ByParent[parentID][0]

	th := store.Snapshot().Assistants.Mesh.Threads[deriveMeshThreadID("delegate-thread:"+string(parentID)+":"+string(childID))]
	if th == nil || !slices.Contains(th.Members, meshUserParticipantID) {
		t.Fatalf("delegation thread = %+v, want the human in it", th)
	}
}

// The human is registered as a run-scoped mesh participant once it joins a thread; the roster still lists it
// exactly once.
func TestRoster_ListsTheHumanOnce(t *testing.T) {
	o, _ := newTwoStageMeshOrchestrator(t)
	if _, err := agentSend(o, "op-ask", "stage:docs", []string{assistantsTestStageID}, "hi"); err != nil {
		t.Fatalf("SendFromAgent: %v", err)
	}
	page, err := o.ListParticipants(context.Background(), ListParticipantsRequest{})
	if err != nil {
		t.Fatalf("ListParticipants: %v", err)
	}
	n := 0
	for _, p := range page.Participants {
		if p.ID == meshUserParticipantID {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the human is listed %d times, want once", n)
	}
}
