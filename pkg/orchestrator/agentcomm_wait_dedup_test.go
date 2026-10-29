package orchestrator

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/clock"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/state"
)

// An equivalent single wait that is still pending is one wait: a repeated `afm agent wait` of a stage that does
// not remember its first call must not pile up waits that each park and wake the stage on their own.

func replyToStageWait(o *Orchestrator, opID string, msg agentcomm.MessageID, deadline *time.Time, note string) (WaitReceipt, error) {
	return o.Wait(context.Background(), WaitRequest{
		OperationID: opID, Waiter: spawnStage, Actor: spawnStageActor, Deadline: deadline, ContinuationNote: note,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: msg},
	})
}

func TestWait_EquivalentPendingWaitUnderAnotherOperationIsTheSameWait(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	gates := installGatedHelpers(o, func(string) (executor.HelperOutcome, error) { return passResultOutcome(), nil })
	h := mustSpawn(t, o, "op-dedup-spawn", "review")

	deadline := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	first, err := replyToStageWait(o, "op-dedup-1", *h.MessageID, &deadline, "first note")
	if err != nil || first.Status != agentcomm.WaitPending || first.AlreadyPending {
		t.Fatalf("first = %+v, %v; want a new pending wait", first, err)
	}
	seqAfterFirst := store.Snapshot().Assistants.Mesh.Waits[first.WaitID].RegisteredSeq

	later := deadline.Add(time.Hour)
	second, err := replyToStageWait(o, "op-dedup-2", *h.MessageID, &later, "second note")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if second.WaitID != first.WaitID || second.Status != agentcomm.WaitPending || !second.AlreadyPending {
		t.Fatalf("second = %+v, want the first wait %s again, pending, already_pending", second, first.WaitID)
	}

	mesh := store.Snapshot().Assistants.Mesh
	if len(mesh.WaitOrder) != 1 || len(mesh.Waits) != 1 {
		t.Fatalf("waits = %d (order %d), want exactly one", len(mesh.Waits), len(mesh.WaitOrder))
	}
	w := mesh.Waits[first.WaitID]
	if w.RegisteredSeq != seqAfterFirst || w.Deadline == nil || !w.Deadline.Equal(deadline) || w.ContinuationNote != "first note" {
		t.Fatalf("wait = seq %d deadline %v note %q, want the original registration untouched (seq %d, %v, first note)",
			w.RegisteredSeq, w.Deadline, w.ContinuationNote, seqAfterFirst, deadline)
	}

	// The same operation id still replays the first registration unchanged, and so does a retry of the duplicate.
	replay, err := replyToStageWait(o, "op-dedup-1", *h.MessageID, &deadline, "first note")
	if err != nil || replay.WaitID != first.WaitID || replay.AlreadyPending {
		t.Fatalf("replay of the first = %+v, %v; want the same wait, not flagged", replay, err)
	}
	retry, err := replyToStageWait(o, "op-dedup-2", *h.MessageID, &later, "second note")
	if err != nil || retry.WaitID != first.WaitID || !retry.AlreadyPending {
		t.Fatalf("retry of the duplicate = %+v, %v; want the same wait, already pending", retry, err)
	}
	if n := len(store.Snapshot().Assistants.Mesh.Waits); n != 1 {
		t.Fatalf("waits after the retries = %d, want 1", n)
	}

	gates.release(h.TaskID)
	waitUntil(t, "the wait is ready", func() bool { return waitStatus(store, first.WaitID).Status == agentcomm.WaitReady })
	o.concurrency.WaitAgents()

	// An ended wait is not pending: the same condition registers a fresh wait, ready at once.
	again, err := replyToStageWait(o, "op-dedup-3", *h.MessageID, nil, "")
	if err != nil || again.WaitID == first.WaitID || again.AlreadyPending || again.Status != agentcomm.WaitReady {
		t.Fatalf("after the first ended = %+v, %v; want a new wait that is ready", again, err)
	}

	// Accepted limitation, pinned: the duplicate wrote nothing, so a retry of ITS operation id after the original
	// ended is a new registration, not a replay of the answer it got while the original was pending.
	lateRetry, err := replyToStageWait(o, "op-dedup-2", *h.MessageID, &later, "second note")
	if err != nil || lateRetry.WaitID == first.WaitID || lateRetry.AlreadyPending {
		t.Fatalf("late retry of the duplicate = %+v, %v; want a fresh wait", lateRetry, err)
	}
}

// Only the fields of the condition's own kind decide equivalence: a reply_to that also names a thread waits for the
// same thing.
func TestWait_ExtraFieldsOfAnotherKindDoNotMakeAWaitDistinct(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	installGatedHelpers(o, func(string) (executor.HelperOutcome, error) { return passResultOutcome(), nil })
	h := mustSpawn(t, o, "op-dedup-x", "review")

	first, err := replyToStageWait(o, "op-dedup-x-1", *h.MessageID, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := o.Wait(context.Background(), WaitRequest{
		OperationID: "op-dedup-x-2", Waiter: spawnStage, Actor: spawnStageActor,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: *h.MessageID, ThreadID: h.ThreadID},
	})
	if err != nil || second.WaitID != first.WaitID || !second.AlreadyPending {
		t.Fatalf("second = %+v, %v; want the first wait %s, already pending", second, err, first.WaitID)
	}
	if n := len(store.Snapshot().Assistants.Mesh.Waits); n != 1 {
		t.Fatalf("waits = %d, want 1", n)
	}
}

func TestSameWaitCondition(t *testing.T) {
	reply := agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: "m1"}
	stage := agentcomm.WaitCondition{Kind: agentcomm.WaitOnStageStatus, StageID: "s1", Status: "done"}
	cases := []struct {
		name string
		a, b agentcomm.WaitCondition
		want bool
	}{
		{"reply_to, same message", reply, reply, true},
		{"reply_to, extra thread and key", reply, agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: "m1", ThreadID: "t", Key: "k"}, true},
		{"reply_to, other message", reply, agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: "m2"}, false},
		{"other kind", reply, agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, MessageID: "m1"}, false},
		{"addressed_message, same thread", agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: "t"}, agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: "t", MessageID: "x"}, true},
		{"addressed_message, any thread vs a thread", agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage}, agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: "t"}, false},
		{"task_result", agentcomm.WaitCondition{Kind: agentcomm.WaitOnTaskResult, TaskID: "a"}, agentcomm.WaitCondition{Kind: agentcomm.WaitOnTaskResult, TaskID: "b"}, false},
		{"board_key, other status", agentcomm.WaitCondition{Kind: agentcomm.WaitOnBoardKey, Key: "k", Status: "ready"}, agentcomm.WaitCondition{Kind: agentcomm.WaitOnBoardKey, Key: "k", Status: "done"}, false},
		{"stage_status, same", stage, stage, true},
		{"stage_status, other status", stage, agentcomm.WaitCondition{Kind: agentcomm.WaitOnStageStatus, StageID: "s1", Status: "running"}, false},
		{"stage_status, other stage", stage, agentcomm.WaitCondition{Kind: agentcomm.WaitOnStageStatus, StageID: "s2", Status: "done"}, false},
		{"unknown kind", agentcomm.WaitCondition{Kind: "x"}, agentcomm.WaitCondition{Kind: "x"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameWaitCondition(tc.a, tc.b); got != tc.want {
				t.Fatalf("sameWaitCondition(%+v, %+v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// The deadline and the continuation note are the first registration's: a later duplicate arms no timer, so an
// earlier duplicate deadline cannot expire the original, and a duplicate cannot add a deadline to a wait without one.
func TestWait_DuplicateNeverArmsADeadline(t *testing.T) {
	for _, tc := range []struct {
		name            string
		original, later time.Duration // zero original: no deadline
	}{
		{"earlier duplicate deadline", time.Hour, time.Millisecond},
		{"deadline added to a wait without one", 0, time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, store := newAssistantsTestOrchestrator(t, true)
			fc := clock.NewFake(time.Now())
			o.clock = fc
			activateStage(t, o, assistantsTestStageID)
			installGatedHelpers(o, func(string) (executor.HelperOutcome, error) { return passResultOutcome(), nil })
			h := mustSpawn(t, o, "op-dedup-dl", "review")

			var original *time.Time
			if tc.original > 0 {
				d := fc.Now().Add(tc.original)
				original = &d
			}
			first, err := replyToStageWait(o, "op-dedup-dl-1", *h.MessageID, original, "")
			if err != nil {
				t.Fatal(err)
			}
			if original != nil {
				fc.BlockUntil(1) // the original's own timer is armed
			}
			duplicateDeadline := fc.Now().Add(tc.later)
			dup, err := replyToStageWait(o, "op-dedup-dl-2", *h.MessageID, &duplicateDeadline, "")
			if err != nil || dup.WaitID != first.WaitID || !dup.AlreadyPending {
				t.Fatalf("duplicate = %+v, %v; want the first wait, already pending", dup, err)
			}

			// A timer armed by the duplicate would fire on one of these steps; the original's hour is far away.
			for range 200 {
				fc.Advance(2 * time.Millisecond)
				runtime.Gosched()
			}
			w := waitStatus(store, first.WaitID)
			if w.Status != agentcomm.WaitPending {
				t.Fatalf("wait = %s after the duplicate's deadline passed, want still pending", w.Status)
			}
			if (original == nil) != (w.Deadline == nil) {
				t.Fatalf("stored deadline = %v, want the original's (%v)", w.Deadline, original)
			}
		})
	}
}

// The projection decides equivalence: same waiter and condition AND same rule flags, a pending single wait outside
// any batch. A wait from before a flag existed must not swallow a request that wants the stricter rule.
func TestEquivalentPendingWait(t *testing.T) {
	cond := agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: "m1"}
	waiter := agentcomm.ParticipantID("stage:impl")
	build := func(mutate func(*state.MeshWait)) state.RunState {
		rs := *state.NewRunState([]string{"impl"})
		w := &state.MeshWait{Wait: agentcomm.Wait{ID: "w1", Waiter: waiter, Status: agentcomm.WaitPending, Condition: cond}, RecipientReplyOnly: true}
		mutate(w)
		rs.Assistants.Mesh.Waits["w1"] = w
		rs.Assistants.Mesh.WaitOrder = []agentcomm.WaitID{"w1"}
		return rs
	}
	cases := []struct {
		name      string
		mutate    func(*state.MeshWait)
		freshOnly bool
		recipient bool
		waiter    agentcomm.ParticipantID
		want      bool
	}{
		{"equal flags", func(*state.MeshWait) {}, false, true, waiter, true},
		{"older wait without the recipient rule", func(w *state.MeshWait) { w.RecipientReplyOnly = false }, false, true, waiter, false},
		{"request without the recipient rule", func(*state.MeshWait) {}, false, false, waiter, false},
		{"fresh-result rule differs", func(*state.MeshWait) {}, true, true, waiter, false},
		{"batch item", func(w *state.MeshWait) { w.BatchID = "b1" }, false, true, waiter, false},
		{"ended wait", func(w *state.MeshWait) { w.Status = agentcomm.WaitReady }, false, true, waiter, false},
		{"other waiter", func(*state.MeshWait) {}, false, true, "stage:other", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, found := equivalentPendingWait(build(tc.mutate), tc.waiter, cond, tc.freshOnly, tc.recipient)
			if found != tc.want || (found && id != "w1") {
				t.Fatalf("equivalentPendingWait = %q, %v; want found=%v", id, found, tc.want)
			}
		})
	}
}

// A batch for a message and a single wait for the same message are two waits: the single wait never becomes an
// item of the batch.
func TestWait_SingleWaitNextToABatchOnTheSameMessageIsItsOwnWait(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	installGatedHelpers(o, func(string) (executor.HelperOutcome, error) { return passResultOutcome(), nil })
	h := mustSpawn(t, o, "op-dedup-b", "review")

	batch, err := o.Wait(context.Background(), WaitRequest{
		OperationID: "op-dedup-b-batch", Waiter: spawnStage, Actor: spawnStageActor,
		Condition:  agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo},
		MessageIDs: []agentcomm.MessageID{*h.MessageID},
	})
	if err != nil || batch.BatchID == "" {
		t.Fatalf("batch = %+v, %v", batch, err)
	}
	single, err := replyToStageWait(o, "op-dedup-b-single", *h.MessageID, nil, "")
	if err != nil || single.WaitID == batch.Items[0].WaitID || single.AlreadyPending {
		t.Fatalf("single = %+v, %v; want a new wait distinct from the batch item %s", single, err, batch.Items[0].WaitID)
	}
	if n := len(store.Snapshot().Assistants.Mesh.Waits); n != 2 {
		t.Fatalf("waits = %d, want 2", n)
	}
}

func TestWait_DistinctWaitersAndConditionsAreNotCoalesced(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	gates := installGatedHelpers(o, func(string) (executor.HelperOutcome, error) { return passResultOutcome(), nil })
	h1 := mustSpawn(t, o, "op-dedup-d1", "first")
	h2 := mustSpawn(t, o, "op-dedup-d2", "second")

	base, err := replyToStageWait(o, "op-dedup-d-base", *h1.MessageID, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	// Another condition (other message), other waiter (the human may wait on the same message), other kind.
	other, err := replyToStageWait(o, "op-dedup-d-other-msg", *h2.MessageID, nil, "")
	if err != nil || other.WaitID == base.WaitID || other.AlreadyPending {
		t.Fatalf("other message = %+v, %v; want a distinct wait", other, err)
	}
	human, err := o.Wait(context.Background(), WaitRequest{
		OperationID: "op-dedup-d-human", Waiter: meshUserParticipantID, Actor: state.Actor{Kind: "user"},
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: *h1.MessageID},
	})
	if err != nil || human.WaitID == base.WaitID || human.AlreadyPending {
		t.Fatalf("other waiter = %+v, %v; want a distinct wait", human, err)
	}
	if got := len(store.Snapshot().Assistants.Mesh.Waits); got != 3 {
		t.Fatalf("waits = %d, want 3 distinct ones", got)
	}

	gates.release(h1.TaskID)
	gates.release(h2.TaskID)
	o.concurrency.WaitAgents()
}

// Several registrations of one condition at the same time under different operation ids: exactly one wait.
func TestWait_ConcurrentEquivalentRegistrationsYieldOneWait(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	gates := installGatedHelpers(o, func(string) (executor.HelperOutcome, error) { return passResultOutcome(), nil })
	h := mustSpawn(t, o, "op-dedup-c", "review")

	const callers = 12
	receipts := make([]WaitReceipt, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Go(func() {
			receipts[i], errs[i] = replyToStageWait(o, fmt.Sprintf("op-dedup-c-%d", i), *h.MessageID, nil, "")
		})
	}
	wg.Wait()

	fresh := 0
	for i, r := range receipts {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if r.WaitID != receipts[0].WaitID {
			t.Fatalf("caller %d got wait %s, caller 0 got %s", i, r.WaitID, receipts[0].WaitID)
		}
		if !r.AlreadyPending {
			fresh++
		}
	}
	if fresh != 1 {
		t.Fatalf("callers that registered the wait = %d, want exactly 1", fresh)
	}
	if n := len(store.Snapshot().Assistants.Mesh.Waits); n != 1 {
		t.Fatalf("waits = %d, want 1", n)
	}
	gates.release(h.TaskID)
	o.concurrency.WaitAgents()
}

// A speculative wait (a board key nobody has published) needs a deadline once; its repeat under another operation
// id without one is the same wait, since the deadline is not part of the identity.
func TestWait_SpeculativeWaitRepeatedWithoutDeadlineIsTheSameWait(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	cond := agentcomm.WaitCondition{Kind: agentcomm.WaitOnBoardKey, Key: "nobody-published-this", Status: "ready"}
	wait := func(opID string, deadline *time.Time) (WaitReceipt, error) {
		return o.Wait(context.Background(), WaitRequest{OperationID: opID, Waiter: spawnStage, Actor: spawnStageActor, Condition: cond, Deadline: deadline})
	}

	first, err := wait("op-spec-1", testFarDeadline())
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := wait("op-spec-2", nil)
	if err != nil || second.WaitID != first.WaitID || !second.AlreadyPending {
		t.Fatalf("repeat without a deadline = %+v, %v; want the first wait %s, already pending", second, err, first.WaitID)
	}
	if n := len(store.Snapshot().Assistants.Mesh.Waits); n != 1 {
		t.Fatalf("waits = %d, want 1", n)
	}
	// Without a pending original the repeat is a new speculative wait and still needs its deadline.
	_, err = o.Wait(context.Background(), WaitRequest{
		OperationID: "op-spec-3", Waiter: spawnStage, Actor: spawnStageActor, Deadline: nil,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnBoardKey, Key: "another-key", Status: "ready"},
	})
	requireRejected(t, err, assistants.CodeInvalidRequest)
}
