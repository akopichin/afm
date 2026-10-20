package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
)

// ---- T1.3: waits ----

func meshWaitRegisteredControl(waitID, waiter string, cond agentcomm.WaitCondition, targetTaskID, targetStageID string, generation uint64) *Control {
	payload, _ := json.Marshal(MeshWaitRegisteredPayload{Waiter: waiter, Condition: cond, Generation: generation})
	return &Control{
		Kind: kindMeshWaitRegistered, SchemaVersion: ControlSchemaVersion,
		Target: Target{WaitID: waitID, TaskID: targetTaskID, StageID: targetStageID}, Payload: payload,
	}
}

func meshWaitResolvedControl(waitID, status, reason string) *Control {
	payload, _ := json.Marshal(MeshWaitResolvedPayload{Status: status, Reason: reason})
	return &Control{
		Kind: kindMeshWaitResolved, SchemaVersion: ControlSchemaVersion,
		Target: Target{WaitID: waitID}, Payload: payload,
	}
}

// TestMesh_WaitRegisteredAlreadySatisfiedIsReadyImmediately: the reply a WaitOnAddressedMessage wait is
// waiting for already exists in the thread BEFORE the wait is registered — the classic race
// applyMeshWaitRegistered's doc comment describes. The wait must be created directly in WaitReady status,
// visible via ReadyWaits() on the very next call, with no separate catch-up pass needed.
func TestMesh_WaitRegisteredAlreadySatisfiedIsReadyImmediately(t *testing.T) {
	as := setupMeshThread(t, "a", "b")
	mustApply(t, &as, meshMessageSentControl(1, "m1", "th-1", "a", []string{"b"}, string(agentcomm.MessageKindMessage), nil, "ping"))

	cond := agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: "th-1"}
	mustApply(t, &as, meshWaitRegisteredControl("w1", "b", cond, "", "", 0))

	w, ok := as.Mesh.Waits["w1"]
	if !ok {
		t.Fatal("wait w1 not projected")
	}
	if w.Status != agentcomm.WaitReady {
		t.Fatalf("wait status = %q, want ready (condition already satisfied at registration time)", w.Status)
	}
	ready := as.ReadyWaits()
	if len(ready) != 1 || ready[0].ID != "w1" {
		t.Fatalf("ReadyWaits() = %+v, want exactly [w1]", ready)
	}
}

// TestMesh_WaitResolvedTwiceIsNoop replays mesh_wait_resolved for the same wait id twice (the "two
// concurrent resolutions" race, T1.3): the second resolution — even naming a DIFFERENT status — must be a
// silent no-op, not an error, and must never corrupt the stored status.
func TestMesh_WaitResolvedTwiceIsNoop(t *testing.T) {
	as := setupMeshThread(t, "a", "b")
	cond := agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: "th-1"}
	mustApply(t, &as, meshWaitRegisteredControl("w1", "b", cond, "", "", 0))
	if as.Mesh.Waits["w1"].Status != agentcomm.WaitPending {
		t.Fatalf("wait status = %q, want pending (condition not yet satisfied)", as.Mesh.Waits["w1"].Status)
	}

	mustApply(t, &as, meshWaitResolvedControl("w1", string(agentcomm.WaitReady), "orchestrator observed it"))
	if as.Mesh.Waits["w1"].Status != agentcomm.WaitReady {
		t.Fatal("first resolution did not take effect")
	}

	if err := dispatchControlKind(&as, meshWaitResolvedControl("w1", string(agentcomm.WaitExpired), "late deadline"), false); err != nil {
		t.Fatalf("second resolution of an already-resolved wait must not error: %v", err)
	}
	if as.Mesh.Waits["w1"].Status != agentcomm.WaitReady {
		t.Fatalf("second resolution corrupted status: %q, want it to stay ready", as.Mesh.Waits["w1"].Status)
	}
}

// TestMesh_WaitCancelIsIdempotent: mesh_wait_resolved with Status "cancelled" is how cancellation is modeled
// (applyMeshWaitResolved accepts any non-Pending WaitStatus, including WaitCancelled, through the same
// "already resolved -> no-op" path tested above). Cancelling an already-cancelled wait must stay a no-op.
func TestMesh_WaitCancelIsIdempotent(t *testing.T) {
	as := setupMeshThread(t, "a", "b")
	cond := agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: "th-1"}
	mustApply(t, &as, meshWaitRegisteredControl("w1", "b", cond, "", "", 0))

	mustApply(t, &as, meshWaitResolvedControl("w1", string(agentcomm.WaitCancelled), "waiter gave up"))
	if as.Mesh.Waits["w1"].Status != agentcomm.WaitCancelled {
		t.Fatal("cancel did not take effect")
	}
	if err := dispatchControlKind(&as, meshWaitResolvedControl("w1", string(agentcomm.WaitCancelled), "repeated cancel"), false); err != nil {
		t.Fatalf("repeated cancel must not error: %v", err)
	}
	if as.Mesh.Waits["w1"].Status != agentcomm.WaitCancelled {
		t.Fatalf("repeated cancel corrupted status: %q", as.Mesh.Waits["w1"].Status)
	}
}

// TestMesh_WaitStaleGenerationNeverResolved: a wait registered against task t1's generation 0 must never be
// surfaced by ReadyWaits once t1's generation has been bumped (the task was retried) — even if the wait
// itself later gets explicitly resolved to WaitReady (e.g. by an orchestrator that raced the retry).
func TestMesh_WaitStaleGenerationNeverResolved(t *testing.T) {
	as := setupMeshThread(t, "a", "b")
	mustApply(t, &as, taskCreatedControl("op-1", "t1", "stage-a", nil))

	cond := agentcomm.WaitCondition{Kind: agentcomm.WaitOnTaskResult, TaskID: "t1"}
	mustApply(t, &as, meshWaitRegisteredControl("w1", "b", cond, "t1", "", 0))
	if as.Mesh.Waits["w1"].Status != agentcomm.WaitPending {
		t.Fatalf("wait status = %q, want pending (task not terminal yet)", as.Mesh.Waits["w1"].Status)
	}

	// The task is retried: its own generation is bumped past the one this wait was registered against.
	mustApply(t, &as, &Control{Kind: kindTaskGenerationBumped, SchemaVersion: ControlSchemaVersion, Target: Target{TaskID: "t1"}})

	// The underlying condition becomes true (the retried task eventually completes)...
	mustApply(t, &as, &Control{
		Kind: "task_status_changed", SchemaVersion: ControlSchemaVersion, Target: Target{TaskID: "t1"},
		Payload: mustMarshal(t, TaskStatusChangedPayload{Status: "completed"}),
	})
	// ...and something resolves the wait accordingly, racing the retry.
	mustApply(t, &as, meshWaitResolvedControl("w1", string(agentcomm.WaitReady), "task completed"))

	if as.Mesh.Waits["w1"].Status != agentcomm.WaitReady {
		t.Fatal("resolution did not take effect on the stored wait")
	}
	for _, rw := range as.ReadyWaits() {
		if rw.ID == "w1" {
			t.Fatal("ReadyWaits() must never surface a wait registered against a superseded generation")
		}
	}
}

// TestMesh_ReadyWaitsBasicCorrectness: a wait whose condition is satisfied appears in ReadyWaits(); one
// whose condition is not satisfied does not.
func TestMesh_ReadyWaitsBasicCorrectness(t *testing.T) {
	as := setupMeshThread(t, "a", "b")
	mustApply(t, &as, meshMessageSentControl(1, "m1", "th-1", "a", []string{"b"}, string(agentcomm.MessageKindMessage), nil, "ping"))

	cond := agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: "th-1"}
	// "b" has a message addressed to it already (m1) -> satisfied. "a" does not -> unsatisfied.
	mustApply(t, &as, meshWaitRegisteredControl("ready-wait", "b", cond, "", "", 0))
	mustApply(t, &as, meshWaitRegisteredControl("pending-wait", "a", cond, "", "", 0))

	ready := as.ReadyWaits()
	if len(ready) != 1 || ready[0].ID != "ready-wait" {
		t.Fatalf("ReadyWaits() = %+v, want exactly [ready-wait]", ready)
	}
}

// addressed_message WITHOUT a thread = "wake me when anybody writes to me": an agent waiting for a colleague's
// question before any conversation exists. Only a message the waiter has not seen yet satisfies it — one it
// already read (delivered) must not wake it again, or a wait right after reading the inbox would loop.
func TestMesh_AddressedMessageWaitWithoutThread(t *testing.T) {
	as := setupMeshThread(t, "a", "b")
	if !(agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage}).Valid() {
		t.Fatal("addressed_message without a thread must be a valid condition")
	}
	// b already read an older message: it must not satisfy a new wait.
	mustApply(t, &as, meshMessageSentControl(1, "m0", "th-1", "a", []string{"b"}, string(agentcomm.MessageKindMessage), nil, "old"))
	mustApply(t, &as, meshMessageReceiptControl(kindMeshMessageDelivered, "m0", "b"))

	mustApply(t, &as, meshWaitRegisteredControl("w1", "b", agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage}, "", "", 0))
	if got := as.Mesh.Waits["w1"].Status; got != agentcomm.WaitPending {
		t.Fatalf("wait status = %q, want pending (the only message was already read)", got)
	}
	// A message addressed to someone else does not wake b.
	mustApply(t, &as, meshMessageSentControl(2, "m1", "th-1", "b", []string{"a"}, string(agentcomm.MessageKindMessage), nil, "to a"))
	if got := as.Mesh.Waits["w1"].Status; got != agentcomm.WaitPending {
		t.Fatalf("wait status = %q, want still pending", got)
	}
	// A new message to b, in any thread, does.
	mustApply(t, &as, meshMessageSentControl(3, "m2", "th-1", "a", []string{"b"}, string(agentcomm.MessageKindMessage), nil, "new question"))
	if got := as.Mesh.Waits["w1"].Status; got != agentcomm.WaitReady {
		t.Fatalf("wait status = %q, want ready after a new message to b", got)
	}
}

// TestMesh_StageStatusWaitResolvesOnTransition: the transition that brings a stage to the awaited status
// resolves a pending stage_status wait in the same step, live and on replay alike — and the resolution never
// changes a wait value an earlier snapshot copy shares.
func TestMesh_StageStatusWaitResolvesOnTransition(t *testing.T) {
	rs := NewRunState([]string{"build"})
	cond := agentcomm.WaitCondition{Kind: agentcomm.WaitOnStageStatus, StageID: "build", Status: string(StatusRunning)}
	if err := applyControlToRunState(rs, meshWaitRegisteredControl("w1", "user", cond, "", "", 0)); err != nil {
		t.Fatalf("register: %v", err)
	}
	before := rs.Assistants.Mesh.Waits["w1"]
	if before.Status != agentcomm.WaitPending {
		t.Fatalf("wait on a status not reached yet = %s, want pending", before.Status)
	}

	rs.applyTransition(&Transition{StageID: "build", From: StatusPending, To: StatusReady})
	if got := rs.Assistants.Mesh.Waits["w1"].Status; got != agentcomm.WaitPending {
		t.Fatalf("after a transition to another status = %s, want pending", got)
	}
	rs.applyTransition(&Transition{StageID: "build", From: StatusReady, To: StatusRunning})
	if got := rs.Assistants.Mesh.Waits["w1"].Status; got != agentcomm.WaitReady {
		t.Fatalf("after the stage reached running = %s, want ready", got)
	}
	if before.Status != agentcomm.WaitPending {
		t.Fatal("resolving the wait changed the value an earlier copy holds")
	}

	if err := applyControlToRunState(rs, meshWaitRegisteredControl("w2", "user", cond, "", "", 0)); err != nil {
		t.Fatalf("register again: %v", err)
	}
	if got := rs.Assistants.Mesh.Waits["w2"].Status; got != agentcomm.WaitReady {
		t.Fatalf("wait on the status the stage already has = %s, want ready at once", got)
	}
}

// TestMesh_TaskResultWaitReplaysLegacySemantics: in a journal written before result baselines, a task_result
// wait on a run agent that already had a result was ready at once — replay must keep it ready. A new wait
// (FreshResultOnly) on the same task waits for the next result, live and after replay alike.
func TestMesh_TaskResultWaitReplaysLegacySemantics(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	mustRecordPolicy(t, store)
	created := taskCreatedControl("op-run", "t-run", "", nil)
	created.Payload = mustMarshal(t, TaskCreatedPayload{Scope: string(assistants.ScopeRun), ProfileID: "qa", Mode: "advisory"})
	result := &Control{Kind: "task_result", SchemaVersion: ControlSchemaVersion,
		Target: Target{TaskID: "t-run"}, Payload: mustMarshal(t, assistants.Result{Verdict: assistants.VerdictPass, Summary: "turn 1"})}
	cond := agentcomm.WaitCondition{Kind: agentcomm.WaitOnTaskResult, TaskID: "t-run"}
	legacy := meshWaitRegisteredControl("w-legacy", "user", cond, "", "", 0)
	fresh := meshWaitRegisteredControl("w-fresh", "user", cond, "", "", 0)
	fresh.Payload = mustMarshal(t, MeshWaitRegisteredPayload{Waiter: "user", Condition: cond, FreshResultOnly: true})
	for _, c := range []*Control{created, result, legacy, fresh} {
		if err := store.AppendControl(c); err != nil {
			t.Fatalf("AppendControl(%s): %v", c.Kind, err)
		}
	}
	check := func(when string, rs RunState) {
		t.Helper()
		if got := rs.Assistants.Mesh.Waits["w-legacy"].Status; got != agentcomm.WaitReady {
			t.Fatalf("%s: legacy wait = %s, want ready (an earlier result answered it)", when, got)
		}
		if got := rs.Assistants.Mesh.Waits["w-fresh"].Status; got != agentcomm.WaitPending {
			t.Fatalf("%s: fresh wait = %s, want pending until the next result", when, got)
		}
	}
	check("live", store.Snapshot())
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	defer reopened.Close()
	check("replay", reopened.Snapshot())

	next := &Control{Kind: "task_result", SchemaVersion: ControlSchemaVersion,
		Target: Target{TaskID: "t-run"}, Payload: mustMarshal(t, assistants.Result{Verdict: assistants.VerdictPass, Summary: "turn 2"})}
	if err := reopened.AppendControl(next); err != nil {
		t.Fatalf("AppendControl(next result): %v", err)
	}
	if got := reopened.Snapshot().Assistants.Mesh.Waits["w-fresh"].Status; got != agentcomm.WaitReady {
		t.Fatalf("fresh wait after the next result = %s, want ready", got)
	}
}

// TestMesh_ReplyToIsAnsweredOnlyByARecipient: the human is a member of every thread, so a reply_to wait must
// not be satisfied by just any reply — only by one from a recipient of the original message.
func TestMesh_ReplyToIsAnsweredOnlyByARecipient(t *testing.T) {
	cond := agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: "m1"}
	tests := []struct {
		name       string
		recipients []string // of the original message from "author"
		replier    string
		want       agentcomm.WaitStatus
	}{
		{"the addressed reviewer replies", []string{"rev"}, "rev", agentcomm.WaitReady},
		{"one of several recipients replies", []string{"rev", "qa"}, "qa", agentcomm.WaitReady},
		{"the human replies to a message for the reviewer", []string{"rev"}, "user", agentcomm.WaitPending},
		{"the human replies to a message for the human", []string{"user"}, "user", agentcomm.WaitReady},
		{"the author answers itself", []string{"rev"}, "author", agentcomm.WaitPending},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			as := setupMeshThread(t, "author", "rev", "qa", "user")
			mustApply(t, &as, meshMessageSentControl(1, "m1", "th-1", "author", tt.recipients, string(agentcomm.MessageKindMessage), nil, "review please"))
			mustApply(t, &as, recipientReplyWaitControl("w1", "author", cond))
			to := "author"
			if tt.replier == "author" {
				to = "rev"
			}
			mustApply(t, &as, meshMessageSentControl(2, "m2", "th-1", tt.replier, []string{to}, string(agentcomm.MessageKindReply), strp("m1"), "done"))

			if got := as.Mesh.Waits["w1"].Status; got != tt.want {
				t.Fatalf("wait status = %q, want %q", got, tt.want)
			}
			if got := as.Mesh.MessageAnsweredByRecipient("m1"); got != (tt.want == agentcomm.WaitReady) {
				t.Fatalf("MessageAnswered = %v, want %v", got, tt.want == agentcomm.WaitReady)
			}
		})
	}
}

// A wait registered after a recipient already replied is ready at once; after only a non-recipient replied, it
// is still pending.
func TestMesh_ReplyToReadyAtRegistrationNeedsARecipientReply(t *testing.T) {
	cond := agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: "m1"}
	as := setupMeshThread(t, "author", "rev", "user")
	mustApply(t, &as, meshMessageSentControl(1, "m1", "th-1", "author", []string{"rev"}, string(agentcomm.MessageKindMessage), nil, "review please"))
	mustApply(t, &as, meshMessageSentControl(2, "m2", "th-1", "user", []string{"author"}, string(agentcomm.MessageKindReply), strp("m1"), "me too"))
	mustApply(t, &as, recipientReplyWaitControl("w1", "author", cond))
	if got := as.Mesh.Waits["w1"].Status; got != agentcomm.WaitPending {
		t.Fatalf("after the human's reply: wait = %q, want pending", got)
	}
	mustApply(t, &as, meshMessageSentControl(3, "m3", "th-1", "rev", []string{"author"}, string(agentcomm.MessageKindReply), strp("m1"), "lgtm"))
	mustApply(t, &as, recipientReplyWaitControl("w2", "author", cond))
	if got := as.Mesh.Waits["w2"].Status; got != agentcomm.WaitReady {
		t.Fatalf("after the reviewer's reply: wait = %q, want ready", got)
	}
}

// recipientReplyWaitControl is a reply_to wait registered under the recipient-only rule, as every new one is.
func recipientReplyWaitControl(waitID, waiter string, cond agentcomm.WaitCondition) *Control {
	payload, _ := json.Marshal(MeshWaitRegisteredPayload{Waiter: waiter, Condition: cond, RecipientReplyOnly: true})
	return &Control{Kind: kindMeshWaitRegistered, SchemaVersion: ControlSchemaVersion, Target: Target{WaitID: waitID}, Payload: payload}
}

// A reply_to wait journaled before the recipient-only rule keeps the rule it was registered under: any reply
// answered it, the human's included. Replaying such a journal must not turn an answered wait back into a
// pending one — whether the reply came before the wait was registered or after.
func TestMesh_LegacyReplyToWaitKeepsAnySenderRule(t *testing.T) {
	for _, fixture := range []string{"reply-before-wait", "reply-after-wait"} {
		t.Run(fixture, func(t *testing.T) {
			dir := t.TempDir()
			raw, err := os.ReadFile(filepath.Join("testdata", "mesh-pre-recipient-reply", fixture, "events.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), raw, 0o644); err != nil {
				t.Fatal(err)
			}
			rs, err := LoadRunState(dir)
			if err != nil {
				t.Fatalf("load the old journal: %v", err)
			}
			w := rs.Assistants.Mesh.Waits["w-review"]
			if w == nil || w.Status != agentcomm.WaitReady || w.RecipientReplyOnly {
				t.Fatalf("legacy wait = %+v, want ready under the any-sender rule", w)
			}
		})
	}
}

// The same journal with the wait registered under the new rule leaves it pending: the human is not the
// reviewer the message was for.
func TestMesh_NewReplyToWaitIgnoresAHumanReplyOnReplay(t *testing.T) {
	as := setupMeshThread(t, "author", "rev", "user")
	cond := agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: "m1"}
	mustApply(t, &as, meshMessageSentControl(1, "m1", "th-1", "author", []string{"rev"}, string(agentcomm.MessageKindMessage), nil, "review please"))
	mustApply(t, &as, recipientReplyWaitControl("w1", "author", cond))
	mustApply(t, &as, meshMessageSentControl(2, "m2", "th-1", "user", []string{"author"}, string(agentcomm.MessageKindReply), strp("m1"), "fine by me"))
	if got := as.Mesh.Waits["w1"]; got.Status != agentcomm.WaitPending || !got.RecipientReplyOnly {
		t.Fatalf("new wait = %+v, want pending under the recipient-only rule", got)
	}
}

// A wait remembers when it was registered and whether it was still pending then (`afm agent wait` answered
// "yield") — derived on replay, so recovery can tell which waits a crashed turn yielded on.
func TestMesh_WaitRecordsRegistrationSeqAndYield(t *testing.T) {
	as := setupMeshThread(t, "author", "rev")
	cond := agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: "m1"}
	mustApply(t, &as, meshMessageSentControl(1, "m1", "th-1", "author", []string{"rev"}, string(agentcomm.MessageKindMessage), nil, "review please"))
	pending := recipientReplyWaitControl("w-pending", "author", cond)
	pending.Seq = 2
	mustApply(t, &as, pending)
	mustApply(t, &as, meshMessageSentControl(3, "m2", "th-1", "rev", []string{"author"}, string(agentcomm.MessageKindReply), strp("m1"), "lgtm"))
	ready := recipientReplyWaitControl("w-ready", "author", cond)
	ready.Seq = 4
	mustApply(t, &as, ready)

	if w := as.Mesh.Waits["w-pending"]; !w.Yielded || w.RegisteredSeq != 2 || w.Status != agentcomm.WaitReady {
		t.Fatalf("w-pending = %+v, want yielded at seq 2 (ready since)", w)
	}
	if w := as.Mesh.Waits["w-ready"]; w.Yielded || w.RegisteredSeq != 4 {
		t.Fatalf("w-ready = %+v, want ready at registration (no yield) at seq 4", w)
	}
}
