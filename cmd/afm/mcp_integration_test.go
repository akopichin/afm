package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCP on the real engine: `afm run --mcp` as a child process with fake agents (no LLM), a go-sdk client over
// HTTP. The flows and agents are those of testdata/core-mode.

const (
	mcpFlows           = "testdata/mcp/flows"
	mcpReadOnlyTools   = "afm_artifact_read,afm_events_read,afm_run_status,afm_run_wait"
	mcpApprovalAllowed = "approve,stages"
)

type mcpAction struct {
	ID             string   `json:"id"`
	Type           string   `json:"type"`
	StageID        string   `json:"stage_id"`
	Scope          string   `json:"scope"`
	Revision       string   `json:"revision"`
	Phase          string   `json:"phase"`
	QuestionID     string   `json:"question_id"`
	Question       string   `json:"question"`
	AllowedActions []string `json:"allowed_actions"`
	Resources      []string `json:"resources"`
}

type mcpStatus struct {
	RunID             string      `json:"run_id"`
	Cursor            string      `json:"cursor"`
	RunStatus         string      `json:"run_status"`
	AttentionRequired bool        `json:"attention_required"`
	PendingActions    []mcpAction `json:"pending_actions"`
	Permissions       []string    `json:"permissions"`
	Stages            []stageInfo `json:"stages"`
	Cost              mcpCost     `json:"cost"`
}

type mcpCost struct {
	HasData bool       `json:"has_data"`
	Tokens  *mcpTokens `json:"tokens"`
	CostUSD *float64   `json:"estimated_cost_usd"`
}

type mcpTokens struct {
	Total uint64 `json:"total"`
}

func (st mcpStatus) action(stageID, kind string) (mcpAction, bool) {
	for _, a := range st.PendingActions {
		if a.StageID == stageID && a.Type == kind {
			return a, true
		}
	}
	return mcpAction{}, false
}

type mcpControlResult struct {
	Applied          string        `json:"applied"`
	ApprovedRevision string        `json:"approved_revision"`
	TransitionSeq    uint64        `json:"transition_seq"`
	State            mcpStageState `json:"state"`
}

type mcpStageState struct {
	StageStatus string `json:"stage_status"`
	RunStatus   string `json:"run_status"`
}

type mcpFailure struct {
	Code string `json:"code"`
}

// mcpFixture is a core-mode fixture started with --mcp.
type mcpFixture struct {
	*modeFixture
	ctx context.Context
}

func newMCPFixture(t *testing.T, mode, flowFile string) *mcpFixture {
	t.Helper()
	return withMCP(t, newModeFixture(t, mode, flowFile, ""))
}

func withMCP(t *testing.T, fx *modeFixture) *mcpFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*loopTestTimeout)
	t.Cleanup(cancel)
	return &mcpFixture{modeFixture: fx, ctx: ctx}
}

// startMCP starts the run with the MCP endpoint and the named permission groups (none: read-only) and waits
// until it serves.
func (fx *mcpFixture) startMCP(allow string, extra ...string) (*afmChild, string) {
	fx.t.Helper()
	if allow == "" {
		allow = "read"
	}
	args := append([]string{"--mcp", "--mcp-allow", allow}, extra...)
	child := fx.start(args...)
	return child, fx.waitReady(child)
}

func (fx *mcpFixture) endpoint() string { return fx.base() + "/mcp" }

// connect opens a client session that is closed with the test.
func (fx *mcpFixture) connect() *sdkmcp.ClientSession {
	fx.t.Helper()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "mcp-integration", Version: "0"}, nil)
	session, err := client.Connect(fx.ctx, &sdkmcp.StreamableClientTransport{Endpoint: fx.endpoint()}, nil)
	if err != nil {
		fx.t.Fatalf("connect %s: %v", fx.endpoint(), err)
	}
	fx.t.Cleanup(func() { _ = session.Close() })
	return session
}

// tryCall calls a tool and returns the transport error, if any; a tool failure is a result with IsError.
func tryCall(ctx context.Context, session *sdkmcp.ClientSession, name string, args map[string]any) (*sdkmcp.CallToolResult, error) {
	return session.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: args})
}

func (fx *mcpFixture) call(session *sdkmcp.ClientSession, name string, args map[string]any) *sdkmcp.CallToolResult {
	fx.t.Helper()
	res, err := tryCall(fx.ctx, session, name, args)
	if err != nil {
		fx.t.Fatalf("call %s: %v", name, err)
	}
	return res
}

func decodeMCP(t *testing.T, res *sdkmcp.CallToolResult, v any) {
	t.Helper()
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
}

func (fx *mcpFixture) status(session *sdkmcp.ClientSession) mcpStatus {
	fx.t.Helper()
	res := fx.call(session, "afm_run_status", nil)
	if res.IsError {
		fx.t.Fatalf("afm_run_status failed: %v", res.StructuredContent)
	}
	var st mcpStatus
	decodeMCP(fx.t, res, &st)
	return st
}

// waitAction polls afm_run_status until the stage has a pending action of the given type.
func (fx *mcpFixture) waitAction(session *sdkmcp.ClientSession, stageID, kind string) mcpAction {
	fx.t.Helper()
	var action mcpAction
	waitFor(fx.t, kind+" action of stage "+stageID, func() bool {
		var ok bool
		action, ok = fx.status(session).action(stageID, kind)
		return ok
	})
	return action
}

// control calls a control tool that must apply.
func (fx *mcpFixture) control(session *sdkmcp.ClientSession, name string, args map[string]any) mcpControlResult {
	fx.t.Helper()
	res := fx.call(session, name, args)
	if res.IsError {
		fx.t.Fatalf("%s failed: %v", name, res.StructuredContent)
	}
	var out mcpControlResult
	decodeMCP(fx.t, res, &out)
	return out
}

// failureCode calls a control tool that must be refused and returns the code of the refusal.
func (fx *mcpFixture) failureCode(session *sdkmcp.ClientSession, name string, args map[string]any) string {
	fx.t.Helper()
	res := fx.call(session, name, args)
	if !res.IsError {
		fx.t.Fatalf("%s applied, want a refusal: %v", name, res.StructuredContent)
	}
	var f mcpFailure
	decodeMCP(fx.t, res, &f)
	return f.Code
}

func (fx *mcpFixture) approveArgs(runID, stageID, revision string) map[string]any {
	return map[string]any{"run_id": runID, "stage_id": stageID, "decision": "approve", "expected_revision": revision}
}

func (fx *mcpFixture) implementations(stageID string) int {
	n := 0
	for _, r := range fx.agentRuns() {
		if r == stageID+":implementation" {
			n++
		}
	}
	return n
}

func mcpToolList(ctx context.Context, t *testing.T, session *sdkmcp.ClientSession) []string {
	t.Helper()
	var names []string
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			t.Fatalf("list tools: %v", err)
		}
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
}

// The same read-only endpoint in both modes, next to the API (and the UI in dashboard mode) on one port.
func TestMCPIntegrationServesBesideAPIInBothModes(t *testing.T) {
	for _, mode := range bothModes {
		t.Run(mode, func(t *testing.T) {
			fx := newMCPFixture(t, mode, "manual.yaml")
			_, runID := fx.startMCP("")
			session := fx.connect()

			if got := strings.Join(mcpToolList(fx.ctx, t, session), ","); got != mcpReadOnlyTools {
				t.Errorf("tools = %s, want %s", got, mcpReadOnlyTools)
			}
			st := fx.status(session)
			if st.RunID != runID || len(st.Permissions) != 0 {
				t.Errorf("status run_id %q permissions %v, want run %s and no write groups", st.RunID, st.Permissions, runID)
			}
			waitFor(t, "the approval of build", func() bool {
				_, ok := fx.status(session).action("build", "approval")
				return ok
			})
			if got, _ := fx.status(session).action("build", "approval"); len(got.AllowedActions) != 0 {
				t.Errorf("approval allows %v without --mcp-allow, want none", got.AllowedActions)
			}
			if st := fx.status(session).Stages; len(st) != 1 || st[0].Status != "awaiting_approval" {
				t.Errorf("stages = %+v, want build awaiting approval", st)
			}
			if got, want := fx.status(session).RunStatus, fx.modeFixture.status().RunStatus; got != want || got != "running" {
				t.Errorf("run_status = %q, API says %q, want %q while the plan awaits approval", got, want, "running")
			}

			_, _, header := fx.get(http.MethodGet, "/")
			isUI := strings.Contains(header.Get("Content-Type"), "html")
			if isUI != (mode == modeDashboard) {
				t.Errorf("GET / content type %q in %s mode", header.Get("Content-Type"), mode)
			}
			if code, _, _ := fx.get(http.MethodGet, "/api/status"); code != http.StatusOK {
				t.Errorf("GET /api/status = %d beside MCP, want 200", code)
			}
		})
	}
}

// get is a request without a body against the fixture's HTTP server.
func (fx *mcpFixture) get(method, path string) (int, string, http.Header) {
	fx.t.Helper()
	return fx.modeFixture.call(method, path, "", nil)
}

// Two clients approve the same revision at once: one applies, the other is told the revision is stale, and the
// implementation starts once. A repeated approve of the applied revision is stale too.
func TestMCPIntegrationConcurrentApproveAppliesOnce(t *testing.T) {
	fx := newMCPFixture(t, modeCore, "manual.yaml")
	_, runID := fx.startMCP(mcpApprovalAllowed)
	a, b := fx.connect(), fx.connect()
	action := fx.waitAction(a, "build", "approval")
	if action.Revision == "" || !slices.Contains(action.AllowedActions, "approve") {
		t.Fatalf("approval action = %+v, want a revision and approve allowed", action)
	}

	results := make([]*sdkmcp.CallToolResult, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, session := range []*sdkmcp.ClientSession{a, b} {
		wg.Go(func() {
			results[i], errs[i] = tryCall(fx.ctx, session, "afm_stage_decide", fx.approveArgs(runID, "build", action.Revision))
		})
	}
	wg.Wait()

	applied, stale := 0, 0
	for i, res := range results {
		if errs[i] != nil {
			t.Fatalf("client %d: %v", i, errs[i])
		}
		if !res.IsError {
			var out mcpControlResult
			decodeMCP(t, res, &out)
			if out.Applied != "approve" || out.ApprovedRevision != action.Revision || out.TransitionSeq == 0 {
				t.Errorf("applied result = %+v, want approve of %s with a transition seq", out, action.Revision)
			}
			applied++
			continue
		}
		var f mcpFailure
		decodeMCP(t, res, &f)
		if f.Code != "approval_stale" {
			t.Errorf("refusal code = %q, want approval_stale", f.Code)
		}
		stale++
	}
	if applied != 1 || stale != 1 {
		t.Fatalf("applied %d, stale %d, want exactly one of each", applied, stale)
	}

	fx.waitRunStatus("finished")
	if got := fx.implementations("build"); got != 1 {
		t.Errorf("implementation started %d times, want 1", got)
	}
	if code := fx.failureCode(a, "afm_stage_decide", fx.approveArgs(runID, "build", action.Revision)); code != "approval_stale" {
		t.Errorf("repeated approve code = %q, want approval_stale", code)
	}
	if got := fx.implementations("build"); got != 1 {
		t.Errorf("implementation started %d times after the repeat, want 1", got)
	}
}

// A revise makes a new revision: approving the old one is stale, the new one applies. The plan resource serves
// the snapshot the revision names.
func TestMCPIntegrationReviseInvalidatesEarlierRevision(t *testing.T) {
	fx := newMCPFixture(t, modeCore, "manual.yaml")
	_, runID := fx.startMCP(mcpApprovalAllowed)
	session := fx.connect()
	first := fx.waitAction(session, "build", "approval")

	revised := fx.control(session, "afm_stage_decide", map[string]any{
		"run_id": runID, "stage_id": "build", "decision": "revise", "feedback": "add more tests",
	})
	if revised.Applied != "revise" {
		t.Fatalf("revise result = %+v", revised)
	}
	var second mcpAction
	waitFor(t, "a new approval revision", func() bool {
		var ok bool
		second, ok = fx.status(session).action("build", "approval")
		return ok && second.Revision != first.Revision
	})

	if code := fx.failureCode(session, "afm_stage_decide", fx.approveArgs(runID, "build", first.Revision)); code != "approval_stale" {
		t.Errorf("approve of the old revision = %q, want approval_stale", code)
	}
	if got := fx.implementations("build"); got != 0 {
		t.Fatalf("implementation started %d times after a stale approve, want 0", got)
	}

	if len(second.Resources) != 1 {
		t.Fatalf("approval resources = %v, want the plan", second.Resources)
	}
	plan, err := session.ReadResource(fx.ctx, &sdkmcp.ReadResourceParams{URI: second.Resources[0]})
	if err != nil || len(plan.Contents) != 1 || !strings.Contains(plan.Contents[0].Text, "## Tasks") {
		t.Errorf("plan resource = %+v (%v), want the plan text", plan, err)
	}

	applied := fx.control(session, "afm_stage_decide", fx.approveArgs(runID, "build", second.Revision))
	if applied.ApprovedRevision != second.Revision {
		t.Errorf("approved revision = %q, want %q", applied.ApprovedRevision, second.Revision)
	}
	fx.waitRunStatus("finished")
	if got := fx.implementations("build"); got != 1 {
		t.Errorf("implementation started %d times, want 1", got)
	}
}

// The plan changes on disk after the client read it: the revision it holds is stale, the fresh one applies.
func TestMCPIntegrationPlanEditedAfterReadIsStale(t *testing.T) {
	fx := newMCPFixture(t, modeCore, "manual.yaml")
	_, runID := fx.startMCP(mcpApprovalAllowed)
	session := fx.connect()
	read := fx.waitAction(session, "build", "approval")

	planPath := filepath.Join(fx.runDir(runID), "build", "plan.md")
	raw, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, planPath, string(raw)+"\n- [ ] added by a person\n")

	if code := fx.failureCode(session, "afm_stage_decide", fx.approveArgs(runID, "build", read.Revision)); code != "approval_stale" {
		t.Fatalf("approve of the read revision = %q, want approval_stale", code)
	}
	if got := fx.implementations("build"); got != 0 {
		t.Fatalf("implementation started %d times after a stale approve, want 0", got)
	}
	fresh := fx.waitAction(session, "build", "approval")
	if fresh.Revision == read.Revision {
		t.Fatalf("revision did not change after the plan was edited: %s", fresh.Revision)
	}
	fx.control(session, "afm_stage_decide", fx.approveArgs(runID, "build", fresh.Revision))
	fx.waitRunStatus("finished")
}

// Questions of two stages are two pending actions; answering one leaves the other, and an answer is not
// applied twice.
func TestMCPIntegrationQuestionsInTwoStages(t *testing.T) {
	fx := newMCPFixture(t, modeCore, "question.yaml")
	flow := strings.ReplaceAll(mustRead(t, filepath.Join(mcpFlows, "two-questions.yaml")), "__ASK_AGENT__", testdataAbs(t, "agents/ask_agent.sh"))
	writeFile(t, fx.flowPath, flow)
	fx.useAfmBinary()
	_, runID := fx.startMCP("reply")
	session := fx.connect()

	var alpha, beta mcpAction
	waitFor(t, "questions in both stages", func() bool {
		st := fx.status(session)
		var okA, okB bool
		alpha, okA = st.action("alpha", "question")
		beta, okB = st.action("beta", "question")
		return okA && okB
	})
	for _, a := range []mcpAction{alpha, beta} {
		if a.Question != "which database?" || !slices.Contains(a.AllowedActions, "reply") || a.QuestionID == "" {
			t.Fatalf("question action = %+v", a)
		}
	}
	if st := fx.status(session); !st.AttentionRequired || len(st.PendingActions) != 2 {
		t.Fatalf("pending actions = %+v, want the two questions", st.PendingActions)
	}

	args := map[string]any{
		"run_id": runID, "stage_id": "alpha", "phase": alpha.Phase, "question_id": alpha.QuestionID,
		"answer": "sqlite", "from_options": true,
	}
	if out := fx.control(session, "afm_interaction_reply", args); out.Applied != "reply" {
		t.Fatalf("reply result = %+v", out)
	}
	if code := fx.failureCode(session, "afm_interaction_reply", args); code != "question_not_pending" {
		t.Errorf("second reply code = %q, want question_not_pending", code)
	}
	fx.waitStage("alpha", "awaiting_approval")
	if _, ok := fx.status(session).action("beta", "question"); !ok {
		t.Error("the question of beta vanished after alpha was answered")
	}
	if s, _ := fx.modeFixture.status().stage("beta"); s.Status != "awaiting_user_input" {
		t.Errorf("beta = %q, want awaiting_user_input", s.Status)
	}
}

// A held budget scope: adjust raises the limit but does not release the work, resume does. A stale revision is
// a conflict.
func TestMCPIntegrationBudgetAdjustThenResume(t *testing.T) {
	fx := newBudgetFixture(t, "budget.yaml", budgetScript, 3)
	mfx := withMCP(t, fx.modeFixture)
	_, runID := mfx.startMCP("budget")
	session := mfx.connect()

	hold := mfx.waitAction(session, "build", "budget_hold")
	if hold.Scope != budgetStage || hold.Revision != "0" || !slices.Contains(hold.AllowedActions, "budget_adjust") {
		t.Fatalf("budget hold = %+v", hold)
	}
	scope := map[string]any{"run_id": runID, "scope": hold.Scope}
	adjust := func(revision int64) map[string]any {
		return map[string]any{"run_id": runID, "scope": hold.Scope, "expected_revision": revision, "tokens": 1000}
	}

	if out := mfx.control(session, "afm_budget_adjust", adjust(0)); out.Applied != "budget_adjust" {
		t.Fatalf("adjust result = %+v", out)
	}
	if code := mfx.failureCode(session, "afm_budget_adjust", adjust(0)); code == "" {
		t.Error("adjust on a stale revision applied")
	}
	view := fx.view(budgetStage).View
	if view.Hold == nil || view.Revision != 1 || view.Override.Spec.Tokens != 1000 {
		t.Fatalf("view after adjust = %+v, want the new limit and the hold still on", view)
	}
	if got := fx.agentRuns(); len(got) != 2 {
		t.Fatalf("agent ran again after adjust alone: %v", got)
	}

	scope["expected_revision"] = view.Revision
	if out := mfx.control(session, "afm_budget_resume", scope); out.Applied != "budget_resume" {
		t.Fatalf("resume result = %+v", out)
	}
	fx.waitRunStatus("finished")
	if got := fx.agentRuns(); len(got) != 3 {
		t.Errorf("agent invocations = %v, want 3 in the one run", got)
	}
	if st := mfx.status(session); st.RunID != runID || st.RunStatus != "finished" {
		t.Errorf("status after resume = %+v", st)
	}
}

// Tokens are reported and money is not while accounting.show_money is off.
func TestMCPIntegrationCostWithoutMoney(t *testing.T) {
	fx := newMCPFixture(t, modeCore, "auto.yaml")
	fx.env = append(fx.env, "AFM_ACCOUNTING_SHOW_MONEY=0")
	fx.startMCP("")
	session := fx.connect()
	fx.waitRunStatus("finished")

	var st mcpStatus
	waitFor(t, "token totals in the cost", func() bool {
		st = fx.status(session)
		return st.Cost.Tokens != nil
	})
	if st.RunStatus != "finished" {
		t.Errorf("run_status after completion = %q, want finished", st.RunStatus)
	}
	if st.Cost.Tokens.Total == 0 || st.Cost.CostUSD != nil {
		t.Errorf("cost = %+v, want tokens and no estimated_cost_usd", st.Cost)
	}
	res, err := session.ReadResource(fx.ctx, &sdkmcp.ReadResourceParams{URI: "afm://runs/" + st.RunID + "/cost"})
	if err != nil || len(res.Contents) != 1 || strings.Contains(res.Contents[0].Text, "$") {
		t.Errorf("cost resource = %+v (%v)", res, err)
	}
}

// A new repeat cycle is a new run: the old session is gone, and the old run_id is refused by a control call
// without reaching the engine.
func TestMCPIntegrationRepeatCycleChangesRun(t *testing.T) {
	fx := newMCPFixture(t, modeCore, "repeat.yaml")
	fx.gateImplementation()
	child, first := fx.startMCP("stages")
	old := fx.connect()
	oldSessionID := old.ID()
	if got := fx.status(old).RunID; got != first {
		t.Fatalf("status run_id = %q, want %q", got, first)
	}

	fx.release("build")
	var second string
	waitFor(t, "the second cycle", func() bool {
		resp, err := http.Get(fx.base() + "/api/runtime") //nolint:gosec // loopback address of the test child
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		var rt struct {
			RunID string `json:"run_id"`
			Ready bool   `json:"ready"`
		}
		if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&rt) != nil || !rt.Ready || rt.RunID == first {
			return false
		}
		second = rt.RunID
		return true
	})
	child.waitOutput(t, holdingFinalState)

	if _, err := tryCall(fx.ctx, old, "afm_run_status", nil); err == nil {
		t.Error("the session of the first cycle still answers")
	}
	// The SDK may fail locally; a raw request with the old session id shows what the new server answers.
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	if code, _, _ := fx.modeFixture.call(http.MethodPost, "/mcp", body, map[string]string{"Mcp-Session-Id": oldSessionID, "Accept": "application/json, text/event-stream"}); code != http.StatusNotFound {
		t.Errorf("old session id on the new server = HTTP %d, want 404", code)
	}
	fresh := fx.connect()
	if got := fx.status(fresh).RunID; got != second {
		t.Fatalf("new session sees run %q, want %q", got, second)
	}
	before := len(readEvents(t, fx.runDir(second)))
	if code := fx.failureCode(fresh, "afm_stage_pause", map[string]any{"run_id": first, "stage_id": "build"}); code != "run_changed" {
		t.Errorf("control with the old run_id = %q, want run_changed", code)
	}
	if after := len(readEvents(t, fx.runDir(second))); after != before {
		t.Errorf("a refused control call changed the journal: %d -> %d events", before, after)
	}
}

// Closing a client and abandoning its wait changes nothing for the agent.
func TestMCPIntegrationClientCloseKeepsAgentRunning(t *testing.T) {
	fx := newMCPFixture(t, modeCore, "auto.yaml")
	fx.gateImplementation()
	fx.startMCP("")
	session := fx.connect()
	waitFor(t, "the implementation agent to start", func() bool { return fx.implementations("build") == 1 })

	st := fx.status(session)
	waited := make(chan error, 1)
	waitCtx, cancel := goJoined(fx.ctx, t, func(ctx context.Context) {
		_, err := callAdmittedWait(ctx, runWaitCall(session, st, 60), retryPause)
		waited <- err
	})
	fx.waitParkedWait(waitCtx, session, st)
	cancel()
	if err := <-waited; err == nil {
		t.Error("the cancelled wait returned no error")
	}
	_ = session.Close()

	if s, _ := fx.modeFixture.status().stage("build"); s.Status != "running" {
		t.Fatalf("build = %q after the client left, want running", s.Status)
	}
	fx.release("build")
	fx.waitRunStatus("finished")
	if got := fx.implementations("build"); got != 1 {
		t.Errorf("implementation started %d times, want 1", got)
	}
}

// Ctrl+C with a wait and an SSE stream open ends the process through the clean path.
func TestMCPIntegrationShutdownWithActiveWait(t *testing.T) {
	fx := newMCPFixture(t, modeCore, "auto.yaml")
	fx.gateImplementation()
	child, runID := fx.startMCP("")
	session := fx.connect()
	waitFor(t, "the implementation agent to start", func() bool { return fx.implementations("build") == 1 })

	st := fx.status(session)
	waited := make(chan *sdkmcp.CallToolResult, 1)
	goJoined(fx.ctx, t, func(ctx context.Context) {
		res, err := callAdmittedWait(ctx, runWaitCall(session, st, 60), retryPause)
		if err != nil {
			res = nil
		}
		waited <- res
	})
	fx.waitParkedWait(fx.ctx, session, st)

	if code := child.interrupt(t); code != interruptedExitCode {
		t.Fatalf("exit code = %d, want %d\n%s", code, interruptedExitCode, child.out.String())
	}
	select {
	case res := <-waited:
		// The close races the transport teardown: the wait ends with a transport error or the mcp_closed failure.
		if res != nil {
			var f mcpFailure
			if !res.IsError {
				t.Error("the parked wait returned a normal result, want a transport error or mcp_closed")
			} else if decodeMCP(t, res, &f); f.Code != "mcp_closed" {
				t.Errorf("the parked wait failed with %q, want mcp_closed", f.Code)
			}
		}
	case <-time.After(loopTestTimeout):
		t.Error("the wait did not end with the server")
	}
	if out := child.out.String(); strings.Contains(out, "invalid argument") || strings.Contains(out, "write events.jsonl") {
		t.Errorf("shutdown wrote to a closed journal:\n%s", out)
	}
	if len(readEvents(t, fx.runDir(runID))) == 0 {
		t.Error("the journal of the interrupted run is empty")
	}
}

// goJoined runs fn on a goroutine with its own context, which cleanup cancels and then joins, so the goroutine
// ends on every path, including an early failure of the test.
func goJoined(parent context.Context, t *testing.T, fn func(ctx context.Context)) (context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(loopTestTimeout):
			t.Error("a test goroutine did not end after its cancellation")
		}
	})
	return ctx, cancel
}

// mcpWaitSlots mirrors the server's limit of simultaneous afm_run_wait calls.
const mcpWaitSlots = 8

const codeWaitCapacity = "wait_capacity"

// waitParkedWait returns once the server holds the caller's wait: with the other slots filled, only that wait
// makes a further one fail with wait_capacity. The caller's wait must use callAdmittedWait, as the fillers and
// the probe may hold every slot when it first arrives.
func (fx *mcpFixture) waitParkedWait(ctx context.Context, session *sdkmcp.ClientSession, st mcpStatus) {
	fx.t.Helper()
	fillCtx, stopFill := context.WithCancel(ctx)
	var fillers sync.WaitGroup
	fx.t.Cleanup(func() {
		stopFill()
		fillers.Wait()
	})
	for range mcpWaitSlots - 1 {
		fillers.Go(func() { _, _ = callAdmittedWait(fillCtx, runWaitCall(session, st, 60), retryPause) })
	}
	probe := runWaitCall(session, st, 1)
	waitFor(fx.t, "every wait slot to be held on the server", func() bool {
		res, err := probe(fillCtx)
		return err == nil && failureCode(res) == codeWaitCapacity
	})
}

// runWaitCall is one afm_run_wait of the given length on the status st.
func runWaitCall(session *sdkmcp.ClientSession, st mcpStatus, timeoutSeconds int) func(context.Context) (*sdkmcp.CallToolResult, error) {
	return func(ctx context.Context) (*sdkmcp.CallToolResult, error) {
		return tryCall(ctx, session, "afm_run_wait", map[string]any{"run_id": st.RunID, "cursor": st.Cursor, "timeout_seconds": timeoutSeconds})
	}
}

// callAdmittedWait repeats call while the server refuses it for wait capacity and returns the first other
// outcome unchanged: a transport error, any other failure or a result. pause runs between attempts.
func callAdmittedWait(ctx context.Context, call func(context.Context) (*sdkmcp.CallToolResult, error), pause func(context.Context) error) (*sdkmcp.CallToolResult, error) {
	for {
		res, err := call(ctx)
		if err != nil || failureCode(res) != codeWaitCapacity {
			return res, err
		}
		if err := pause(ctx); err != nil {
			return nil, err
		}
	}
}

// retryPause waits briefly before the next attempt, or returns the context's error once it ends.
func retryPause(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(20 * time.Millisecond):
		return nil
	}
}

// failureCode is the code of a failed tool result, empty for a success.
func failureCode(res *sdkmcp.CallToolResult) string {
	if res == nil || !res.IsError {
		return ""
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		return ""
	}
	var f mcpFailure
	_ = json.Unmarshal(raw, &f)
	return f.Code
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func toolFailure(code string) *sdkmcp.CallToolResult {
	return &sdkmcp.CallToolResult{IsError: true, StructuredContent: map[string]any{"code": code}}
}

// The integration targets retry only a capacity refusal; every other outcome reaches the caller's assertions
// unchanged. Attempts and pauses are scripted, so no ordering depends on time.
func TestCallAdmittedWaitRetriesOnlyCapacity(t *testing.T) {
	type outcome struct {
		res *sdkmcp.CallToolResult
		err error
	}
	refused := outcome{res: toolFailure(codeWaitCapacity)}
	parked := outcome{res: &sdkmcp.CallToolResult{StructuredContent: map[string]any{"changed": true}}}
	transport := outcome{err: errors.New("connection reset")}
	closed := outcome{res: toolFailure("mcp_closed")}
	changed := outcome{res: toolFailure("run_changed")}
	cases := map[string]struct {
		script []outcome
		want   outcome
	}{
		"target first, admitted at once":    {[]outcome{parked}, parked},
		"target after the slots filled":     {[]outcome{refused, parked}, parked},
		"several refusals in a row":         {[]outcome{refused, refused, refused, parked}, parked},
		"transport failure":                 {[]outcome{transport}, transport},
		"transport failure after a refusal": {[]outcome{refused, transport}, transport},
		"server closing":                    {[]outcome{refused, closed}, closed},
		"other failure":                     {[]outcome{changed}, changed},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			calls, pauses := 0, 0
			res, err := callAdmittedWait(t.Context(),
				func(context.Context) (*sdkmcp.CallToolResult, error) {
					next := tc.script[calls]
					calls++
					return next.res, next.err
				},
				func(context.Context) error { pauses++; return nil })
			if res != tc.want.res || !errors.Is(err, tc.want.err) {
				t.Fatalf("outcome = %+v, %v; want %+v, %v", res, err, tc.want.res, tc.want.err)
			}
			if calls != len(tc.script) || pauses != calls-1 {
				t.Fatalf("calls %d, pauses %d for a script of %d", calls, pauses, len(tc.script))
			}
		})
	}
}

func TestCallAdmittedWaitEndsWhenCancelledWhileRetrying(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	paused := make(chan struct{})
	calls := 0
	done := make(chan error, 1)
	go func() {
		_, err := callAdmittedWait(ctx,
			func(context.Context) (*sdkmcp.CallToolResult, error) {
				calls++
				return toolFailure(codeWaitCapacity), nil
			},
			func(ctx context.Context) error {
				close(paused)
				<-ctx.Done()
				return ctx.Err()
			})
		done <- err
	}()
	<-paused
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cancellation", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}
