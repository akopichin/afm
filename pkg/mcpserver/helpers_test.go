package mcpserver

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/akopichin/afm/contracts/accounting"
	"github.com/akopichin/afm/contracts/budget"
	"github.com/akopichin/afm/contracts/clock"
	"github.com/akopichin/afm/contracts/control"
	bus "github.com/akopichin/afm/contracts/events"
	"github.com/akopichin/afm/contracts/state"
)

// fakeRun is a run whose state a test edits; Snapshot fails the test once the service was closed.
type fakeRun struct {
	t      *testing.T
	mu     sync.Mutex
	rs     state.RunState
	hist   []state.Transition
	closed atomic.Bool
	reads  atomic.Int32
}

func (f *fakeRun) Snapshot() state.RunState {
	f.reads.Add(1)
	if f.closed.Load() {
		f.t.Error("Reader.Snapshot called after Close returned")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	rs := f.rs
	rs.Stages = make(map[string]state.StageState, len(f.rs.Stages))
	for id, st := range f.rs.Stages {
		rs.Stages[id] = st
	}
	return rs
}

func (f *fakeRun) History() ([]state.Transition, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]state.Transition(nil), f.hist...), nil
}

func (f *fakeRun) setStage(id string, st state.StageState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rs.Stages == nil {
		f.rs.Stages = map[string]state.StageState{}
	}
	if _, ok := f.rs.Stages[id]; !ok {
		f.rs.StageOrder = append(f.rs.StageOrder, id)
	}
	f.rs.Stages[id] = st
}

func (f *fakeRun) setStatus(id string, status state.StageStatus) {
	f.setStage(id, state.StageState{Status: status})
}

// fakeBus is the dashboard bus: a test pushes impulses and raises the drop count by hand.
type fakeBus struct {
	ch      chan bus.Event
	dropped atomic.Uint64
	unsubs  atomic.Int32
}

func newFakeBus() *fakeBus { return &fakeBus{ch: make(chan bus.Event, 64)} }

func (b *fakeBus) Subscribe(int) (uint64, <-chan bus.Event) { return 1, b.ch }
func (b *fakeBus) Unsubscribe(uint64)                       { b.unsubs.Add(1) }
func (b *fakeBus) SubscriberDroppedCount(uint64) uint64     { return b.dropped.Load() }
func (b *fakeBus) impulse()                                 { b.ch <- bus.Event{Type: bus.EventStageStatusChanged} }

// fakeCommands records calls; Approve exists only to fail the test if anything ever reaches the legacy path.
type fakeCommands struct {
	t     *testing.T
	mu    sync.Mutex
	calls []string
	err   error
	// ctxErr is the context error seen by the last Revise/Retry/Pause/Continue.
	ctxErr error
	// after runs before a command returns, like the engine changing the run while it applies the command.
	after func(call string)
}

func (c *fakeCommands) record(call string, err error) error {
	c.mu.Lock()
	c.calls = append(c.calls, call)
	after := c.after
	c.mu.Unlock()
	if after != nil {
		after(call)
	}
	return err
}

// recordCtx also keeps the context error the command was given.
func (c *fakeCommands) recordCtx(ctx context.Context, call string) error {
	c.mu.Lock()
	c.ctxErr = ctx.Err()
	c.mu.Unlock()
	return c.record(call, c.err)
}

func (c *fakeCommands) Revise(ctx context.Context, stageID, feedback string) error {
	return c.recordCtx(ctx, "revise:"+stageID+":"+feedback)
}
func (c *fakeCommands) CheckRevise(stageID string) error { return c.record("check:"+stageID, nil) }
func (c *fakeCommands) Retry(ctx context.Context, stageID string) error {
	return c.recordCtx(ctx, "retry:"+stageID)
}
func (c *fakeCommands) Pause(ctx context.Context, stageID string) error {
	return c.recordCtx(ctx, "pause:"+stageID)
}
func (c *fakeCommands) Continue(ctx context.Context, stageID string) error {
	return c.recordCtx(ctx, "continue:"+stageID)
}
func (c *fakeCommands) Answer(stageID, phase, qID, answer string, fromOptions bool) error {
	return c.record("answer:"+stageID+":"+phase+":"+qID+":"+answer, c.err)
}
func (c *fakeCommands) Approve(context.Context, string) error {
	c.t.Error("legacy Approve was called")
	return nil
}

func (c *fakeCommands) callList() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

type fakeApproval struct {
	mu         sync.Mutex
	snaps      map[string]control.ApprovalSnapshot
	snapErr    error
	approveErr error
	approved   []control.ApproveExpectedRequest
	snapshots  int
	// onApprove runs before ApproveExpected returns, like the engine moving the run while it decides.
	onApprove func()
}

func (a *fakeApproval) ApprovalSnapshot(_ context.Context, stageID string) (control.ApprovalSnapshot, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.snapshots++
	if a.snapErr != nil {
		return control.ApprovalSnapshot{}, a.snapErr
	}
	snap := a.snaps[stageID]
	// Like the engine, a snapshot names its run and stage; a test sets other values to model a mismatch.
	if snap.RunID == "" {
		snap.RunID = "run-1"
	}
	if snap.StageID == "" {
		snap.StageID = stageID
	}
	return snap, nil
}

func (a *fakeApproval) ApproveExpected(_ context.Context, req control.ApproveExpectedRequest) (control.ApproveExpectedResult, error) {
	a.mu.Lock()
	a.approved = append(a.approved, req)
	onApprove := a.onApprove
	a.mu.Unlock()
	if onApprove != nil {
		onApprove()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.approveErr != nil {
		return control.ApproveExpectedResult{}, a.approveErr
	}
	return control.ApproveExpectedResult{RunID: req.RunID, StageID: req.StageID, ApprovedRevision: req.ExpectedRevision, TransitionSeq: 42}, nil
}

func (a *fakeApproval) approveCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.approved)
}

type fakeBudgets struct {
	mu      sync.Mutex
	views   []budget.View
	updates []budget.UpdateRequest
	resumes []budget.ResumeRequest
	lists   int
}

func (b *fakeBudgets) List(context.Context) ([]budget.View, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lists++
	return append([]budget.View(nil), b.views...), nil
}
func (b *fakeBudgets) View(context.Context, budget.ScopeID) (budget.View, error) {
	return budget.View{}, nil
}
func (b *fakeBudgets) Update(_ context.Context, req budget.UpdateRequest) (budget.UpdateResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.updates = append(b.updates, req)
	return budget.UpdateResult{View: budget.View{Scope: req.Scope, Revision: req.ExpectedRevision + 1}}, nil
}
func (b *fakeBudgets) Resume(_ context.Context, req budget.ResumeRequest) (budget.View, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.resumes = append(b.resumes, req)
	return budget.View{Scope: req.Scope, Revision: req.ExpectedRevision + 1}, nil
}

type fakeCost struct{ bundle accounting.CostBundle }

func (c fakeCost) CostSnapshot() accounting.CostBundle { return c.bundle }

// harness is one service wired to fakes and a fake clock.
type harness struct {
	t        *testing.T
	svc      *Service
	run      *fakeRun
	dir      string
	clk      *clock.FakeClock
	bus      *fakeBus
	cmds     *fakeCommands
	approval *fakeApproval
	budgets  *fakeBudgets
}

// newHarness builds a service with every collaborator faked; tune edits the config before New.
func newHarness(t *testing.T, groups string, tune ...func(*Config)) *harness {
	t.Helper()
	g, err := ParseGroups(groups)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{
		t: t, run: &fakeRun{t: t}, dir: t.TempDir(), clk: clock.NewFake(time.Unix(1_700_000_000, 0)),
		bus: newFakeBus(), cmds: &fakeCommands{t: t}, approval: &fakeApproval{snaps: map[string]control.ApprovalSnapshot{}},
		budgets: &fakeBudgets{},
	}
	cfg := Config{
		RunID: "run-1", RunDir: h.dir, Reader: h.run, Events: h.bus, Commands: h.cmds, Approval: h.approval,
		Budgets: h.budgets, Groups: g, Clock: h.clk,
	}
	for _, f := range tune {
		f(&cfg)
	}
	h.svc, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// The wait for Run is the cleanup: handlers must be gone before the reader may be torn down.
	t.Cleanup(func() {
		if err := h.svc.Close(context.Background()); err != nil {
			t.Error(err)
		}
		h.run.closed.Store(true)
	})
	return h
}

// writeFile writes content under the run directory, creating parents.
func (h *harness) writeFile(rel, content string) {
	h.t.Helper()
	path := filepath.Join(h.dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

// ask writes a pending question the way the agent's ask_user does.
func (h *harness) ask(stageID, id, question string) {
	h.writeFile(stageID+"/planning."+id+".question.json", `{"id":"`+id+`","question":"`+question+`","options":["a","b"]}`)
}

// connect opens a real MCP client session over HTTP.
func (h *harness) connect(t *testing.T, opts *sdkmcp.ClientOptions) *sdkmcp.ClientSession {
	t.Helper()
	ts := httptest.NewServer(h.svc)
	t.Cleanup(ts.Close)
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test", Version: "1"}, opts)
	session, err := client.Connect(context.Background(), &sdkmcp.StreamableClientTransport{Endpoint: ts.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// decode reads a tool result's structured body into v, whether it came from the server side or a client.
func decode(t *testing.T, res *sdkmcp.CallToolResult, v any) {
	t.Helper()
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
}

// errCode returns the code of a failed result; it fails the test if the result succeeded.
func errCode(t *testing.T, res *sdkmcp.CallToolResult) string {
	t.Helper()
	if !res.IsError {
		t.Fatalf("expected an error result, got %v", res.StructuredContent)
	}
	var te ToolError
	decode(t, res, &te)
	return te.Code
}

func toolNames(t *testing.T, session *sdkmcp.ClientSession) []string {
	t.Helper()
	var names []string
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tool.Name)
	}
	return names
}

// fakeCostSwitch is a cost provider whose total a test moves.
type fakeCostSwitch struct {
	mu     sync.Mutex
	tokens uint64
}

func (c *fakeCostSwitch) set(tokens uint64) {
	c.mu.Lock()
	c.tokens = tokens
	c.mu.Unlock()
}

func (c *fakeCostSwitch) CostSnapshot() accounting.CostBundle {
	c.mu.Lock()
	defer c.mu.Unlock()
	return accounting.CostBundle{Run: &accounting.CostView{TotalTokens: c.tokens, Metered: 1}, HasData: true, Health: accounting.HealthOK}
}

func (a *fakeApproval) snapshotCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.snapshots
}

func (b *fakeBudgets) listCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lists
}

func controlSnapWithPlan(revision, plan string) control.ApprovalSnapshot {
	return control.ApprovalSnapshot{Revision: control.ApprovalRevision(revision), PlanMarkdown: plan}
}
