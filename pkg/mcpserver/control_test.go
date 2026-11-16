package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/akopichin/afm-engine/engine"
	"github.com/akopichin/afm/contracts/budget"
	"github.com/akopichin/afm/contracts/control"
	"github.com/akopichin/afm/contracts/state"
	"github.com/akopichin/afm/pkg/server"
)

func TestControlApproveUsesApproveExpectedOnly(t *testing.T) {
	h := newHarness(t, "approve")
	awaitingPlan(h, "# plan")

	res := h.svc.decide(context.Background(), decideInput{RunID: "run-1", StageID: "plan", Decision: decisionApprove, ExpectedRevision: "rev-1"})

	if res.IsError {
		t.Fatalf("error: %+v", res.StructuredContent)
	}
	var got controlResult
	decode(t, res, &got)
	if got.Applied != decisionApprove || got.ApprovedRevision != "rev-1" || got.TransitionSeq != 42 || got.State.StageID != "plan" || got.State.Cursor == "" {
		t.Fatalf("result = %+v", got)
	}
	req := h.approval.approved
	if len(req) != 1 || req[0].RunID != "run-1" || req[0].StageID != "plan" || req[0].ExpectedRevision != "rev-1" {
		t.Fatalf("ApproveExpected got %+v", req)
	}
	if calls := h.cmds.callList(); len(calls) != 0 {
		t.Fatalf("commands touched: %v", calls)
	}
}

func TestControlApproveNeedsRevisionAndApproval(t *testing.T) {
	h := newHarness(t, "approve")
	res := h.svc.decide(context.Background(), decideInput{RunID: "run-1", StageID: "plan", Decision: decisionApprove})
	if got := errCode(t, res); got != codeInvalid {
		t.Fatalf("missing revision: %s", got)
	}
	if h.approval.approveCount() != 0 {
		t.Fatal("ApproveExpected must not be called without a revision")
	}
	noApproval := newHarness(t, "approve", func(c *Config) { c.Approval = nil })
	res = noApproval.svc.decide(context.Background(), decideInput{RunID: "run-1", StageID: "plan", Decision: decisionApprove, ExpectedRevision: "r"})
	if got := errCode(t, res); got != control.CodeApprovalUnavailable {
		t.Fatalf("no approval support: %s", got)
	}
}

func TestControlConflictsKeepRESTCodesAndReturnState(t *testing.T) {
	h := newHarness(t, "approve,stages")
	h.run.setStatus("plan", state.StatusAwaitingApproval)
	h.writeFile("plan/plan.md", "# plan")
	h.approval.snaps["plan"] = controlSnapWithPlan("old", "# plan")
	cases := map[string]string{
		control.CodeApprovalStale:      control.CodeApprovalStale,
		control.CodeApprovalRunChanged: control.CodeApprovalRunChanged,
	}
	for code, want := range cases {
		h.approval.approveErr = &engine.Error{Kind: engine.KindConflict, Code: code, Message: "no"}
		res := h.svc.decide(context.Background(), decideInput{RunID: "run-1", StageID: "plan", Decision: decisionApprove, ExpectedRevision: "old"})
		var te ToolError
		decode(t, res, &te)
		if !res.IsError || te.Code != want || te.Kind != codeConflict {
			t.Fatalf("%s: %+v", code, te)
		}
		if rest := server.ClassifyEngineError(h.approval.approveErr); rest.Code != te.Code {
			t.Fatalf("MCP code %q differs from REST code %q", te.Code, rest.Code)
		}
		if te.State == nil || te.State.StageStatus != "awaiting_approval" || te.State.Cursor == "" {
			t.Fatalf("a conflict must carry the post-state: %+v", te.State)
		}
	}
	h.cmds.err = &engine.Error{Kind: engine.KindConflict, Message: "paused"}
	res := h.svc.decide(context.Background(), decideInput{RunID: "run-1", StageID: "plan", Decision: decisionRetry})
	if got := errCode(t, res); got != codeConflict {
		t.Fatalf("retry conflict code = %s", got)
	}
}

func TestControlReviseChecksThenRevises(t *testing.T) {
	h := newHarness(t, "stages")
	res := h.svc.decide(context.Background(), decideInput{RunID: "run-1", StageID: "plan", Decision: decisionRevise, Feedback: "more tests"})
	if res.IsError {
		t.Fatalf("error: %+v", res.StructuredContent)
	}
	if got := h.cmds.callList(); !slices.Equal(got, []string{"check:plan", "revise:plan:more tests"}) {
		t.Fatalf("calls = %v", got)
	}
	empty := h.svc.decide(context.Background(), decideInput{RunID: "run-1", StageID: "plan", Decision: decisionRevise})
	if errCode(t, empty) != codeInvalid {
		t.Fatal("empty feedback must be invalid")
	}
}

func TestControlRetryPauseContinue(t *testing.T) {
	h := newHarness(t, "stages")
	ctx := context.Background()
	// Each command moves the run before it returns, so the state in the answer must be read afterwards.
	h.cmds.after = func(call string) {
		switch {
		case strings.HasPrefix(call, "retry:"):
			h.run.setStatus("s", state.StatusRunning)
		case strings.HasPrefix(call, "pause:"):
			h.run.setStatus("s", state.StatusPaused)
		case strings.HasPrefix(call, "continue:"):
			h.run.setStatus("s", state.StatusDone)
		default:
			t.Errorf("unexpected command %s", call)
		}
	}
	results := []*sdkmcp.CallToolResult{
		h.svc.decide(ctx, decideInput{RunID: "run-1", StageID: "s", Decision: decisionRetry}),
		h.svc.pauseOrContinue(ctx, stageInput{RunID: "run-1", StageID: "s"}, toolActionPause),
		h.svc.pauseOrContinue(ctx, stageInput{RunID: "run-1", StageID: "s"}, toolActionContinue),
	}
	for i, want := range []struct{ applied, status string }{
		{decisionRetry, "running"}, {toolActionPause, "paused"}, {toolActionContinue, "done"},
	} {
		if results[i].IsError {
			t.Fatalf("result %d: %+v", i, results[i].StructuredContent)
		}
		var got controlResult
		decode(t, results[i], &got)
		if got.Applied != want.applied || got.State == nil || got.State.StageStatus != want.status || got.State.StageID != "s" || got.State.Cursor == "" {
			t.Fatalf("result %d = %+v (state %+v), want %s/%s", i, got, got.State, want.applied, want.status)
		}
	}
	if got := h.svc.cursorOf(h.svc.buildAttention(ctx, h.run.Snapshot())); got == "" {
		t.Fatal("no current cursor")
	}
	if got := h.cmds.callList(); !slices.Equal(got, []string{"retry:s", "pause:s", "continue:s"}) {
		t.Fatalf("calls = %v", got)
	}
}

func TestControlApproveStateIsReadAfterCommand(t *testing.T) {
	h := newHarness(t, "approve")
	ctx := context.Background()
	h.run.setStatus("plan", state.StatusAwaitingApproval)
	h.writeFile("plan/plan.md", "# p")
	h.approval.snaps["plan"] = control.ApprovalSnapshot{Revision: "rev-1", PlanMarkdown: "# p"}
	before := h.svc.buildStatus(ctx).Cursor
	h.approval.onApprove = func() { h.run.setStatus("plan", state.StatusRunning) }

	res := h.svc.decide(ctx, decideInput{RunID: "run-1", StageID: "plan", Decision: decisionApprove, ExpectedRevision: "rev-1"})

	var got controlResult
	decode(t, res, &got)
	if res.IsError || got.State.StageStatus != "running" || got.State.Cursor == before {
		t.Fatalf("approve answer = %+v, state %+v (cursor before %s)", got, got.State, before)
	}
	if want := h.svc.buildStatus(ctx).Cursor; got.State.Cursor != want {
		t.Fatalf("answer cursor %s is not the post-command cursor %s", got.State.Cursor, want)
	}

	// A conflict that arrives after the run moved reports where the run stands now.
	h.approval.onApprove = func() { h.run.setStatus("plan", state.StatusDone) }
	h.approval.approveErr = &engine.Error{Kind: engine.KindConflict, Code: control.CodeApprovalStale, Message: "stale"}
	res = h.svc.decide(ctx, decideInput{RunID: "run-1", StageID: "plan", Decision: decisionApprove, ExpectedRevision: "rev-1"})
	var te ToolError
	decode(t, res, &te)
	if !res.IsError || te.Code != control.CodeApprovalStale || te.State == nil || te.State.StageStatus != "done" {
		t.Fatalf("conflict = %+v (state %+v)", te, te.State)
	}
}

func TestControlRunMismatchReachesNothing(t *testing.T) {
	h := newHarness(t, "reply,stages,approve,budget")
	ctx := context.Background()
	// An awaiting stage and a held budget are what the post-state would read; a mismatch must read neither.
	h.run.setStatus("s", state.StatusAwaitingApproval)
	h.budgets.views = []budget.View{{Scope: budget.RunScope(), Hold: &budget.Hold{Reason: "limit"}}}
	reads := h.run.reads.Load()
	for name, call := range map[string]func() string{
		"reply": func() string {
			return errCode(t, h.svc.reply(ctx, replyInput{RunID: "old", StageID: "s", Phase: "planning", QuestionID: "q1"}))
		},
		"decide": func() string {
			return errCode(t, h.svc.decide(ctx, decideInput{RunID: "old", StageID: "s", Decision: decisionApprove, ExpectedRevision: "r"}))
		},
		"pause": func() string {
			return errCode(t, h.svc.pauseOrContinue(ctx, stageInput{RunID: "old", StageID: "s"}, toolActionPause))
		},
		"adjust": func() string {
			return errCode(t, h.svc.budgetAdjust(ctx, budgetAdjustInput{RunID: "old", Scope: "run", Unlimited: true}))
		},
		"resume": func() string {
			return errCode(t, h.svc.budgetResume(ctx, budgetResumeInput{RunID: "old", Scope: "run"}))
		},
		"no runid": func() string {
			return errCode(t, h.svc.decide(ctx, decideInput{StageID: "s", Decision: decisionRetry}))
		},
	} {
		if got := call(); got != codeRunChanged {
			t.Errorf("%s: code = %s, want %s", name, got, codeRunChanged)
		}
	}
	if len(h.cmds.callList()) != 0 || h.approval.approveCount() != 0 || len(h.budgets.updates)+len(h.budgets.resumes) != 0 {
		t.Fatal("a run mismatch must make zero engine calls")
	}
	if h.run.reads.Load() != reads || h.approval.snapshotCount() != 0 || h.budgets.listCount() != 0 {
		t.Fatalf("a run mismatch read the run: reader %d->%d, snapshots %d, budget lists %d",
			reads, h.run.reads.Load(), h.approval.snapshotCount(), h.budgets.listCount())
	}
}

func TestControlRejectsBadStageID(t *testing.T) {
	h := newHarness(t, "reply,stages,approve")
	ctx := context.Background()
	for _, id := range []string{"../x", "a/b", ""} {
		if got := errCode(t, h.svc.decide(ctx, decideInput{RunID: "run-1", StageID: id, Decision: decisionRetry})); got != codeInvalid {
			t.Errorf("stage %q: %s", id, got)
		}
		if got := errCode(t, h.svc.reply(ctx, replyInput{RunID: "run-1", StageID: id, Phase: "planning", QuestionID: "q1"})); got != codeInvalid && got != codeQuestionNotPending {
			t.Errorf("reply stage %q: %s", id, got)
		}
	}
	if len(h.cmds.callList()) != 0 {
		t.Fatal("engine reached with a bad stage id")
	}
}

func TestControlNotAllowedWithoutGroup(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, "approve")
	if got := errCode(t, h.svc.decide(ctx, decideInput{RunID: "run-1", StageID: "s", Decision: decisionRevise, Feedback: "x"})); got != codeNotAllowed {
		t.Fatalf("revise without stages: %s", got)
	}
	if got := errCode(t, h.svc.reply(ctx, replyInput{RunID: "run-1", StageID: "s"})); got != codeNotAllowed {
		t.Fatalf("reply without reply group: %s", got)
	}
	if got := errCode(t, h.svc.budgetResume(ctx, budgetResumeInput{RunID: "run-1", Scope: "run"})); got != codeNotAllowed {
		t.Fatalf("budget without group: %s", got)
	}
	g := newHarness(t, "stages")
	if got := errCode(t, g.svc.decide(ctx, decideInput{RunID: "run-1", StageID: "s", Decision: decisionApprove, ExpectedRevision: "r"})); got != codeNotAllowed {
		t.Fatalf("approve without approve group: %s", got)
	}
	if g.approval.approveCount() != 0 || len(h.cmds.callList()) != 0 {
		t.Fatal("a refused call reached the engine")
	}
}

func TestControlNilCollaboratorsAreNotAllowed(t *testing.T) {
	h := newHarness(t, "reply,stages,budget", func(c *Config) { c.Commands, c.Budgets = nil, nil })
	ctx := context.Background()
	h.ask("s", "q1", "?")
	for name, res := range map[string]string{
		"reply":  errCode(t, h.svc.reply(ctx, replyInput{RunID: "run-1", StageID: "s", Phase: "planning", QuestionID: "q1"})),
		"retry":  errCode(t, h.svc.decide(ctx, decideInput{RunID: "run-1", StageID: "s", Decision: decisionRetry})),
		"budget": errCode(t, h.svc.budgetResume(ctx, budgetResumeInput{RunID: "run-1", Scope: "run"})),
	} {
		if res != codeNotAllowed {
			t.Errorf("%s: %s", name, res)
		}
	}
}

func TestControlCancelledRequestDoesNotCancelCommand(t *testing.T) {
	h := newHarness(t, "stages")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res := h.svc.decide(ctx, decideInput{RunID: "run-1", StageID: "s", Decision: decisionRetry})

	if res.IsError {
		t.Fatalf("error: %+v", res.StructuredContent)
	}
	if h.cmds.ctxErr != nil {
		t.Fatalf("the command saw a cancelled context: %v", h.cmds.ctxErr)
	}
}

func TestControlReplyBindsToCurrentQuestion(t *testing.T) {
	h := newHarness(t, "reply")
	h.run.setStatus("s", state.StatusAwaitingUserInput)
	ctx := context.Background()

	none := h.svc.reply(ctx, replyInput{RunID: "run-1", StageID: "s", Phase: "planning", QuestionID: "q1", Answer: "a"})
	if got := errCode(t, none); got != codeQuestionNotPending {
		t.Fatalf("no question: %s", got)
	}
	h.ask("s", "q1", "which?")
	wrong := h.svc.reply(ctx, replyInput{RunID: "run-1", StageID: "s", Phase: "planning", QuestionID: "q9", Answer: "a"})
	if got := errCode(t, wrong); got != codeQuestionNotPending {
		t.Fatalf("other question: %s", got)
	}
	if len(h.cmds.callList()) != 0 {
		t.Fatal("a reply to a question that is not waiting reached the engine")
	}

	ok := h.svc.reply(ctx, replyInput{RunID: "run-1", StageID: "s", Phase: "planning", QuestionID: "q1", Answer: "a", FromOptions: true})
	if ok.IsError {
		t.Fatalf("error: %+v", ok.StructuredContent)
	}
	var got controlResult
	decode(t, ok, &got)
	if got.Applied != toolActionReply || got.State.StageStatus != "awaiting_user_input" {
		t.Fatalf("result = %+v", got)
	}
	if calls := h.cmds.callList(); !slices.Equal(calls, []string{"answer:s:planning:q1:a"}) {
		t.Fatalf("calls = %v", calls)
	}
}

func TestControlBudgetAdjustIsAbsoluteAndDoesNotResume(t *testing.T) {
	h := newHarness(t, "budget")
	ctx := context.Background()

	res := h.svc.budgetAdjust(ctx, budgetAdjustInput{RunID: "run-1", Scope: "stage:impl", ExpectedRevision: 4, Tokens: 500000, MoneyUSD: 2.5, StopPolicy: "medium"})

	if res.IsError {
		t.Fatalf("error: %+v", res.StructuredContent)
	}
	if len(h.budgets.updates) != 1 {
		t.Fatalf("updates = %+v", h.budgets.updates)
	}
	u := h.budgets.updates[0]
	want := budget.Override{Kind: budget.OverrideLimit, Spec: budget.Spec{Tokens: 500000, MoneyUSD: 2.5, StopPolicy: budget.PolicyMedium}}
	if u.Scope != "stage:impl" || u.ExpectedRevision != 4 || u.Override != want || u.Resume || u.SaveToYAML {
		t.Fatalf("update = %+v", u)
	}
	var got controlResult
	decode(t, res, &got)
	if got.Applied != toolActionBudgetSet || got.Budget == nil {
		t.Fatalf("result = %+v", got)
	}

	unlimited := h.svc.budgetAdjust(ctx, budgetAdjustInput{RunID: "run-1", Scope: "run", ExpectedRevision: 1, Unlimited: true})
	if unlimited.IsError || h.budgets.updates[1].Override.Kind != budget.OverrideUnlimited {
		t.Fatalf("unlimited: %+v", h.budgets.updates)
	}
	for name, in := range map[string]budgetAdjustInput{
		"both":       {RunID: "run-1", Scope: "run", Unlimited: true, Tokens: 1},
		"neither":    {RunID: "run-1", Scope: "run"},
		"bad scope":  {RunID: "run-1", Scope: "nonsense", Tokens: 1},
		"bad policy": {RunID: "run-1", Scope: "run", Tokens: 1, StopPolicy: "wild"},
	} {
		if got := errCode(t, h.svc.budgetAdjust(ctx, in)); got != codeInvalid {
			t.Errorf("%s: %s", name, got)
		}
	}
	if len(h.budgets.updates) != 2 {
		t.Fatalf("invalid input reached the engine: %+v", h.budgets.updates)
	}
}

func TestControlBudgetResume(t *testing.T) {
	h := newHarness(t, "budget")
	res := h.svc.budgetResume(context.Background(), budgetResumeInput{RunID: "run-1", Scope: "run", ExpectedRevision: 7})
	if res.IsError || len(h.budgets.resumes) != 1 || h.budgets.resumes[0].ExpectedRevision != 7 || h.budgets.resumes[0].Scope != "run" {
		t.Fatalf("resume: %+v %+v", res.StructuredContent, h.budgets.resumes)
	}
}

func approveRevision(h *harness, revision string) *sdkmcp.CallToolResult {
	return h.svc.decide(context.Background(), decideInput{RunID: "run-1", StageID: "plan", Decision: decisionApprove, ExpectedRevision: revision})
}

// Calling afm_stage_decide directly applies the policy behind allowed_actions: a plan the status does not offer
// for approval cannot be approved anyway.
func TestControlApproveNeedsReviewablePlan(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(outside, []byte("same text"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, code string
		tune       func(h *harness)
	}{
		{"invalid UTF-8", control.CodeApprovalUnavailable, func(h *harness) { awaitingPlan(h, "plan \xff") }},
		{"file differs from snapshot", control.CodeApprovalUnavailable, func(h *harness) {
			awaitingPlan(h, "on disk")
			h.approval.snaps["plan"] = controlSnapWithPlan("rev-1", "engine copy")
		}},
		{"missing plan", control.CodeApprovalUnavailable, func(h *harness) {
			awaitingPlan(h, "gone")
			if err := os.Remove(filepath.Join(h.dir, "plan", "plan.md")); err != nil {
				t.Fatal(err)
			}
		}},
		{"oversize plan", control.CodeApprovalUnavailable, func(h *harness) { awaitingPlan(h, strings.Repeat("x", maxTextFile+1)) }},
		{"symlinked plan", control.CodeApprovalUnavailable, func(h *harness) {
			awaitingPlan(h, "same text")
			path := filepath.Join(h.dir, "plan", "plan.md")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, path); err != nil {
				t.Fatal(err)
			}
		}},
		{"snapshot of another stage", control.CodeApprovalUnavailable, func(h *harness) {
			awaitingPlan(h, "plan")
			h.approval.snaps["plan"] = control.ApprovalSnapshot{RunID: "run-1", StageID: "other", Revision: "rev-1", PlanMarkdown: "plan"}
		}},
		{"snapshot of another run", control.CodeApprovalRunChanged, func(h *harness) {
			awaitingPlan(h, "plan")
			h.approval.snaps["plan"] = control.ApprovalSnapshot{RunID: "run-0", StageID: "plan", Revision: "rev-1", PlanMarkdown: "plan"}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, "approve,stages")
			tc.tune(h)
			shown := actionsOf(t, h.svc.buildStatus(context.Background()), actionApproval)
			if len(shown) != 1 || slices.Contains(shown[0].AllowedActions, toolActionApprove) {
				t.Fatalf("status offers approve: %+v", shown)
			}
			if got := errCode(t, approveRevision(h, "rev-1")); got != tc.code {
				t.Fatalf("code = %s, want %s", got, tc.code)
			}
			if n := h.approval.approveCount(); n != 0 {
				t.Fatalf("ApproveExpected called %d times", n)
			}
		})
	}
}

// A valid current snapshot B never authorizes the revision A the caller sent.
func TestControlApproveRequiresTheCurrentRevision(t *testing.T) {
	h := newHarness(t, "approve")
	awaitingPlan(h, "plan B")
	if got := errCode(t, approveRevision(h, "rev-0")); got != control.CodeApprovalStale {
		t.Fatalf("code = %s", got)
	}
	if n := h.approval.approveCount(); n != 0 {
		t.Fatalf("ApproveExpected called %d times", n)
	}
}

// After the preflight the engine still decides atomically, with the caller's revision and not the latest one.
func TestControlApproveKeepsEngineFence(t *testing.T) {
	h := newHarness(t, "approve")
	awaitingPlan(h, "plan A")
	h.approval.onApprove = func() {
		h.writeFile("plan/plan.md", "plan B")
		h.approval.snaps["plan"] = controlSnapWithPlan("rev-2", "plan B")
	}
	h.approval.approveErr = &engine.Error{Kind: engine.KindConflict, Code: control.CodeApprovalStale, Message: "plan changed"}

	if got := errCode(t, approveRevision(h, "rev-1")); got != control.CodeApprovalStale {
		t.Fatalf("code = %s", got)
	}
	if req := h.approval.approved; len(req) != 1 || req[0].ExpectedRevision != "rev-1" {
		t.Fatalf("ApproveExpected got %+v, want the caller's revision once", req)
	}
}

// Two clients of one revision and a retry after a lost answer: one approve applies, the rest are stale.
func TestControlApproveTwiceIsStale(t *testing.T) {
	h := newHarness(t, "approve")
	awaitingPlan(h, "plan")
	h.approval.onApprove = func() {
		h.run.setStatus("plan", state.StatusRunning)
		h.approval.snapErr = &engine.Error{Kind: engine.KindInvalid, Message: "stage is not awaiting approval"}
	}
	if res := approveRevision(h, "rev-1"); res.IsError {
		t.Fatalf("first approve: %+v", res.StructuredContent)
	}
	for _, client := range []string{"second client", "retry"} {
		if got := errCode(t, approveRevision(h, "rev-1")); got != control.CodeApprovalStale {
			t.Fatalf("%s: code = %s", client, got)
		}
	}
	if n := h.approval.approveCount(); n != 1 {
		t.Fatalf("ApproveExpected called %d times", n)
	}
}

func TestControlApproveKeepsEngineErrorClasses(t *testing.T) {
	for name, snapErr := range map[string]error{
		"unknown stage": &engine.Error{Kind: engine.KindNotFound, Message: "stage not found"},
		"run closing":   &engine.Error{Kind: engine.KindClosed, Message: "run is shutting down"},
		"unavailable":   &engine.Error{Kind: engine.KindConflict, Code: control.CodeApprovalUnavailable, Message: "plan.md unreadable"},
		"storage":       &engine.Error{Kind: engine.KindInternal, Message: "journal write failed"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, "approve")
			awaitingPlan(h, "plan")
			h.approval.snapErr = snapErr
			if got, want := errCode(t, approveRevision(h, "rev-1")), toolError(snapErr).Code; got != want {
				t.Fatalf("code = %s, want %s", got, want)
			}
			if n := h.approval.approveCount(); n != 0 {
				t.Fatalf("ApproveExpected called %d times", n)
			}
		})
	}
}

func TestControlApproveNonASCIIAndRedactedPlan(t *testing.T) {
	h := newHarness(t, "approve", func(c *Config) {
		c.RedactSecrets = func(s string) string { return strings.ReplaceAll(s, "SECRET", "[REDACTED]") }
	})
	awaitingPlan(h, "# План ✓ token SECRET")
	page, err := h.read(h.refID(kindPlan, "plan", ""), 0, 0)
	if err != nil || !page.Redacted || page.Revision != "rev-1" || strings.Contains(page.Text, "SECRET") {
		t.Fatalf("plan page = %+v, %v", page, err)
	}
	res := approveRevision(h, page.Revision)
	var got controlResult
	decode(t, res, &got)
	if res.IsError || got.ApprovedRevision != "rev-1" {
		t.Fatalf("approve of the redacted plan = %+v", res.StructuredContent)
	}
}
