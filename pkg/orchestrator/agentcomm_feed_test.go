// Package orchestrator (this file, Agent Mesh Phase 8, T8.2): the Feed scenario — a human's discussion Q&A
// with a stage's own agent (T6.4's discussion-mesh mirror), followed by addressing an EXISTING run-scoped
// role (QA) for a clarification that round-trips back to the stage, asserted to leave the stage's own FSM and
// task tree untouched: no new stage revision, no new helper task beyond QA's own single standing one, and no
// new required gate — only mesh Send/Wait/OpenThread traffic plus the pre-existing discussion mirror.
//
// "User writes @qa ..." is modeled here as AddRunAgent (T6.2b) — the backend primitive the Feed composer's
// mention ultimately calls to reach an existing run-scoped role by profile alias — since this test's own scope
// is the backend chain dev-plan.md's T8.2 prescribes, not the composer's own text-parsing (covered by the
// vitest companion instead, pkg/web/dashboard/src/.../feed-view-model T8.2 fixture).
//
// Package `orchestrator` (white-box), same convention and same reasoning as agentcomm_integration_test.go
// (T8.1): this scenario's resumption is entirely scope=run/scope=stage mesh traffic, driven synchronously from
// Send/Publish's own externalWork, never dependent on Orchestrator.Run's own event loop.
package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// TestFeed_UserDiscussionThenQAClarification_NoRevisionNoHelperNoGate is T8.2's central claim: the chain
// (question -> answer -> @qa -> clarify -> answer -> report -> continue) holds, and all five negative
// assertions (no new stage revision, no new helper task, no new required gate, correct authors/recipients/
// receipts/reply links, the same QA reachable from another stage afterwards) survive it.
func TestFeed_UserDiscussionThenQAClarification_NoRevisionNoHelperNoGate(t *testing.T) {
	const otherStageID = "docs"
	o, store := newAssistantsTestOrchestrator(t, true, flow.Stage{ID: otherStageID, Command: "claude"})
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)

	stageParticipant := meshStageParticipantID(assistantsTestStageID)
	qaParticipant := meshRunParticipantID(deriveTaskID("op-add-qa"))
	qaThreadID := deriveMeshThreadID("op-qa-clarify-open")

	// 1. The user asks the stage's own agent a question through the pre-existing discussion channel (T6.4) —
	// this alone never touches the mesh beyond the stage's own single discussion-thread mirror.
	qID, err := o.PostDiscussionMessage(ctx, PostMessageRequest{
		OperationID: "op-feed-question", StageID: assistantsTestStageID, AuthorKind: assistants.AuthorHuman,
		Round: assistants.RoundFreeform, Body: "what format should the QA report use?", Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("PostDiscussionMessage (question): %v", err)
	}

	// 2. The executor answers BETWEEN STEPS — a plain discussion reply, never a stage FSM transition
	// (discussion.go's own header: "сообщение в обсуждении не меняет статус родительской стадии само по себе").
	aID, err := o.PostDiscussionMessage(ctx, PostMessageRequest{
		OperationID: "op-feed-answer", StageID: assistantsTestStageID, AuthorKind: assistants.AuthorMainAgent,
		Round: assistants.RoundFreeform, ParentMessageID: qID, Body: "let me check with QA first", Actor: state.Actor{Kind: "system"},
	})
	if err != nil {
		t.Fatalf("PostDiscussionMessage (answer): %v", err)
	}

	statusBeforeQA := o.currentStatus(assistantsTestStageID)
	genBeforeQA := store.Snapshot().Assistants.RootGenerations[assistantsTestStageID]
	taskCountBeforeQA := len(store.Snapshot().Assistants.Tasks)

	var qaCalls int
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if assistants.ProfileID(profileID) != "qa" {
			return passResultOutcome(), nil
		}
		qaCalls++
		switch qaCalls {
		case 1:
			// QA asks the stage's own agent a clarifying question.
			th, err := o.OpenThread(context.Background(), OpenThreadRequest{
				OperationID: "op-qa-clarify-open", Scope: agentcomm.ScopeStage,
				Members: []agentcomm.ParticipantID{qaParticipant, stageParticipant}, Actor: state.Actor{Kind: "assistant", ID: taskID},
			})
			if err != nil {
				t.Errorf("OpenThread: %v", err)
				return executor.HelperOutcome{}, err
			}
			if th.ID != qaThreadID {
				t.Errorf("thread id = %s, want %s (deriveMeshThreadID must be deterministic)", th.ID, qaThreadID)
			}
			if _, err := o.Send(context.Background(), SendRequest{
				OperationID: "op-qa-clarify-ask", ThreadID: qaThreadID, Sender: qaParticipant,
				Recipients: []agentcomm.ParticipantID{stageParticipant}, Kind: agentcomm.MessageKindMessage,
				Body: "quick clarification: JSON or markdown for the report?",
			}); err != nil {
				t.Errorf("Send (ask): %v", err)
			}
			if _, err := o.Wait(context.Background(), WaitRequest{
				OperationID: "op-qa-clarify-wait", Waiter: qaParticipant,
				Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: qaThreadID},
			}); err != nil {
				t.Errorf("Wait: %v", err)
			}
			return executor.HelperOutcome{ProcessOK: true}, nil
		case 2:
			// QA reports back into the ORIGINAL discussion thread — the executor then simply "continues"
			// because nothing about the stage's own FSM status was ever touched by any of this.
			if _, err := o.PostDiscussionMessage(context.Background(), PostMessageRequest{
				OperationID: "op-feed-qa-report", StageID: assistantsTestStageID, AuthorKind: assistants.AuthorAssistant,
				AuthorID: "qa", Round: assistants.RoundFreeform, ParentMessageID: aID, Body: "QA says: use JSON",
				Actor: state.Actor{Kind: "assistant", ID: taskID},
			}); err != nil {
				t.Errorf("PostDiscussionMessage (qa report): %v", err)
			}
			return passResultOutcome(), nil
		default:
			t.Errorf("qa invoked unexpectedly a %d-th time", qaCalls)
			return executor.HelperOutcome{}, nil
		}
	}

	// 3. "User writes @qa ..." — the Feed composer's mention, at the backend level, IS AddRunAgent (T6.2b): an
	// idempotent-per-profile AttachAndSend that creates QA ONCE as a run-scoped standing role, addressable by
	// alias from then on regardless of which stage reaches for it (asserted at the very end of this test).
	qaMsg := "@qa please clarify the report format with the impl stage"
	qaRes, err := o.AddRunAgent(ctx, AddRunAgentRequest{OperationID: "op-add-qa",
		ProfileID: "qa", Title: "QA", Instruction: "answer format questions", FirstMessage: &qaMsg, Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatalf("AddRunAgent: %v", err)
	}
	waitForTaskStatus(t, store, qaRes.TaskID, assistants.StatusWaitingPeer, 5*time.Second)

	// 4. The executor answers QA's clarification BETWEEN STEPS too — a plain Send, standing in for "the
	// stage's own current turn produced this", exactly like the discussion mirror's own AuthorMainAgent case,
	// without ever re-triggering the stage's own FSM (no Trigger call anywhere in this test).
	if _, err := o.Send(ctx, SendRequest{
		OperationID: "op-stage-answer-qa", ThreadID: qaThreadID, Sender: stageParticipant,
		Recipients: []agentcomm.ParticipantID{qaParticipant}, Kind: agentcomm.MessageKindMessage,
		Body: "JSON, please", Actor: state.Actor{Kind: "assistant", ID: assistantsTestStageID},
	}); err != nil {
		t.Fatalf("Send (stage answers): %v", err)
	}

	waitForRunAgentTurn(t, store, qaRes.TaskID, 5*time.Second)

	// --- Negative assertions ---
	if got := o.currentStatus(assistantsTestStageID); got != statusBeforeQA {
		t.Fatalf("stage status changed from %v to %v — the QA clarification must never touch the stage's own FSM", statusBeforeQA, got)
	}
	rsAfter := store.Snapshot()
	if got := rsAfter.Assistants.RootGenerations[assistantsTestStageID]; got != genBeforeQA {
		t.Fatalf("stage root generation changed from %d to %d — want no new stage revision", genBeforeQA, got)
	}
	if got := len(rsAfter.Assistants.Tasks); got != taskCountBeforeQA+1 {
		t.Fatalf("task count = %d, want exactly %d (QA's own single standing task, no new helper task)", got, taskCountBeforeQA+1)
	}
	if _, blocked := rsAfter.Assistants.RequiredBlocker(assistantsTestStageID); blocked {
		t.Fatal("stage has a required-task blocker after the exchange — want no new required gate")
	}
	qaTask := rsAfter.Assistants.Tasks[qaRes.TaskID]
	if qaTask == nil || qaTask.Mode != assistants.ModeAdvisory {
		t.Fatalf("QA task mode = %+v, want advisory (never a required gate)", qaTask)
	}

	// --- Positive assertions: authors/recipients/receipts/reply links ---
	discussionMsgs := o.DiscussionMessages(assistantsTestStageID)
	if len(discussionMsgs) != 3 {
		t.Fatalf("discussion has %d messages, want exactly 3 (question, answer, qa's report)", len(discussionMsgs))
	}
	if discussionMsgs[0].ID != qID || discussionMsgs[0].Author.Kind != assistants.AuthorHuman {
		t.Fatalf("discussion[0] = %+v, want the human's own question (id=%s)", discussionMsgs[0], qID)
	}
	if discussionMsgs[1].ID != aID || discussionMsgs[1].Author.Kind != assistants.AuthorMainAgent || discussionMsgs[1].ParentMessageID != qID {
		t.Fatalf("discussion[1] = %+v, want the main agent's reply to %s", discussionMsgs[1], qID)
	}
	if discussionMsgs[2].Author.Kind != assistants.AuthorAssistant || discussionMsgs[2].Author.ID != "qa" || discussionMsgs[2].ParentMessageID != aID {
		t.Fatalf("discussion[2] = %+v, want QA's own report, replying to %s", discussionMsgs[2], aID)
	}

	qaThreadMsgs := messagesInThread(rsAfter, qaThreadID)
	if len(qaThreadMsgs) != 2 {
		t.Fatalf("qa-clarify thread has %d messages, want exactly 2 (ask, answer)", len(qaThreadMsgs))
	}
	if qaThreadMsgs[0].Sender != qaParticipant || len(qaThreadMsgs[0].Recipients) != 1 || qaThreadMsgs[0].Recipients[0] != stageParticipant {
		t.Fatalf("qa's question = %+v, want sender=qa recipient=stage", qaThreadMsgs[0])
	}
	if qaThreadMsgs[1].Sender != stageParticipant || len(qaThreadMsgs[1].Recipients) != 1 || qaThreadMsgs[1].Recipients[0] != qaParticipant {
		t.Fatalf("stage's answer = %+v, want sender=stage recipient=qa", qaThreadMsgs[1])
	}
	if got := rsAfter.Assistants.Mesh.Receipts[qaThreadMsgs[1].ID][qaParticipant]; got == "" {
		t.Fatal("no delivery receipt recorded for qa's own copy of the stage's answer")
	}

	// --- "the same QA reachable from another stage afterwards" ---
	page, err := o.ListParticipants(ctx, ListParticipantsRequest{Actor: state.Actor{Kind: "user"}})
	if err != nil {
		t.Fatalf("ListParticipants: %v", err)
	}
	var qaEntry *agentcomm.Participant
	for i := range page.Participants {
		if page.Participants[i].ID == qaParticipant {
			qaEntry = &page.Participants[i]
		}
	}
	if qaEntry == nil || !qaEntry.Reachable {
		t.Fatalf("qa participant = %+v, want present and reachable after the exchange", qaEntry)
	}
	otherStageParticipant := meshStageParticipantID(otherStageID)
	if _, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-other-stage-reaches-qa", Scope: agentcomm.ScopeRun,
		Members: []agentcomm.ParticipantID{otherStageParticipant, qaParticipant}, Actor: state.Actor{Kind: "user"},
	}); err != nil {
		t.Fatalf("OpenThread from a DIFFERENT stage naming the SAME qa participant: %v", err)
	}

	o.concurrency.WaitAgents()
}
