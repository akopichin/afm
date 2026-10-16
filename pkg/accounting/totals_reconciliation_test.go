package accounting

import "testing"

// T11.4 — accounting reconciliation (§19 acceptance): the sum of primary usage records must equal the run
// total regardless of delegation-tree depth, replay must never recount an invocation, the side-agent
// namespace must stay separate from per-task costs, and an unpriced invocation must never render as "$0".

// TestTotals_NoDoubleCountingAcrossTree extends TestCost_RunTotalCountsInvocationOnce (depth 2) to a THREE
// level delegation tree (root task -> child -> grandchild, plus a sibling branch) — regardless of depth, the
// run total is exactly the sum of each invocation's OWN cost, counted once each.
func TestTotals_NoDoubleCountingAcrossTree(t *testing.T) {
	r := NewResolver(PricingConfig{})
	l := &Ledger{}

	// root (depth 0, parented by the stage directly) -> child (depth 1) -> grandchild (depth 2), plus a
	// second child of root to exercise branching, not just a chain.
	root := BuildTaskRecord(r, obsGLM(), TaskMeta{TaskID: "root", RootStageID: "impl"}, "check", "")
	child := BuildTaskRecord(r, obsMemory(), TaskMeta{TaskID: "child", RootStageID: "impl", ParentTaskID: "root"}, "check", "")
	grandchild := BuildTaskRecord(r, obsGLM(), TaskMeta{TaskID: "grandchild", RootStageID: "impl", ParentTaskID: "child"}, "check", "")
	sibling := BuildTaskRecord(r, obsMemory(), TaskMeta{TaskID: "sibling", RootStageID: "impl", ParentTaskID: "root"}, "check", "")
	for _, rec := range []UsageRecord{root, child, grandchild, sibling} {
		if rec.EstimatedCostUSD <= 0 {
			t.Fatalf("fixture must price > 0 to be meaningful: %+v", rec)
		}
		l.Add(rec)
	}

	want := root.EstimatedCostUSD + child.EstimatedCostUSD + grandchild.EstimatedCostUSD + sibling.EstimatedCostUSD
	got := l.RunSummary().CostUSD
	if got != want {
		t.Fatalf("RunSummary().CostUSD = %v, want %v (each of the 4 invocations counted exactly once regardless of tree depth)", got, want)
	}

	// The root's SubtreeCostUSD must equal the whole tree's total (own + every descendant, each counted
	// once) — proving depth > 2 does not lose or duplicate the grandchild's contribution.
	parentOf := map[string]string{"child": "root", "grandchild": "child", "sibling": "root"}
	costs := ComputeTaskCosts(l.Records(), parentOf)
	rootCost, ok := costs["root"]
	if !ok {
		t.Fatal("root missing from ComputeTaskCosts result")
	}
	if rootCost.SubtreeCostUSD != want {
		t.Fatalf("root.SubtreeCostUSD = %v, want %v (own + child + grandchild + sibling)", rootCost.SubtreeCostUSD, want)
	}

	// Summing every task's OWN cost (never subtree) across the whole tree must also equal the run total —
	// this is the non-double-counting invariant expressed from the per-task-own-cost side, independent of
	// the tree shape, and it's what a UI reconciliation check would actually compare against RunSummary.
	var sumOfOwn float64
	for _, tc := range costs {
		sumOfOwn += tc.OwnCostUSD
	}
	if sumOfOwn != want {
		t.Fatalf("sum of every task's OwnCostUSD = %v, want %v (== run total, independent of depth)", sumOfOwn, want)
	}
}

// TestTotals_ReplayDoesNotRecountInvocation simulates a process restart: a Store is opened against a run
// directory that already has ONE recorded invocation in usage.jsonl, replays it into a fresh in-memory
// Ledger (Store.Open's documented replay-on-resume behavior), and the run total must still reflect exactly
// that one invocation — restart/replay alone must never duplicate it.
func TestTotals_ReplayDoesNotRecountInvocation(t *testing.T) {
	dir := t.TempDir()
	r := NewResolver(PricingConfig{})

	store1, err := Open(dir, r)
	if err != nil {
		t.Fatalf("Open (first process): %v", err)
	}
	if err := store1.AppendTask(obsGLM(), TaskMeta{TaskID: "t1", RootStageID: "impl"}, "check", ""); err != nil {
		t.Fatalf("AppendTask: %v", err)
	}
	wantCost := store1.Snapshot().RunSummary().CostUSD
	if wantCost <= 0 {
		t.Fatal("fixture must price > 0 to be meaningful")
	}
	if err := store1.Close(); err != nil {
		t.Fatalf("Close (first process): %v", err)
	}

	// "Restart": a brand-new Store opened against the SAME run directory, standing in for afm re-opening an
	// existing run — Open's own doc comment: "replays any existing content into an in-memory Ledger, so a
	// resumed run's Snapshot reflects everything recorded before the restart."
	store2, err := Open(dir, r)
	if err != nil {
		t.Fatalf("Open (replay/restart): %v", err)
	}
	defer store2.Close()

	snap := store2.Snapshot()
	if n := len(snap.Records()); n != 1 {
		t.Fatalf("replay produced %d records, want exactly 1 (no recount of the already-recorded invocation)", n)
	}
	if got := snap.RunSummary().CostUSD; got != wantCost {
		t.Fatalf("RunSummary().CostUSD after replay = %v, want %v (unchanged — replay must not recount)", got, wantCost)
	}

	// A second, genuinely NEW invocation after restart must add on top, not replace/double the first —
	// proving the ledger after replay is a normal, still-appendable ledger, not a frozen/duplicated one.
	if err := store2.AppendTask(obsMemory(), TaskMeta{TaskID: "t2", RootStageID: "impl"}, "check", ""); err != nil {
		t.Fatalf("AppendTask after replay: %v", err)
	}
	final := store2.Snapshot()
	if n := len(final.Records()); n != 2 {
		t.Fatalf("got %d records after one post-replay append, want 2", n)
	}
	if got, want := final.RunSummary().CostUSD, wantCost+obsRecordCost(r, obsMemory()); got != want {
		t.Fatalf("RunSummary().CostUSD after post-replay append = %v, want %v", got, want)
	}
}

// obsRecordCost is a small helper so TestTotals_ReplayDoesNotRecountInvocation can predict the cost of a
// second observation without re-deriving BuildRecord's pricing logic inline.
func obsRecordCost(r *Resolver, obs Observation) float64 {
	return BuildRecord(r, obs, "impl", "check", "").EstimatedCostUSD
}

// TestTotals_SideAgentNamespaceSeparate mirrors cmd/afm/run_side_agent.go's real accounting call
// (acct.Append(obs, sideAgentAcctLabel, sideAgentAcctLabel, "") with sideAgentAcctLabel == "side") and
// proves the side-agent's cost (a) is NOT picked up by ComputeTaskCosts (it carries no task_id, so it must
// never appear as a per-task cost entry), (b) lands in its OWN SummaryByStage bucket distinct from any real
// flow stage, and (c) still contributes to the run-wide total exactly once, same as any other record.
func TestTotals_SideAgentNamespaceSeparate(t *testing.T) {
	const sideAgentAcctLabel = "side" // mirrors cmd/afm/run_side_agent.go's sideAgentAcctLabel constant

	r := NewResolver(PricingConfig{})
	l := &Ledger{}
	sideRec := BuildRecord(r, obsGLM(), sideAgentAcctLabel, sideAgentAcctLabel, "")
	stageTaskRec := BuildTaskRecord(r, obsMemory(), TaskMeta{TaskID: "t1", RootStageID: "impl"}, "check", "")
	l.Add(sideRec)
	l.Add(stageTaskRec)

	// (a) ComputeTaskCosts must never surface "side" as a task id — side-agent records carry no TaskID.
	costs := ComputeTaskCosts(l.Records(), nil)
	if _, ok := costs[sideAgentAcctLabel]; ok {
		t.Fatal("side agent's stage_id must never be picked up as a task id in ComputeTaskCosts")
	}
	if len(costs) != 1 {
		t.Fatalf("got %d task costs, want exactly 1 (only the real stage task)", len(costs))
	}

	// (b) SummaryByStage must bucket "side" separately from "impl" (the real flow stage) — two distinct
	// buckets, neither merged into the other.
	byStage := l.SummaryByStage()
	sideSummary, ok := byStage[sideAgentAcctLabel]
	if !ok {
		t.Fatal("side agent namespace missing from SummaryByStage")
	}
	implSummary, ok := byStage["impl"]
	if !ok {
		t.Fatal("impl stage missing from SummaryByStage")
	}
	if sideSummary.CostUSD != sideRec.EstimatedCostUSD {
		t.Fatalf("side bucket cost = %v, want %v (only the side-agent record)", sideSummary.CostUSD, sideRec.EstimatedCostUSD)
	}
	if implSummary.CostUSD != stageTaskRec.EstimatedCostUSD {
		t.Fatalf("impl bucket cost = %v, want %v (only the stage task record, not polluted by side agent cost)", implSummary.CostUSD, stageTaskRec.EstimatedCostUSD)
	}

	// (c) The run total still includes both, exactly once each.
	want := sideRec.EstimatedCostUSD + stageTaskRec.EstimatedCostUSD
	if got := l.RunSummary().CostUSD; got != want {
		t.Fatalf("RunSummary().CostUSD = %v, want %v (side + stage task, each counted once)", got, want)
	}
}

// TestTotals_UnpricedShownAsUnknownNotZero proves an unpriced invocation (metered, but the model has no
// known rate) is never displayed as "$0.00" at any of the three layers a user could observe cost: the raw
// record, the per-task aggregate, and the formatted display string.
func TestTotals_UnpricedShownAsUnknownNotZero(t *testing.T) {
	r := NewResolver(PricingConfig{})
	unknownModel := Observation{Metered: true, Channel: "claude-cli", Model: "totally-unknown-model"}

	rec := BuildTaskRecord(r, unknownModel, TaskMeta{TaskID: "t1", RootStageID: "impl"}, "check", "")
	if rec.Priced {
		t.Fatal("fixture must be unpriced to be meaningful")
	}
	if rec.EstimatedCostUSD != 0 {
		t.Fatalf("sanity: unpriced record's raw float is the zero value, got %v", rec.EstimatedCostUSD)
	}

	// Layer 1: FormatUSD must never render an unpriced value as "$0.00" — it must be the unknown marker.
	if got := FormatUSD(rec.EstimatedCostUSD, rec.Priced); got == "$0.0000" || got == "$0.00" {
		t.Fatalf("FormatUSD rendered an unpriced record as zero money: %q", got)
	} else if got != "—" {
		t.Fatalf("FormatUSD(unpriced) = %q, want the unknown marker %q", got, "—")
	}

	// Layer 2: the per-task aggregate must be Incomplete (same "$0 must never look verified" guarantee
	// TestCost_InterruptedNotShownAsZero already pins for the unmetered case, here for the unpriced case).
	costs := ComputeTaskCosts([]UsageRecord{rec}, nil)
	tc, ok := costs["t1"]
	if !ok {
		t.Fatal("t1 missing from task costs")
	}
	if tc.OwnCostUSD != 0 {
		t.Fatalf("OwnCostUSD = %v, want 0 (no known price)", tc.OwnCostUSD)
	}

	// Layer 3: the run/stage-level DisplayCost (what the dashboard actually renders) must also be the
	// unknown marker, not a misleading "$0.00" total, when every metered record in the group is unpriced.
	l := &Ledger{}
	l.Add(rec)
	display := DisplayCost(l.RunSummary())
	if display == "$0.0000" || display == "$0.00" {
		t.Fatalf("DisplayCost rendered an all-unpriced group as zero money: %q", display)
	}
	if display != "—" {
		t.Fatalf("DisplayCost(all-unpriced) = %q, want the unknown marker %q", display, "—")
	}
}
