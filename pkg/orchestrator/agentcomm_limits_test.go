// Package orchestrator (this file, Agent Mesh Phase 8, T8.4): the limits/authority/isolation sweep's
// Service-level half (the HTTP-adapter half lives in pkg/server/agent_mesh_test.go). dev-plan.md's T8.4 asks
// for "each clause [to be] a separate named test" — most of the limits and isolation rules it names were
// already built, and tested, for their own earlier reasons; this header maps every clause to either a NEW
// test in this file or an EXISTING one elsewhere, so the mapping stays traceable without duplicating a
// scenario that already exists:
//
//   - inbox limit produces an explicit refusal: TestMesh_InboxUnreadLimitProducesExplicitRefusal (below).
//   - fan-out limit produces an explicit refusal: TestMesh_FanOutRecipientLimitProducesExplicitRefusal and
//     TestMesh_ThreadMemberFanOutLimitProducesExplicitRefusal (below) — the two independent fan-out caps
//     (MaxRecipients per Send, MaxThreadMembers per OpenThread).
//   - causal-chain limit produces an explicit refusal: TestMesh_WaitMutualCycleRejected
//     (agentcomm_wait_test.go) and TestMesh_ParentChildMeshWaitCycleRejected (agentcomm_recovery_test.go,
//     T8.3) — the mesh-wait cycle check IS the causal-chain limit (agentcomm.MaxCausalHops bounds its own
//     walk), so a THIRD copy of the same mechanism here would be a redundant test, not a new clause.
//   - an exhausted budget never becomes a successful result: TestAttachAndSend_AtomicOrNothing's "exceeded
//     MaxTasks quota" case (agentcomm_attach_test.go) — AttachAndSend writes NOTHING (no task, no
//     participant, no thread) once the budget is exhausted, never a partial or fabricated success.
//   - silence never becomes a successful result: TestMesh_SilenceOnExpiredWaitNeverFabricatesASuccessfulVerdict
//     (below).
//   - a participant cannot forge an author, a generation or a run: see pkg/server/agent_mesh_test.go
//     (TestAgentMesh_TokenIdentityIsNeverClientSupplied, TestAgentMesh_PublishIgnoresClientSuppliedAuthor,
//     TestAgentMesh_RequestBodiesCarryNoSenderActorGenerationOrRunOverride) — forgery is rejected at the HTTP
//     boundary, the only place a client-supplied identity could ever be read from.
//   - a private thread cannot be read by a non-member (the human run owner excepted):
//     TestMesh_ReadThreadMembershipEnforced (agentcomm_messages_test.go) at the Service level, plus
//     TestAgentMesh_PrivateThreadNotReadableByNonMemberHumanExcepted (pkg/server/agent_mesh_test.go) proving
//     the HTTP adapter actually plumbs that rejection through rather than swallowing it.
//   - a peer message cannot create a new root to dodge max_depth or widen capabilities:
//     TestAgentMesh_NoAttachOrCapabilityWideningRouteExposedToAgents (pkg/server/agent_mesh_test.go) — the
//     agent-facing Service interface (AgentMeshActions) has no AttachAndSend method AT ALL, so there is no
//     HTTP path by which an authenticated peer could ever reach AttachAndSend (the only thing that creates a
//     new task/participant) in the first place; every AttachAndSend call, from whatever caller, always
//     computes `assistants.ComputeDepth(nil)` (agentcomm_attach.go) — top-level, never nested under whatever
//     task triggered it — so there is no depth to "dodge" even in principle.
//   - a helper keeps the same workspace whatever created it: TestHelperExecutorConfig_ProjectRootAndScratch
//     (assistants_files_test.go) — helperExecutorConfig (the ONE executor.Config builder every helper task
//     uses, mesh-attached or not) is keyed purely on taskID, so a mesh-created task gets the identical project
//     root and scratch directory; write protection comes from its environment, never from this config.
//   - secrets are absent from persisted messages and from notices: TestMesh_SendRedactsSecretsInBody
//     (agentcomm_messages_test.go) for the persisted message body, and
//     TestMesh_SecretsAbsentFromLiveNoticeAndNoticesJournal (below) for both the live bus notice and the
//     durable notices.jsonl journal.
package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/clock"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// TestMesh_InboxUnreadLimitProducesExplicitRefusal is T8.4's inbox-limit clause: a recipient's own unread
// budget (agentcomm.MaxInboxUnread) is exhausted across TWO separate threads — split deliberately so that
// neither thread's own, independent MaxThreadMessages cap (also 200) is what trips first, isolating the
// per-recipient inbox limit itself. The Send that would push the recipient over the limit must be refused
// explicitly, and must write nothing (no message, no inbox growth) — never a silent drop or truncation.
func TestMesh_InboxUnreadLimitProducesExplicitRefusal(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	recipient := meshStageParticipantID(assistantsTestStageID)
	sender := meshUserParticipantID

	threadA, err := o.OpenThread(ctx, OpenThreadRequest{OperationID: "op-inbox-open-a", Members: []agentcomm.ParticipantID{sender, recipient}, Scope: agentcomm.ScopeStage})
	if err != nil {
		t.Fatalf("OpenThread A: %v", err)
	}
	threadB, err := o.OpenThread(ctx, OpenThreadRequest{OperationID: "op-inbox-open-b", Members: []agentcomm.ParticipantID{sender, recipient}, Scope: agentcomm.ScopeStage})
	if err != nil {
		t.Fatalf("OpenThread B: %v", err)
	}

	half := agentcomm.MaxInboxUnread / 2
	for i := 0; i < half; i++ {
		if _, err := o.Send(ctx, SendRequest{
			OperationID: fmt.Sprintf("op-inbox-a-%d", i), ThreadID: threadA.ID, Sender: sender,
			Recipients: []agentcomm.ParticipantID{recipient}, Kind: agentcomm.MessageKindMessage, Body: "x",
		}); err != nil {
			t.Fatalf("Send A#%d: %v", i, err)
		}
	}
	for i := 0; i < half; i++ {
		if _, err := o.Send(ctx, SendRequest{
			OperationID: fmt.Sprintf("op-inbox-b-%d", i), ThreadID: threadB.ID, Sender: sender,
			Recipients: []agentcomm.ParticipantID{recipient}, Kind: agentcomm.MessageKindMessage, Body: "x",
		}); err != nil {
			t.Fatalf("Send B#%d: %v", i, err)
		}
	}

	before := len(store.Snapshot().Assistants.Mesh.Inbox[recipient])
	if before != agentcomm.MaxInboxUnread {
		t.Fatalf("test setup: recipient inbox = %d, want exactly MaxInboxUnread (%d)", before, agentcomm.MaxInboxUnread)
	}

	_, err = o.Send(ctx, SendRequest{
		OperationID: "op-inbox-overflow", ThreadID: threadB.ID, Sender: sender,
		Recipients: []agentcomm.ParticipantID{recipient}, Kind: agentcomm.MessageKindMessage, Body: "one too many",
	})
	if err == nil || !strings.Contains(err.Error(), "MaxInboxUnread") {
		t.Fatalf("Send past MaxInboxUnread: want an explicit refusal mentioning MaxInboxUnread, got %v", err)
	}
	if got := len(store.Snapshot().Assistants.Mesh.Inbox[recipient]); got != agentcomm.MaxInboxUnread {
		t.Fatalf("the refused Send still wrote to the inbox: %d, want unchanged %d", got, agentcomm.MaxInboxUnread)
	}
}

// TestMesh_FanOutRecipientLimitProducesExplicitRefusal is T8.4's fan-out clause for a single Send's own
// recipient list (agentcomm.MaxRecipients): ValidateMessage's own structural check fires before any
// membership/roster lookup at all (agentcomm/validate.go), so bogus/unregistered ids are enough to exercise
// it — the point is the explicit refusal and the "writes nothing" guarantee, not roster resolution.
func TestMesh_FanOutRecipientLimitProducesExplicitRefusal(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	helperID := meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-fanout-sender"))

	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-fanout-open", Members: []agentcomm.ParticipantID{meshUserParticipantID, helperID}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}

	recipients := make([]agentcomm.ParticipantID, agentcomm.MaxRecipients+1)
	for i := range recipients {
		recipients[i] = agentcomm.ParticipantID(fmt.Sprintf("helper:bogus-%d", i))
	}

	before := len(store.Snapshot().Assistants.Mesh.Messages)
	_, err = o.Send(ctx, SendRequest{
		OperationID: "op-fanout-send", ThreadID: thread.ID, Sender: meshUserParticipantID,
		Recipients: recipients, Kind: agentcomm.MessageKindMessage, Body: "spam",
	})
	if err == nil || !strings.Contains(err.Error(), "too many recipients") {
		t.Fatalf("Send with %d recipients: want an explicit 'too many recipients' refusal, got %v", len(recipients), err)
	}
	if got := len(store.Snapshot().Assistants.Mesh.Messages); got != before {
		t.Fatalf("the refused fan-out Send still wrote a message: before=%d after=%d", before, got)
	}
}

// TestMesh_ThreadMemberFanOutLimitProducesExplicitRefusal is T8.4's fan-out clause for a thread's own FIXED
// member set (agentcomm.MaxThreadMembers): unlike Send's recipient check, this one needs genuinely resolvable
// participant ids (ensureParticipantEffects resolves each member against the live roster before the
// operation is even built) — eight real, terminal helper tasks plus the human make nine, one over the limit.
func TestMesh_ThreadMemberFanOutLimitProducesExplicitRefusal(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return passResultOutcome(), nil
	}

	members := []agentcomm.ParticipantID{meshUserParticipantID}
	for i := 0; i < agentcomm.MaxThreadMembers; i++ {
		taskID := attachTestTask(ctx, t, o, fmt.Sprintf("op-members-%d", i))
		members = append(members, meshHelperParticipantID(taskID))
	}
	if len(members) != agentcomm.MaxThreadMembers+1 {
		t.Fatalf("test setup: built %d members, want %d", len(members), agentcomm.MaxThreadMembers+1)
	}

	before := len(store.Snapshot().Assistants.Mesh.ThreadOrder)
	_, err := o.OpenThread(ctx, OpenThreadRequest{OperationID: "op-members-open", Members: members, Scope: agentcomm.ScopeStage})
	if err == nil || !strings.Contains(err.Error(), "MaxThreadMembers") {
		t.Fatalf("OpenThread with %d members: want an explicit MaxThreadMembers refusal, got %v", len(members), err)
	}
	if got := len(store.Snapshot().Assistants.Mesh.ThreadOrder); got != before {
		t.Fatalf("the refused OpenThread still created a thread: before=%d after=%d", before, got)
	}

	o.concurrency.WaitAgents()
}

// TestMesh_SilenceOnExpiredWaitNeverFabricatesASuccessfulVerdict is T8.4's "silence ... never becomes a
// successful result" clause: a helper's wait on a colleague who never replies expires (T5.6); the orchestrator
// only resumes the task for ANOTHER real turn (resumeTaskIfWaitCleared), it never invents an outcome on the
// waiter's behalf. The resumed turn here explicitly reports its own HONEST non-pass verdict ("nobody ever
// replied") — the task must finish with exactly that verdict, never silently upgraded to pass just because a
// wait ended.
func TestMesh_SilenceOnExpiredWaitNeverFabricatesASuccessfulVerdict(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	fc := clock.NewFake(time.Now())
	o.clock = fc
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)

	taskID := deriveTaskID("op-silence-1")
	helperID := meshHelperParticipantID(taskID)
	var calls int
	o.runHelperAgent = func(ctx context.Context, tid, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		if tid != string(taskID) {
			return passResultOutcome(), nil
		}
		calls++
		if calls == 1 {
			deadline := fc.Now().Add(time.Minute)
			if _, err := o.Wait(context.Background(), WaitRequest{
				OperationID: "op-silence-1-wait", Waiter: helperID, Deadline: &deadline,
				Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: "t-nobody-ever-replies"},
			}); err != nil {
				t.Errorf("Wait: %v", err)
			}
			return executor.HelperOutcome{ProcessOK: true}, nil
		}
		// The resumed turn, now that the original request has gone unanswered (expired) — the agent's OWN
		// verdict, never one the orchestrator invents.
		return executor.HelperOutcome{ProcessOK: true, RawText: `{"schema_version":1,"kind":"result","verdict":"needs_changes","summary":"nobody ever replied"}`}, nil
	}

	attachTestTask(ctx, t, o, "op-silence-1")
	waitForTaskStatus(t, store, taskID, assistants.StatusWaitingPeer, 2*time.Second)

	fc.BlockUntil(1)
	fc.Advance(time.Minute)

	final := waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)
	if final.LastVerdict == assistants.VerdictPass {
		t.Fatalf("task verdict = %q after a silently-expired wait, want never fabricated as pass", final.LastVerdict)
	}
	if final.LastVerdict != assistants.VerdictNeedsChanges {
		t.Fatalf("task verdict = %q, want the agent's own honest needs_changes", final.LastVerdict)
	}
	o.concurrency.WaitAgents()
}

// TestMesh_SecretsAbsentFromLiveNoticeAndNoticesJournal is T8.4's "secrets are absent from ... notices"
// clause (the "persisted messages" half is TestMesh_SendRedactsSecretsInBody, agentcomm_messages_test.go):
// Send's own EventMeshMessage notice (agentcomm_messages.go) carries only message_id/thread_id/sender_id/kind
// by construction — never Body — so the secret cannot reach either the LIVE bus subscriber or the durable
// notices.jsonl journal publishNotice also writes to. This proves both surfaces directly rather than trusting
// the construction by inspection alone.
func TestMesh_SecretsAbsentFromLiveNoticeAndNoticesJournal(t *testing.T) {
	const secretValue = "sk-test-notice-secret-98765" //nolint:gosec // test fixture value, not a real credential
	t.Setenv("ANTHROPIC_API_KEY", secretValue)

	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	taskID := newRunningHelperForMesh(t, o, store, "op-notice-secret")
	helperID := meshHelperParticipantID(taskID)

	subID, events := o.ui.Subscribe(16)
	defer o.ui.Unsubscribe(subID)

	thread, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-notice-secret-open", Members: []agentcomm.ParticipantID{meshUserParticipantID, helperID}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	if _, err := o.Send(ctx, SendRequest{
		OperationID: "op-notice-secret-send", ThreadID: thread.ID, Sender: meshUserParticipantID,
		Recipients: []agentcomm.ParticipantID{helperID}, Kind: agentcomm.MessageKindMessage,
		Body: "token is " + secretValue,
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	var sawMeshMessage bool
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !sawMeshMessage {
		select {
		case ev := <-events:
			if ev.Type == bus.EventMeshMessage {
				sawMeshMessage = true
				raw, _ := json.Marshal(ev.Data)
				if strings.Contains(string(raw), secretValue) {
					t.Fatalf("live EventMeshMessage notice contains the raw secret: %s", raw)
				}
			}
		case <-time.After(10 * time.Millisecond):
		}
	}
	if !sawMeshMessage {
		t.Fatal("expected an EventMeshMessage notice to be published")
	}

	data, err := os.ReadFile(filepath.Join(o.opts.RunDir, "notices.jsonl"))
	if err != nil {
		t.Fatalf("read notices.jsonl: %v", err)
	}
	if strings.Contains(string(data), secretValue) {
		t.Fatal("notices.jsonl contains the raw secret")
	}
}

// TestMesh_RedactsSecretsOutsideTheEnvironment: secrets.env values and minted mesh tokens never appear in afm's
// own environment, yet they must be redacted from mesh text like environment secrets are — in a message, a
// board status, and in both copies of a discussion post.
func TestMesh_RedactsSecretsOutsideTheEnvironment(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	ctx := context.Background()
	activateStage(t, o, assistantsTestStageID)
	const fromSecretsEnv = "secrets-env-value-1234567" //nolint:gosec // test fixture value, not a real credential
	o.opts.SecretValues = []string{fromSecretsEnv}
	token, err := o.meshTokens().mint(meshStageParticipantID(assistantsTestStageID))
	if err != nil {
		t.Fatal(err)
	}
	leaked := func(text string) bool { return strings.Contains(text, fromSecretsEnv) || strings.Contains(text, token) }
	text := "creds: " + fromSecretsEnv + " and " + token

	stage := meshStageParticipantID(assistantsTestStageID)
	thread, err := o.OpenThread(ctx, OpenThreadRequest{OperationID: "op-red-open", Members: []agentcomm.ParticipantID{meshUserParticipantID, stage}, Scope: agentcomm.ScopeStage})
	if err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	sent, err := o.Send(ctx, SendRequest{OperationID: "op-red-send", ThreadID: thread.ID, Sender: meshUserParticipantID, Recipients: []agentcomm.ParticipantID{stage}, Kind: agentcomm.MessageKindMessage, Body: text})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	entry, err := o.Publish(ctx, PublishRequest{OperationID: "op-red-board", Author: meshUserParticipantID, Key: "k", Status: text, Actor: state.Actor{Kind: "user"}})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	postID, err := o.PostDiscussionMessage(ctx, PostMessageRequest{OperationID: "op-red-post", StageID: assistantsTestStageID, AuthorKind: assistants.AuthorHuman, Round: assistants.RoundFreeform, Body: text, Actor: state.Actor{Kind: "user"}})
	if err != nil {
		t.Fatalf("PostDiscussionMessage: %v", err)
	}

	rs := store.Snapshot()
	for what, got := range map[string]string{
		"message":                rs.Assistants.Mesh.Messages[sent.MessageID].Body,
		"board status":           entry.Status,
		"discussion post":        rs.Assistants.Discussions.Messages[postID].Body,
		"discussion mesh mirror": rs.Assistants.Mesh.Messages[agentcomm.MessageID(postID)].Body,
	} {
		if got == "" || leaked(got) {
			t.Fatalf("%s = %q, want the secrets.env value and the mesh token redacted", what, got)
		}
	}
	journal, err := os.ReadFile(filepath.Join(o.opts.RunDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if leaked(string(journal)) {
		t.Fatal("events.jsonl carries a secret outside the environment")
	}
}
