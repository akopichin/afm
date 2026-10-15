package state

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
)

func depthPolicy(maxDepth int, revision string) assistants.DepthPolicy {
	return assistants.DepthPolicy{MaxDepth: maxDepth, Source: assistants.DepthSourceFlow, Revision: revision}
}

func TestPolicy_RecordedBeforeFirstAdmission(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	op := Operation{ID: "op-1", Kind: "create_children", Effects: []Effect{taskCreatedEffect(`{"profile_id":"p"}`, "t1", "a")}}
	_, err = store.ApplyRunOperation(op, nil)
	if !errors.Is(err, ErrPolicyNotRecorded) {
		t.Fatalf("err = %v, want ErrPolicyNotRecorded (no task may be created before a policy is recorded)", err)
	}

	changed, err := store.EnsurePolicyRecorded(depthPolicy(3, "rev-1"), Actor{Kind: "system"})
	if err != nil || !changed {
		t.Fatalf("EnsurePolicyRecorded: changed=%v err=%v, want changed=true", changed, err)
	}

	if _, err := store.ApplyRunOperation(op, nil); err != nil {
		t.Fatalf("ApplyRunOperation after policy recorded: %v", err)
	}
}

func TestPolicy_ChangeOnResumeIsDurableOperation(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	changed, err := store.EnsurePolicyRecorded(depthPolicy(3, "rev-1"), Actor{Kind: "system"})
	if err != nil || !changed {
		t.Fatalf("first EnsurePolicyRecorded: changed=%v err=%v", changed, err)
	}
	// Same revision again — no new record.
	changed, err = store.EnsurePolicyRecorded(depthPolicy(3, "rev-1"), Actor{Kind: "system"})
	if err != nil || changed {
		t.Fatalf("repeat EnsurePolicyRecorded with same revision: changed=%v err=%v, want changed=false", changed, err)
	}
	store.Close()

	// Simulate "resume" with a changed max_depth (different revision) — must be committed as its own durable
	// record BEFORE this test's stand-in for worker resumption (the subsequent ApplyRunOperation call).
	store2, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store2.Close()
	changed, err = store2.EnsurePolicyRecorded(depthPolicy(1, "rev-2"), Actor{Kind: "system"})
	if err != nil || !changed {
		t.Fatalf("EnsurePolicyRecorded on resume with a new revision: changed=%v err=%v, want changed=true", changed, err)
	}

	rs, err := LoadRunState(dir)
	if err != nil {
		t.Fatalf("LoadRunState: %v", err)
	}
	if rs.Assistants.Policy.Revision != "rev-2" || rs.Assistants.Policy.MaxDepth != 1 {
		t.Fatalf("Policy = %+v after restart, want {MaxDepth:1 Revision:rev-2}", rs.Assistants.Policy)
	}
}

func TestPolicy_LoweredLimitKeepsExistingTree(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	if _, err := store.EnsurePolicyRecorded(depthPolicy(5, "rev-1"), Actor{Kind: "system"}); err != nil {
		t.Fatalf("EnsurePolicyRecorded: %v", err)
	}
	root := Operation{ID: "op-root", Kind: "create_children", Effects: []Effect{taskCreatedEffect(`{"profile_id":"p"}`, "root", "a")}}
	if _, err := store.ApplyRunOperation(root, nil); err != nil {
		t.Fatalf("create root: %v", err)
	}
	rootID := "root"
	child := Operation{ID: "op-child", Kind: "create_children", Effects: []Effect{taskCreatedEffect(
		fmt.Sprintf(`{"profile_id":"p","parent_task_id":%q}`, rootID), "child", "a")}}
	if _, err := store.ApplyRunOperation(child, nil); err != nil {
		t.Fatalf("create child: %v", err)
	}

	before := store.Snapshot().Assistants.Tasks["child"]
	if before.Depth != 2 {
		t.Fatalf("child depth = %d, want 2", before.Depth)
	}

	// Lower the limit below the existing child's depth.
	if _, err := store.EnsurePolicyRecorded(depthPolicy(1, "rev-2"), Actor{Kind: "system"}); err != nil {
		t.Fatalf("EnsurePolicyRecorded (lowered): %v", err)
	}

	snap := store.Snapshot()
	after := snap.Assistants.Tasks["child"]
	if after.Depth != before.Depth || (after.ParentTaskID == nil) != (before.ParentTaskID == nil) ||
		(after.ParentTaskID != nil && *after.ParentTaskID != *before.ParentTaskID) {
		t.Fatalf("lowering the policy mutated the existing task: before=%+v after=%+v", before, after)
	}
	if !snap.Assistants.PolicyBlocked("child") {
		t.Fatal("PolicyBlocked(child) = false, want true (depth 2 > new max_depth 1)")
	}
	if snap.Assistants.PolicyBlocked("root") {
		t.Fatal("PolicyBlocked(root) = true, want false (depth 1 <= max_depth 1)")
	}
}

func TestQuota_ReservedAtomicallyForChildGroup(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	const limit = 5
	const branches = 2
	const perBranch = 4 // 2*4=8 > 5: not all attempts can be admitted
	var wg sync.WaitGroup
	var accepted int64
	for b := 0; b < branches; b++ {
		for i := 0; i < perBranch; i++ {
			wg.Go(func() {
				limitCopy := limit
				op := Operation{
					ID:      fmt.Sprintf("branch-%d-reserve-%d", b, i),
					Kind:    "reserve_quota",
					Expect:  Preconditions{MaxQuota: &limitCopy},
					Effects: []Effect{{Kind: "quota_reserved", Payload: []byte(`{"amount":1}`)}},
				}
				if _, err := store.ApplyRunOperation(op, nil); err == nil {
					atomic.AddInt64(&accepted, 1)
				}
			})
		}
	}
	wg.Wait()

	if accepted != limit {
		t.Fatalf("accepted = %d, want exactly %d (shared quota across both branches)", accepted, limit)
	}
	if got := store.Snapshot().Assistants.QuotaReserved; got != limit {
		t.Fatalf("QuotaReserved = %d, want %d", got, limit)
	}
}

func TestQuota_NotResetPerDelegation(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	limit := 100
	// "Level 1" reservation.
	op1 := Operation{ID: "level-1", Kind: "reserve_quota", Expect: Preconditions{MaxQuota: &limit},
		Effects: []Effect{{Kind: "quota_reserved", Payload: []byte(`{"amount":10}`)}}}
	if _, err := store.ApplyRunOperation(op1, nil); err != nil {
		t.Fatalf("level-1 reservation: %v", err)
	}
	if got := store.Snapshot().Assistants.QuotaReserved; got != 10 {
		t.Fatalf("QuotaReserved after level 1 = %d, want 10", got)
	}

	// "Level 2" (a delegated child) reservation — must ADD to, not reset, the running total.
	op2 := Operation{ID: "level-2", Kind: "reserve_quota", Expect: Preconditions{MaxQuota: &limit},
		Effects: []Effect{{Kind: "quota_reserved", Payload: []byte(`{"amount":5}`)}}}
	if _, err := store.ApplyRunOperation(op2, nil); err != nil {
		t.Fatalf("level-2 reservation: %v", err)
	}
	if got := store.Snapshot().Assistants.QuotaReserved; got != 15 {
		t.Fatalf("QuotaReserved after level 2 = %d, want 15 (whole-tree budget, not reset per level)", got)
	}
}
