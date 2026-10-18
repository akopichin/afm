package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
)

func taskCreatedEffect(opJSON string, taskID, stageID string) Effect {
	return Effect{Kind: "task_created", Target: Target{TaskID: taskID, StageID: stageID}, Payload: json.RawMessage(opJSON)}
}

// mustRecordPolicy records a default depth policy so task_created effects are admissible (T2.6 gates any
// task_created operation on a policy already being recorded) — tests that only care about operation/
// idempotency mechanics call this once up front rather than re-testing T2.6's own policy behavior.
func mustRecordPolicy(t *testing.T, store *Store) {
	t.Helper()
	if _, err := store.EnsurePolicyRecorded(assistants.DepthPolicy{MaxDepth: 10, Source: assistants.DepthSourceDefault, Revision: "test-rev"}, Actor{Kind: "system"}); err != nil {
		t.Fatalf("EnsurePolicyRecorded: %v", err)
	}
}

// ---- T2.4 ----

func TestOperation_AllEffectsCommittedTogether(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	mustRecordPolicy(t, store)

	op := Operation{
		ID:   "op-1",
		Kind: "create_children",
		Effects: []Effect{
			taskCreatedEffect(`{"profile_id":"p"}`, "t1", "a"),
			taskCreatedEffect(`{"profile_id":"p"}`, "t2", "a"),
		},
	}
	// Simulate a crash right after fsync (before the hook returns) — same pattern as
	// TestAppendControl_FsyncsBeforeReturn / TestApply_CrashAfterFsync_Recovers.
	SetAppendControlHook(func(c Control) {
		store.eventsLog.Close()
		store.lock.Unlock()
		panic("simulated crash")
	})
	defer SetAppendControlHook(nil)

	func() {
		defer func() { _ = recover() }()
		_, _ = store.ApplyRunOperation(op, nil)
	}()

	store2, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store2.Close()
	snap := store2.Snapshot()
	_, t1 := snap.Assistants.Tasks["t1"]
	_, t2 := snap.Assistants.Tasks["t2"]
	if t1 != t2 {
		t.Fatalf("partial commit after crash: t1 present=%v t2 present=%v, want both or neither", t1, t2)
	}
}

func TestOperation_StaleRevisionRejected(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	mustRecordPolicy(t, store)

	op := Operation{
		ID:      "op-1",
		Kind:    "set_policy_dependent",
		Expect:  Preconditions{PolicyRevision: "rev-that-does-not-exist"},
		Effects: []Effect{taskCreatedEffect(`{"profile_id":"p"}`, "t1", "a")},
	}
	_, err = store.ApplyRunOperation(op, nil)
	if err == nil {
		t.Fatal("ApplyRunOperation with a stale policy revision: want error, got nil")
	}
	var rej *OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != assistants.CodeStaleRevision {
		t.Fatalf("err = %v, want *OperationRejectedError{Code: stale_revision}", err)
	}

	// The journal must be unchanged: no task created.
	if _, ok := store.Snapshot().Assistants.Tasks["t1"]; ok {
		t.Fatal("task t1 exists after a rejected operation — the journal was mutated despite the rejection")
	}
}

func TestOperation_RejectedWhenRunFinalizing(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	store.SetRunFinalizing(true)

	op := Operation{ID: "op-1", Kind: "create_children", Effects: []Effect{taskCreatedEffect(`{"profile_id":"p"}`, "t1", "a")}}
	_, err = store.ApplyRunOperation(op, nil)
	var rej *OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != assistants.CodeRunFinalizing {
		t.Fatalf("err = %v, want *OperationRejectedError{Code: run_finalizing}", err)
	}
}

// TestFinalizeIf_DecidesUnderTheAdmissionLock: the run finalizes only when its predicate holds for the
// current projection, and from then on every operation is rejected run_finalizing.
func TestFinalizeIf_DecidesUnderTheAdmissionLock(t *testing.T) {
	store, err := Open(t.TempDir(), []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	mustRecordPolicy(t, store)
	noTasks := func(rs *RunState) bool { return len(rs.Assistants.Tasks) == 0 }

	if _, err := store.ApplyRunOperation(Operation{ID: "op-1", Kind: "create", Effects: []Effect{taskCreatedEffect(`{"profile_id":"p"}`, "t1", "a")}}, nil); err != nil {
		t.Fatalf("ApplyRunOperation: %v", err)
	}
	if store.FinalizeIf(noTasks) {
		t.Fatal("FinalizeIf finalized although its predicate does not hold for the committed task")
	}
	if _, err := store.ApplyRunOperation(Operation{ID: "op-2", Kind: "create", Effects: []Effect{taskCreatedEffect(`{"profile_id":"p"}`, "t2", "a")}}, nil); err != nil {
		t.Fatalf("an operation after a declined FinalizeIf must still be admitted: %v", err)
	}

	if !store.FinalizeIf(func(*RunState) bool { return true }) {
		t.Fatal("FinalizeIf with a holding predicate must finalize")
	}
	_, err = store.ApplyRunOperation(Operation{ID: "op-3", Kind: "create", Effects: []Effect{taskCreatedEffect(`{"profile_id":"p"}`, "t3", "a")}}, nil)
	var rej *OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != assistants.CodeRunFinalizing {
		t.Fatalf("operation after finalizing: err = %v, want run_finalizing", err)
	}
}

// TestOperation_DoesNotHoldLockDuringExternalWork proves externalWork runs AFTER the store's lock is
// released by attempting a SECOND operation from inside the callback — if the lock were still held, this
// would deadlock.
func TestOperation_DoesNotHoldLockDuringExternalWork(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	mustRecordPolicy(t, store)

	done := make(chan struct{})
	op1 := Operation{ID: "op-1", Kind: "create_children", Effects: []Effect{taskCreatedEffect(`{"profile_id":"p"}`, "t1", "a")}}
	_, err = store.ApplyRunOperation(op1, func(res OperationResult) {
		op2 := Operation{ID: "op-2", Kind: "create_children", Effects: []Effect{taskCreatedEffect(`{"profile_id":"p"}`, "t2", "a")}}
		if _, err := store.ApplyRunOperation(op2, nil); err != nil {
			t.Errorf("second ApplyRunOperation from inside externalWork: %v", err)
		}
		close(done)
	})
	if err != nil {
		t.Fatalf("ApplyRunOperation: %v", err)
	}
	select {
	case <-done:
	default:
		t.Fatal("externalWork did not run synchronously within ApplyRunOperation's return")
	}
	if _, ok := store.Snapshot().Assistants.Tasks["t2"]; !ok {
		t.Fatal("task t2 (created from inside externalWork) is missing — the second operation did not actually commit")
	}
}

// TestOperation_ConcurrentWritersSerialized: N goroutines race to reserve 1 unit of a shared quota of
// limit; the lock must serialize them so the total committed reservation never exceeds limit.
func TestOperation_ConcurrentWritersSerialized(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	const n = 20
	const limit = 7
	var wg sync.WaitGroup
	var accepted int64
	for i := 0; i < n; i++ {
		wg.Go(func() {
			limitCopy := limit
			op := Operation{
				ID:      fmt.Sprintf("reserve-%d", i),
				Kind:    "reserve_quota",
				Expect:  Preconditions{MaxQuota: &limitCopy},
				Effects: []Effect{{Kind: "quota_reserved", Payload: json.RawMessage(`{"amount":1}`)}},
			}
			if _, err := store.ApplyRunOperation(op, nil); err == nil {
				atomic.AddInt64(&accepted, 1)
			}
		})
	}
	wg.Wait()

	if accepted != limit {
		t.Fatalf("accepted = %d, want exactly %d", accepted, limit)
	}
	if got := store.Snapshot().Assistants.QuotaReserved; got != limit {
		t.Fatalf("QuotaReserved = %d, want %d", got, limit)
	}
}

// ---- T2.5 ----

func TestIdempotency_SameIdSameContentReturnsPriorResult(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	mustRecordPolicy(t, store)

	op := Operation{ID: "op-1", Kind: "create_children", Effects: []Effect{taskCreatedEffect(`{"profile_id":"p"}`, "t1", "a")}}
	res1, err := store.ApplyRunOperation(op, nil)
	if err != nil {
		t.Fatalf("first ApplyRunOperation: %v", err)
	}
	res2, err := store.ApplyRunOperation(op, nil)
	if err != nil {
		t.Fatalf("second (repeat) ApplyRunOperation: %v", err)
	}
	if res1 != res2 {
		t.Fatalf("res1=%+v res2=%+v, want identical (idempotent repeat)", res1, res2)
	}
	tasks := 0
	for id := range store.Snapshot().Assistants.Tasks {
		_ = id
		tasks++
	}
	if tasks != 1 {
		t.Fatalf("tasks = %d, want 1 (repeat must not create a second task)", tasks)
	}
}

func TestIdempotency_SameIdDifferentContentRejected(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	mustRecordPolicy(t, store)

	op1 := Operation{ID: "op-1", Kind: "create_children", Effects: []Effect{taskCreatedEffect(`{"profile_id":"p"}`, "t1", "a")}}
	if _, err := store.ApplyRunOperation(op1, nil); err != nil {
		t.Fatalf("first ApplyRunOperation: %v", err)
	}
	op2 := Operation{ID: "op-1", Kind: "create_children", Effects: []Effect{taskCreatedEffect(`{"profile_id":"p"}`, "t2", "a")}}
	_, err = store.ApplyRunOperation(op2, nil)
	if !errors.Is(err, ErrOperationIDConflict) {
		t.Fatalf("err = %v, want ErrOperationIDConflict", err)
	}
}

func TestIdempotency_SurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	mustRecordPolicy(t, store)
	op := Operation{ID: "op-1", Kind: "create_children", Effects: []Effect{taskCreatedEffect(`{"profile_id":"p"}`, "t1", "a")}}
	res1, err := store.ApplyRunOperation(op, nil)
	if err != nil {
		t.Fatalf("ApplyRunOperation: %v", err)
	}
	store.Close()

	store2, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store2.Close()
	res2, err := store2.ApplyRunOperation(op, nil)
	if err != nil {
		t.Fatalf("ApplyRunOperation after restart: %v", err)
	}
	if res1 != res2 {
		t.Fatalf("res1=%+v res2=%+v, want identical across restart", res1, res2)
	}
}

func TestIdempotency_DoubleClickFromTwoTabs(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	mustRecordPolicy(t, store)

	op := Operation{ID: "op-1", Kind: "create_children", Effects: []Effect{taskCreatedEffect(`{"profile_id":"p"}`, "t1", "a")}}
	var wg sync.WaitGroup
	results := make([]OperationResult, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Go(func() {
			results[i], errs[i] = store.ApplyRunOperation(op, nil)
		})
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if results[0] != results[1] {
		t.Fatalf("results differ across the two concurrent attempts: %+v vs %+v", results[0], results[1])
	}
	if n := len(store.Snapshot().Assistants.Tasks); n != 1 {
		t.Fatalf("tasks created = %d, want exactly 1 (one durable record for both concurrent attempts)", n)
	}
}

func TestIdempotency_ReplayAfterResultEvicted(t *testing.T) {
	SetOperationResultCacheCapacityForTest(4)
	defer SetOperationResultCacheCapacityForTest(0)

	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	mustRecordPolicy(t, store)

	opA := Operation{ID: "op-A", Kind: "create_children", Effects: []Effect{taskCreatedEffect(`{"profile_id":"p"}`, "tA", "a")}}
	resA, err := store.ApplyRunOperation(opA, nil)
	if err != nil {
		t.Fatalf("ApplyRunOperation(opA): %v", err)
	}

	// N+10 other operations to definitely evict op-A's cached body (capacity 4).
	for i := 0; i < 14; i++ {
		other := Operation{ID: fmt.Sprintf("op-other-%d", i), Kind: "create_children",
			Effects: []Effect{taskCreatedEffect(`{"profile_id":"p"}`, fmt.Sprintf("t-other-%d", i), "a")}}
		if _, err := store.ApplyRunOperation(other, nil); err != nil {
			t.Fatalf("ApplyRunOperation(other %d): %v", i, err)
		}
	}

	var reads int32
	SetControlReadHook(func() { atomic.AddInt32(&reads, 1) })
	defer SetControlReadHook(nil)

	resA2, err := store.ApplyRunOperation(opA, nil)
	if err != nil {
		t.Fatalf("repeat ApplyRunOperation(opA) after eviction: %v", err)
	}
	if resA != resA2 {
		t.Fatalf("resA=%+v resA2=%+v, want identical (prior result read from the journal by seq)", resA, resA2)
	}
	if atomic.LoadInt32(&reads) != 1 {
		t.Fatalf("journal reads = %d, want exactly 1 (direct read by seq, no scan)", reads)
	}

	// No second task for tA, no new control record for op-A.
	if _, ok := store.Snapshot().Assistants.Tasks["tA"]; !ok {
		t.Fatal("task tA is missing")
	}
}

func TestIdempotency_ReplayAfterEvictionAndRestart(t *testing.T) {
	SetOperationResultCacheCapacityForTest(4)
	defer SetOperationResultCacheCapacityForTest(0)

	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	mustRecordPolicy(t, store)

	opA := Operation{ID: "op-A", Kind: "create_children", Effects: []Effect{taskCreatedEffect(`{"profile_id":"p"}`, "tA", "a")}}
	resA, err := store.ApplyRunOperation(opA, nil)
	if err != nil {
		t.Fatalf("ApplyRunOperation(opA): %v", err)
	}
	for i := 0; i < 14; i++ {
		other := Operation{ID: fmt.Sprintf("op-other-%d", i), Kind: "create_children",
			Effects: []Effect{taskCreatedEffect(`{"profile_id":"p"}`, fmt.Sprintf("t-other-%d", i), "a")}}
		if _, err := store.ApplyRunOperation(other, nil); err != nil {
			t.Fatalf("ApplyRunOperation(other %d): %v", i, err)
		}
	}
	store.Close() // the in-memory opCache is gone now, too — not just evicted, entirely rebuilt empty on reopen.

	store2, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store2.Close()

	resA2, err := store2.ApplyRunOperation(opA, nil)
	if err != nil {
		t.Fatalf("repeat ApplyRunOperation(opA) after restart: %v", err)
	}
	if resA != resA2 {
		t.Fatalf("resA=%+v resA2=%+v, want identical across eviction+restart", resA, resA2)
	}
}

func TestIdempotency_EvictedResultDifferentContentStillRejected(t *testing.T) {
	SetOperationResultCacheCapacityForTest(4)
	defer SetOperationResultCacheCapacityForTest(0)

	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	mustRecordPolicy(t, store)

	opA := Operation{ID: "op-A", Kind: "create_children", Effects: []Effect{taskCreatedEffect(`{"profile_id":"p"}`, "tA", "a")}}
	if _, err := store.ApplyRunOperation(opA, nil); err != nil {
		t.Fatalf("ApplyRunOperation(opA): %v", err)
	}
	for i := 0; i < 14; i++ {
		other := Operation{ID: fmt.Sprintf("op-other-%d", i), Kind: "create_children",
			Effects: []Effect{taskCreatedEffect(`{"profile_id":"p"}`, fmt.Sprintf("t-other-%d", i), "a")}}
		if _, err := store.ApplyRunOperation(other, nil); err != nil {
			t.Fatalf("ApplyRunOperation(other %d): %v", i, err)
		}
	}

	opADifferent := Operation{ID: "op-A", Kind: "create_children", Effects: []Effect{taskCreatedEffect(`{"profile_id":"p"}`, "tA-different", "a")}}
	_, err = store.ApplyRunOperation(opADifferent, nil)
	if !errors.Is(err, ErrOperationIDConflict) {
		t.Fatalf("err = %v, want ErrOperationIDConflict even after the original body was evicted", err)
	}
}

func TestIdempotency_KeyIndexNeverEvicts(t *testing.T) {
	SetOperationResultCacheCapacityForTest(4)
	defer SetOperationResultCacheCapacityForTest(0)

	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	mustRecordPolicy(t, store)

	first := Operation{ID: "op-first", Kind: "create_children", Effects: []Effect{taskCreatedEffect(`{"profile_id":"p"}`, "tFirst", "a")}}
	if _, err := store.ApplyRunOperation(first, nil); err != nil {
		t.Fatalf("ApplyRunOperation(first): %v", err)
	}
	for i := 0; i < 14; i++ {
		other := Operation{ID: fmt.Sprintf("op-other-%d", i), Kind: "create_children",
			Effects: []Effect{taskCreatedEffect(`{"profile_id":"p"}`, fmt.Sprintf("t-other-%d", i), "a")}}
		if _, err := store.ApplyRunOperation(other, nil); err != nil {
			t.Fatalf("ApplyRunOperation(other %d): %v", i, err)
		}
	}

	if _, ok := store.Snapshot().Assistants.OperationIndex["op-first"]; !ok {
		t.Fatal("OperationIndex[op-first] missing after 14 more operations — the key index must never evict")
	}
}

func TestIdempotency_NewOperationDoesNotReadJournal(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	mustRecordPolicy(t, store)

	var reads int32
	SetControlReadHook(func() { atomic.AddInt32(&reads, 1) })
	defer SetControlReadHook(nil)

	op := Operation{ID: "brand-new", Kind: "create_children", Effects: []Effect{taskCreatedEffect(`{"profile_id":"p"}`, "t1", "a")}}
	if _, err := store.ApplyRunOperation(op, nil); err != nil {
		t.Fatalf("ApplyRunOperation: %v", err)
	}
	if atomic.LoadInt32(&reads) != 0 {
		t.Fatalf("journal reads = %d, want 0 for a brand-new operation_id (cache miss on a key the index has never seen must not read the journal)", reads)
	}
}

// TestCommittedOperation_SameFromCacheAndJournal: a committed operation reads back with its own result and
// effects — from the cache right after the commit, and from the journal after a restart — and an unknown id
// reports nothing committed.
func TestCommittedOperation_SameFromCacheAndJournal(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	mustRecordPolicy(t, store)
	op := Operation{ID: "op-A", Kind: "create_children", Effects: []Effect{taskCreatedEffect(`{"profile_id":"p","title":"T"}`, "tA", "a")}}
	res, err := store.ApplyRunOperation(op, nil)
	if err != nil {
		t.Fatalf("ApplyRunOperation: %v", err)
	}
	check := func(when string, s *Store) {
		t.Helper()
		got, committed, err := s.CommittedOperation("op-A", "create_children")
		if err != nil || !committed {
			t.Fatalf("%s: committed=%v err=%v", when, committed, err)
		}
		if got.Result.Seq != res.Seq || !got.Result.Time.Equal(res.Time) {
			t.Fatalf("%s: result = %+v, want %+v", when, got.Result, res)
		}
		var p TaskCreatedPayload
		target, found, err := got.Effect("task_created", &p)
		if err != nil || !found || target.TaskID != "tA" || p.Title != "T" {
			t.Fatalf("%s: task_created = %+v %+v (found=%v, err=%v)", when, target, p, found, err)
		}
		if _, committed, err := s.CommittedOperation("op-unknown", "create_children"); committed || err != nil {
			t.Fatalf("%s: unknown id: committed=%v err=%v", when, committed, err)
		}
	}
	check("live", store)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	defer reopened.Close()
	check("after restart", reopened)
}

// TestCommittedOperation_AnotherKindConflicts: an operation read back as another kind than it was committed as
// is a request id conflict.
func TestCommittedOperation_AnotherKindConflicts(t *testing.T) {
	store, err := Open(t.TempDir(), []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	mustRecordPolicy(t, store)
	op := Operation{ID: "op-A", Kind: "create_children", Effects: []Effect{taskCreatedEffect(`{"profile_id":"p","title":"T"}`, "tA", "a")}}
	if _, err := store.ApplyRunOperation(op, nil); err != nil {
		t.Fatalf("ApplyRunOperation: %v", err)
	}
	got, committed, err := store.CommittedOperation("op-A", "create_children")
	if err != nil || !committed || got.Kind != "create_children" {
		t.Fatalf("same kind: kind=%q committed=%v err=%v", got.Kind, committed, err)
	}
	if _, _, err := store.CommittedOperation("op-A", "attach_task"); !errors.Is(err, ErrOperationIDConflict) {
		t.Fatalf("another kind: err = %v, want a request id conflict", err)
	}
}
