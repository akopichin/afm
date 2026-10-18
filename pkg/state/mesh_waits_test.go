package state

import (
	"encoding/json"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
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
