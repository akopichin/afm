package mcpserver

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/akopichin/afm-engine/engine"
	"github.com/akopichin/afm/contracts/accounting"
	"github.com/akopichin/afm/contracts/assistants"
	"github.com/akopichin/afm/contracts/budget"
	"github.com/akopichin/afm/contracts/control"
	"github.com/akopichin/afm/contracts/state"
	"github.com/akopichin/afm/pkg/server"
)

func actionsOf(t *testing.T, res statusResult, typ string) []pendingAction {
	t.Helper()
	var out []pendingAction
	for _, a := range res.PendingActions {
		if a.Type == typ {
			out = append(out, a)
		}
	}
	return out
}

func TestStatusQuestionsInTwoStagesGiveTwoActions(t *testing.T) {
	h := newHarness(t, "reply")
	h.run.setStatus("a", state.StatusAwaitingUserInput)
	h.run.setStatus("b", state.StatusAwaitingUserInput)
	h.run.setStatus("c", state.StatusRunning)
	h.ask("a", "q1", "first?")
	h.ask("b", "q1", "second?")

	res := h.svc.buildStatus(context.Background())

	qs := actionsOf(t, res, actionQuestion)
	if len(qs) != 2 || !res.AttentionRequired {
		t.Fatalf("want two question actions, got %+v", res.PendingActions)
	}
	if qs[0].StageID != "a" || qs[0].Question != "first?" || qs[0].QuestionID != "q1" || qs[0].Phase != "planning" {
		t.Fatalf("unexpected first action %+v", qs[0])
	}
	if !slices.Equal(qs[0].AllowedActions, []string{toolActionReply}) {
		t.Fatalf("reply must be allowed with group reply, got %v", qs[0].AllowedActions)
	}
	if !slices.Equal(res.ActiveStages, []string{"c"}) || res.Counts["awaiting_user_input"] != 2 {
		t.Fatalf("counts/active wrong: %+v %v", res.Counts, res.ActiveStages)
	}
}

func TestStatusReadOnlyOffersNoActions(t *testing.T) {
	h := newHarness(t, "")
	h.run.setStatus("a", state.StatusFailed)
	res := h.svc.buildStatus(context.Background())
	if got := actionsOf(t, res, actionFailed); len(got) != 1 || len(got[0].AllowedActions) != 0 {
		t.Fatalf("read-only must list the failure with no actions: %+v", res.PendingActions)
	}
	if len(res.Permissions) != 0 {
		t.Fatalf("permissions = %v", res.Permissions)
	}
}

func TestStatusApprovalCarriesSnapshotRevision(t *testing.T) {
	h := newHarness(t, "approve,stages")
	h.run.setStatus("plan", state.StatusAwaitingApproval)
	h.writeFile("plan/plan.md", "# p")
	h.approval.snaps["plan"] = control.ApprovalSnapshot{RunID: "run-1", StageID: "plan", Revision: "rev-7", PlanMarkdown: "# p"}

	res := h.svc.buildStatus(context.Background())

	got := actionsOf(t, res, actionApproval)
	if len(got) != 1 || got[0].Revision != "rev-7" {
		t.Fatalf("approval action = %+v", got)
	}
	if !slices.Contains(got[0].AllowedActions, toolActionApprove) || !slices.Contains(got[0].AllowedActions, toolActionRevise) {
		t.Fatalf("allowed = %v", got[0].AllowedActions)
	}
	if want := planURI("run-1", "plan", "rev-7"); !slices.Contains(got[0].Resources, want) {
		t.Fatalf("plan resource %q missing in %v", want, got[0].Resources)
	}
}

func TestStatusApprovalUnavailableOffersNoApprove(t *testing.T) {
	for name, tune := range map[string]func(*harness){
		"snapshot error": func(h *harness) {
			h.approval.snapErr = &engine.Error{Kind: engine.KindConflict, Code: control.CodeApprovalUnavailable, Message: "legacy state"}
		},
		"no approval support": nil,
	} {
		t.Run(name, func(t *testing.T) {
			var cfg []func(*Config)
			if tune == nil {
				cfg = append(cfg, func(c *Config) { c.Approval = nil })
			}
			h := newHarness(t, "approve,stages", cfg...)
			if tune != nil {
				tune(h)
			}
			h.writeFile("plan/plan.md", "# p")
			h.run.setStatus("plan", state.StatusAwaitingApproval)

			got := actionsOf(t, h.svc.buildStatus(context.Background()), actionApproval)

			if len(got) != 1 || got[0].Reason != control.CodeApprovalUnavailable || got[0].Revision != "" {
				t.Fatalf("approval action = %+v", got)
			}
			if slices.Contains(got[0].AllowedActions, toolActionApprove) {
				t.Fatalf("approve must not be offered without a snapshot: %v", got[0].AllowedActions)
			}
		})
	}
}

func TestStatusDistinguishesCauses(t *testing.T) {
	h := newHarness(t, "stages,budget")
	h.run.setStatus("f", state.StatusFailed)
	h.run.setStatus("hf", state.StatusHookFailed)
	h.run.setStage("p", state.StageState{Status: state.StatusPaused, PausedFrom: state.StatusRunning})
	h.run.setStage("q", state.StageState{Status: state.StatusAwaitingQuota, Quota: &state.QuotaWait{PauseReason: "no_reset"}})
	h.run.setStatus("ok", state.StatusDone)
	h.budgets.views = []budget.View{
		{Scope: budget.RunScope(), Revision: 3, Hold: &budget.Hold{Reason: budget.HoldLimitReached}},
		{Scope: budget.StageScope("f"), Revision: 1},
	}

	res := h.svc.buildStatus(context.Background())

	byType := map[string]pendingAction{}
	for _, a := range res.PendingActions {
		byType[a.Type] = a
	}
	for _, typ := range []string{actionFailed, actionHookFailed, actionPaused, actionQuota, actionBudgetHold} {
		if _, ok := byType[typ]; !ok {
			t.Errorf("missing %s in %+v", typ, res.PendingActions)
		}
	}
	if len(res.PendingActions) != 5 {
		t.Errorf("want five distinct actions, got %+v", res.PendingActions)
	}
	if byType[actionPaused].Reason != "running" || byType[actionQuota].Reason != "no_reset" {
		t.Errorf("reasons: paused=%q quota=%q", byType[actionPaused].Reason, byType[actionQuota].Reason)
	}
	hold := byType[actionBudgetHold]
	if hold.Scope != "run" || hold.Revision != "3" || !slices.Equal(hold.AllowedActions, []string{toolActionBudgetSet, toolActionBudgetCont}) {
		t.Errorf("budget hold = %+v", hold)
	}
	if len(byType[actionHookFailed].AllowedActions) != 0 {
		t.Errorf("hook_failed has no MCP action: %v", byType[actionHookFailed].AllowedActions)
	}
}

func TestStatusCursorChangesOnlyWithModel(t *testing.T) {
	h := newHarness(t, "reply")
	h.run.setStatus("a", state.StatusRunning)
	first := h.svc.buildStatus(context.Background()).Cursor
	if again := h.svc.buildStatus(context.Background()).Cursor; again != first {
		t.Fatalf("cursor moved without a change: %s -> %s", first, again)
	}
	h.run.setStatus("a", state.StatusFailed)
	if next := h.svc.buildStatus(context.Background()).Cursor; next == first {
		t.Fatal("cursor did not move after a stage failed")
	}
	if !h.svc.cursorBelongs(first) || h.svc.cursorBelongs("v1:other:0123456789abcdef") || h.svc.cursorBelongs("junk") {
		t.Fatal("cursorBelongs misjudges run ownership")
	}
}

func TestStatusCostMoneyAndUnknown(t *testing.T) {
	run := &accounting.CostView{
		Coverage: "partial", TotalTokens: 1500, UncachedInput: 1000, Output: 500, Metered: 2, PricedInvocations: 2, EstimatedCostUSD: 1.25,
	}
	unmetered := &accounting.CostView{Coverage: "none", Unmetered: 1}
	issues := []accounting.CoverageIssue{{Kind: "unmetered"}, {Kind: "unpriced"}}
	cases := []struct {
		name      string
		showMoney bool
		bundle    *accounting.CostBundle
		wantUSD   string // "absent", "null" or a number
		wantToken bool
		want      costSummary
	}{
		{"money hidden", false, &accounting.CostBundle{Run: run, HasData: true, Health: accounting.HealthOK, Issues: issues}, "absent", true,
			costSummary{Health: "ok", HasData: true, Coverage: "partial", Issues: 2}},
		{"money shown", true, &accounting.CostBundle{Run: run, HasData: true, Health: accounting.HealthOK}, "1.25", true,
			costSummary{Health: "ok", HasData: true, Coverage: "partial"}},
		{"unmetered usage is unknown tokens", true, &accounting.CostBundle{Run: unmetered, HasData: true, Health: accounting.HealthAbsent}, "null", false,
			costSummary{Health: string(accounting.HealthAbsent), HasData: true, Coverage: "none"}},
		{"no data hides nothing but money", false, &accounting.CostBundle{Health: accounting.HealthUnavailable}, "absent", false,
			costSummary{Health: string(accounting.HealthUnavailable)}},
		{"no data shown money is null", true, &accounting.CostBundle{Health: accounting.HealthUnavailable}, "null", false,
			costSummary{Health: string(accounting.HealthUnavailable)}},
		{"no provider", true, nil, "null", false, costSummary{Health: string(accounting.HealthUnavailable)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, "", func(c *Config) {
				c.ShowMoney = tc.showMoney
				if tc.bundle != nil {
					c.Cost = fakeCost{*tc.bundle}
				}
			})
			got := h.svc.buildStatus(context.Background()).Cost
			if got.Health != tc.want.Health || got.HasData != tc.want.HasData || got.Coverage != tc.want.Coverage || got.Issues != tc.want.Issues {
				t.Fatalf("cost = %+v, want %+v", got, tc.want)
			}
			if (got.Tokens != nil) != tc.wantToken || (tc.wantToken && got.Tokens.Total != 1500) {
				t.Fatalf("tokens = %+v", got.Tokens)
			}
			raw, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			usd, present := fields["estimated_cost_usd"]
			switch {
			case tc.wantUSD == "absent" && present:
				t.Fatalf("money field present while hidden: %s", usd)
			case tc.wantUSD != "absent" && string(usd) != tc.wantUSD:
				t.Fatalf("estimated_cost_usd = %s, want %s", usd, tc.wantUSD)
			default:
			}
			if string(fields["tokens"]) == "null" == tc.wantToken {
				t.Fatalf("tokens field = %s", fields["tokens"])
			}
		})
	}
}

func TestStatusReportsRunStatus(t *testing.T) {
	h := newHarness(t, "")
	h.run.mu.Lock()
	h.run.rs.RunStatus = state.RunStatusInterrupted
	h.run.mu.Unlock()
	h.run.setStatus("a", state.StatusRunning)
	if got := h.svc.buildStatus(context.Background()).RunStatus; got != state.RunStatusInterrupted {
		t.Fatalf("run_status = %q, want %q", got, state.RunStatusInterrupted)
	}
	h.run.mu.Lock()
	h.run.rs.RunStatus = state.RunStatusFinished
	h.run.mu.Unlock()
	if got := h.svc.buildStatus(context.Background()).RunStatus; got != state.RunStatusFinished {
		t.Fatalf("run_status = %q, want %q", got, state.RunStatusFinished)
	}
}

func TestStatusToolOverMCP(t *testing.T) {
	h := newHarness(t, "reply")
	h.run.setStatus("a", state.StatusAwaitingUserInput)
	h.ask("a", "q1", "go?")
	session := h.connect(t, nil)

	res, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "afm_run_status"})
	if err != nil || res.IsError {
		t.Fatalf("call: %v %+v", err, res)
	}
	var got statusResult
	decode(t, res, &got)
	if got.RunID != "run-1" || len(got.PendingActions) != 1 || got.Cursor == "" || !slices.Equal(got.Permissions, []Group{GroupReply}) {
		t.Fatalf("status = %+v", got)
	}
}

// TestStatusReportsPreCompletionRounds checks the raw JSON, because the keys are the contract: a blocked round
// carries the blocked flag and one pending action, a running review neither, a stage without a round no key.
func TestStatusReportsPreCompletionRounds(t *testing.T) {
	ds := &declaredStages{configs: map[string]server.StageConfig{
		"blocked":   {PreCompletionMaxFailures: 2},
		"reviewing": {PreCompletionMaxFailures: 1},
	}}
	h := newHarness(t, "", func(c *Config) { c.Stages = ds })
	round := func(id string, decision state.PreCompletionDecision) *state.PreCompletionRound {
		return &state.PreCompletionRound{
			ID: id, Phase: "execution", CorrectionCount: 1, Decision: decision,
			TaskIDs: []assistants.TaskID{"t-1"},
		}
	}
	h.run.setStage("blocked", state.StageState{
		Status: state.StatusAwaitingPreCompletion, PreCompletion: round("round-3", state.PreCompletionNeedsDecision),
	})
	h.run.setStage("reviewing", state.StageState{
		Status: state.StatusAwaitingPreCompletion, PreCompletion: round("round-5", state.PreCompletionReviewing),
	})
	h.run.setStatus("plain", state.StatusRunning)
	session := h.connect(t, nil)

	res, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "afm_run_status"})
	if err != nil || res.IsError {
		t.Fatalf("call: %v %+v", err, res)
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Stages         []map[string]any `json:"stages"`
		PendingActions []map[string]any `json:"pending_actions"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	byID := map[string]map[string]any{}
	for _, st := range got.Stages {
		byID[st["id"].(string)] = st
	}

	blocked := byID["blocked"]
	wantRound := map[string]any{
		"round_id": "round-3", "phase": "execution", "decision": "needs_decision",
		"correction_count": float64(1), "max_failures": float64(2), "task_ids": []any{"t-1"},
	}
	if !reflect.DeepEqual(blocked["pre_completion"], wantRound) || blocked["pre_completion_blocked"] != any(true) {
		t.Fatalf("blocked stage = %v", blocked)
	}
	reviewing := byID["reviewing"]
	if round, _ := reviewing["pre_completion"].(map[string]any); round["decision"] != "reviewing" {
		t.Fatalf("reviewing stage = %v", reviewing)
	}
	if _, ok := reviewing["pre_completion_blocked"]; ok {
		t.Fatalf("a running review must not be blocked: %v", reviewing)
	}
	for _, key := range []string{"pre_completion", "pre_completion_blocked"} {
		if _, ok := byID["plain"][key]; ok {
			t.Fatalf("stage without a round has %q: %v", key, byID["plain"])
		}
	}

	if len(got.PendingActions) != 1 || got.PendingActions[0]["stage_id"] != "blocked" ||
		got.PendingActions[0]["type"] != "assistants_blocked" || got.PendingActions[0]["reason"] != "pre_completion" {
		t.Fatalf("pending_actions = %v", got.PendingActions)
	}
}
