package accounting

import (
	"os"
	"path/filepath"
	"testing"
)

// TestUsage_OldRecordsStillParse proves a pre-T3.8 usage.jsonl line (no task_id/root_stage_id/
// parent_task_id/execution_id/profile_id at all) still parses cleanly — the new fields are all omitempty
// and zero-valued.
func TestUsage_OldRecordsStillParse(t *testing.T) {
	dir := t.TempDir()
	oldLine := `{"record_version":1,"stage_id":"backend","phase":"implementation","metered":true,"priced":true,"estimated_cost_usd":0.5,"tokens":{"uncached_input":100,"output":50}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "usage.jsonl"), []byte(oldLine), 0644); err != nil {
		t.Fatal(err)
	}
	l, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	recs := l.Records()
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	if recs[0].TaskID != "" || recs[0].ProfileID != "" || recs[0].RootStageID != "" {
		t.Fatalf("old record got non-empty task fields: %+v", recs[0])
	}
	if recs[0].EstimatedCostUSD != 0.5 {
		t.Fatalf("cost = %v, want 0.5 (old fields still parse)", recs[0].EstimatedCostUSD)
	}
}

func TestCost_OwnVsSubtreeSeparated(t *testing.T) {
	r := NewResolver(PricingConfig{})
	parentRec := BuildTaskRecord(r, obsGLM(), TaskMeta{TaskID: "p1", RootStageID: "impl", ProfileID: "reviewer"}, "check", "")
	childRec := BuildTaskRecord(r, obsGLM(), TaskMeta{TaskID: "c1", RootStageID: "impl", ParentTaskID: "p1", ProfileID: "security"}, "check", "")

	if parentRec.EstimatedCostUSD <= 0 || childRec.EstimatedCostUSD <= 0 {
		t.Fatalf("fixture must price > 0 to be a meaningful test: parent=%v child=%v", parentRec.EstimatedCostUSD, childRec.EstimatedCostUSD)
	}

	costs := ComputeTaskCosts([]UsageRecord{parentRec, childRec}, map[string]string{"c1": "p1"})

	p, ok := costs["p1"]
	if !ok {
		t.Fatal("p1 missing from ComputeTaskCosts result")
	}
	if p.OwnCostUSD != parentRec.EstimatedCostUSD {
		t.Fatalf("p1 OwnCostUSD = %v, want %v (its own invocation only)", p.OwnCostUSD, parentRec.EstimatedCostUSD)
	}
	wantSubtree := parentRec.EstimatedCostUSD + childRec.EstimatedCostUSD
	if p.SubtreeCostUSD != wantSubtree {
		t.Fatalf("p1 SubtreeCostUSD = %v, want %v (own + child)", p.SubtreeCostUSD, wantSubtree)
	}

	c, ok := costs["c1"]
	if !ok {
		t.Fatal("c1 missing from ComputeTaskCosts result")
	}
	if c.OwnCostUSD != childRec.EstimatedCostUSD || c.SubtreeCostUSD != childRec.EstimatedCostUSD {
		t.Fatalf("c1 own/subtree = %v/%v, want both = %v (leaf task)", c.OwnCostUSD, c.SubtreeCostUSD, childRec.EstimatedCostUSD)
	}
}

func TestCost_RunTotalCountsInvocationOnce(t *testing.T) {
	r := NewResolver(PricingConfig{})
	l := &Ledger{}
	parentRec := BuildTaskRecord(r, obsGLM(), TaskMeta{TaskID: "p1", RootStageID: "impl"}, "check", "")
	childRec := BuildTaskRecord(r, obsGLM(), TaskMeta{TaskID: "c1", RootStageID: "impl", ParentTaskID: "p1"}, "check", "")
	l.Add(parentRec)
	l.Add(childRec)

	run := l.RunSummary()
	want := parentRec.EstimatedCostUSD + childRec.EstimatedCostUSD
	if run.CostUSD != want {
		t.Fatalf("RunSummary CostUSD = %v, want %v (each invocation counted exactly once)", run.CostUSD, want)
	}

	// The same conclusion must hold from the task-cost view: the root task's subtree cost (which already
	// includes its child) must NOT be added a second time to get the run total — summing per-task
	// SubtreeCostUSD across ALL tasks would double count a child under both itself and its parent.
	costs := ComputeTaskCosts(l.Records(), map[string]string{"c1": "p1"})
	naiveDoubleCount := costs["p1"].SubtreeCostUSD + costs["c1"].SubtreeCostUSD
	if naiveDoubleCount == run.CostUSD {
		t.Fatal("test fixture degenerate: naive double-count accidentally equals the correct total")
	}
	if run.CostUSD*2 == naiveDoubleCount {
		// Expected shape of the double-count bug, just documenting it is NOT what RunSummary does.
		t.Log("confirmed: summing every task's SubtreeCostUSD would double count; RunSummary avoids this")
	}
}

func TestCost_SideAgentNamespaceUntouched(t *testing.T) {
	r := NewResolver(PricingConfig{})
	l := &Ledger{}
	// Side-agent cost: recorded via the plain (non-task) Append/BuildRecord path, stage_id set to its own
	// label, no task_id — mirrors cmd/afm/run_side_agent.go's acct.Append(obs, sideAgentAcctLabel, ...).
	sideRec := BuildRecord(r, obsGLM(), "side_agent", "side_agent", "")
	l.Add(sideRec)
	taskRec := BuildTaskRecord(r, obsGLM(), TaskMeta{TaskID: "t1", RootStageID: "impl"}, "check", "")
	l.Add(taskRec)

	costs := ComputeTaskCosts(l.Records(), nil)
	if _, ok := costs["side_agent"]; ok {
		t.Fatal("side-agent's stage_id must never be picked up as a task id in ComputeTaskCosts")
	}
	if len(costs) != 1 {
		t.Fatalf("got %d task costs, want exactly 1 (only the real task record)", len(costs))
	}
	if _, ok := costs["t1"]; !ok {
		t.Fatal("t1 missing from task costs")
	}
}

func TestCost_InterruptedNotShownAsZero(t *testing.T) {
	r := NewResolver(PricingConfig{})
	interrupted := Observation{Metered: false, Reason: ReasonInterrupted}
	rec := BuildTaskRecord(r, interrupted, TaskMeta{TaskID: "t1", RootStageID: "impl"}, "check", "")
	if rec.EstimatedCostUSD != 0 {
		t.Fatalf("sanity: unmetered record should have 0 estimated cost, got %v", rec.EstimatedCostUSD)
	}

	costs := ComputeTaskCosts([]UsageRecord{rec}, nil)
	c, ok := costs["t1"]
	if !ok {
		t.Fatal("t1 missing from task costs")
	}
	if c.OwnCostUSD != 0 {
		t.Fatalf("OwnCostUSD = %v, want 0 (no priced data)", c.OwnCostUSD)
	}
	if !c.Incomplete {
		t.Fatal("an interrupted (unmetered) invocation's $0 total must be flagged Incomplete, not presented as a verified $0")
	}
}
