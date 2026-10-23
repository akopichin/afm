package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

const loopOtherStageID = "docs"

var (
	loopUser = state.Actor{Kind: "user"}
	// loopOtherActor is a token-authenticated agent that is not part of the loop under test.
	loopOtherActor = state.Actor{Kind: "agent", ID: "stage:" + loopOtherStageID}
)

// loopFixture is a run with two active stages, a run agent ("My QA") and a running stage helper.
type loopFixture struct {
	o      *Orchestrator
	store  *state.Store
	agent  agentcomm.ParticipantID
	helper agentcomm.ParticipantID
}

func newLoopFixture(t *testing.T) loopFixture {
	t.Helper()
	o, store := newAssistantsTestOrchestrator(t, true, flow.Stage{ID: loopOtherStageID, Command: "claude"})
	activateStage(t, o, assistantsTestStageID)
	activateStage(t, o, loopOtherStageID)
	stubPassingHelper(o)
	msg := "start"
	added, err := o.AddRunAgent(context.Background(), AddRunAgentRequest{
		OperationID: "op-loop-qa", ProfileID: "qa", Title: "My QA", Instruction: "review", FirstMessage: &msg, Actor: loopUser,
	})
	if err != nil {
		t.Fatalf("AddRunAgent: %v", err)
	}
	waitForRunAgentTurn(t, store, added.TaskID, 10*time.Second)
	helperTask := newRunningHelperForMesh(t, o, store, "op-loop-helper")
	return loopFixture{o: o, store: store, agent: added.ParticipantID, helper: meshHelperParticipantID(helperTask)}
}

func loopReq(opID, target string, every time.Duration, prompt string) CreateLoopRequest {
	return CreateLoopRequest{OperationID: opID, Target: agentcomm.ParticipantID(target), Interval: every, Prompt: prompt, Actor: loopUser}
}

func (f loopFixture) create(t *testing.T, req CreateLoopRequest) LoopView {
	t.Helper()
	view, err := f.o.CreateLoop(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateLoop(%s): %v", req.OperationID, err)
	}
	return view
}

func (f loopFixture) loops() map[string]*state.LoopState { return f.store.Snapshot().Assistants.Loops }

func TestLoop_UserCreatesForRunAgent(t *testing.T) {
	f := newLoopFixture(t)
	req := loopReq("op-loop-1", "my-qa", 5*time.Minute, "check the logs")
	req.OriginStage = assistantsTestStageID

	view := f.create(t, req)

	if view.ID != "L1" || view.CreatorID != meshUserParticipantID || view.TargetID != f.agent {
		t.Fatalf("view = %+v, want L1 from the user to the run agent %s (alias resolved to its id)", view, f.agent)
	}
	if view.Every != "5m" || view.OriginStage != assistantsTestStageID || view.UntilStage != "" || view.Status != LoopActive || view.EndReason != "" {
		t.Fatalf("view = %+v, want every 5m, origin %s, flow-wide, active", view, assistantsTestStageID)
	}
	stored := f.loops()
	if len(stored) != 1 {
		t.Fatalf("loops = %d, want 1", len(stored))
	}
	l, ok := f.store.Snapshot().Assistants.LoopByShortID("L1")
	if !ok || l.TargetID != f.agent || l.Prompt != "check the logs" || l.ThreadID == "" {
		t.Fatalf("stored loop = %+v, want the resolved target, the prompt and its own thread", l)
	}
	// The first tick is one interval after the durable creation; slots are fixed to that moment.
	if view.NextTickAt == nil || !view.NextTickAt.Equal(l.CreatedAt.Add(5*time.Minute)) {
		t.Fatalf("next tick = %v, want creation %v + 5m", view.NextTickAt, l.CreatedAt)
	}
}

func TestLoop_UserCreatesForHelper(t *testing.T) {
	f := newLoopFixture(t)
	view := f.create(t, loopReq("op-loop-h", string(f.helper), time.Minute, "status?"))
	if view.TargetID != f.helper || view.Status != LoopActive {
		t.Fatalf("view = %+v, want an active loop targeting helper %s", view, f.helper)
	}
}

func TestLoop_AgentWithoutTargetLoopsItself(t *testing.T) {
	f := newLoopFixture(t)

	run := f.create(t, CreateLoopRequest{OperationID: "op-self-run", Interval: time.Second, Prompt: "tick", Actor: state.Actor{Kind: "agent", ID: string(f.agent)}})
	if run.CreatorID != f.agent || run.TargetID != f.agent || run.OriginStage != "" {
		t.Fatalf("run agent self-loop = %+v, want creator == target and no Feed origin", run)
	}
	// A helper's loop is shown in the helper's stage; the origin comes from the participant, never from the request.
	helper := f.create(t, CreateLoopRequest{
		OperationID: "op-self-helper", Interval: time.Second, Prompt: "tick",
		OriginStage: loopOtherStageID, Actor: state.Actor{Kind: "agent", ID: string(f.helper)},
	})
	if helper.CreatorID != f.helper || helper.TargetID != f.helper || helper.OriginStage != assistantsTestStageID {
		t.Fatalf("helper self-loop = %+v, want origin %s derived from the helper", helper, assistantsTestStageID)
	}
}

func TestLoop_AgentCanTargetAnotherAgent(t *testing.T) {
	f := newLoopFixture(t)
	req := loopReq("op-loop-other", "@my-qa", time.Minute, "ping")
	req.Actor = state.Actor{Kind: "agent", ID: string(f.helper)}
	view := f.create(t, req)
	if view.CreatorID != f.helper || view.TargetID != f.agent {
		t.Fatalf("view = %+v, want a loop from the helper to the run agent", view)
	}
}

// Stage main agents may create loops for eligible colleagues but cannot target themselves.
func TestLoop_PromptCanSelfLoopMatchesWhoMayBeATarget(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true, flow.Stage{ID: loopOtherStageID, Command: "claude"})
	o.opts.DashboardURL = "http://127.0.0.1:1"
	stage := o.agentCommBlockFor(flow.Stage{ID: assistantsTestStageID})
	if strings.Contains(stage, "it comes to YOU") {
		t.Errorf("a stage's own agent cannot be a loop target, so its prompt must not offer a bare self-loop:\n%s", stage)
	}
	helper := o.agentCommBlockForTask("missing-task")
	if !strings.Contains(helper, "it comes to YOU") {
		t.Errorf("a helper/run agent IS a valid loop target, so its prompt must offer a bare self-loop:\n%s", helper)
	}
}

// Creation must fail when collaboration is disabled, rather than leave an active loop without a scheduler.
func TestLoop_RejectedWhenAssistantsDisabled(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, false, flow.Stage{ID: loopOtherStageID, Command: "claude"})
	_, err := o.CreateLoop(context.Background(), loopReq("op-disabled", "my-qa", time.Minute, "ping"))
	var rej *state.OperationRejectedError
	if !errors.As(err, &rej) || rej.Code != assistants.CodeFeatureDisabled {
		t.Fatalf("err = %v (%T), want *OperationRejectedError{Code: feature_disabled}", err, err)
	}
}

func TestLoop_UserNeedsOneExplicitTarget(t *testing.T) {
	f := newLoopFixture(t)
	_, err := f.o.CreateLoop(context.Background(), loopReq("op-no-target", "", time.Minute, "ping"))
	requireRejected(t, err, assistants.CodeInvalidRequest)
	if len(f.loops()) != 0 {
		t.Fatalf("a rejected create left %d loops behind", len(f.loops()))
	}
}

func TestLoop_RejectsIneligibleTargets(t *testing.T) {
	f := newLoopFixture(t)
	for _, tc := range []struct {
		name   string
		target string
		code   assistants.ErrorCode
	}{
		{"stage agent", "stage:" + assistantsTestStageID, assistants.CodeInvalidRequest},
		{"the human", "user", assistants.CodeInvalidRequest},
		{"unknown name", "nobody", assistants.CodeNotFound},
	} {
		_, err := f.o.CreateLoop(context.Background(), loopReq("op-bad-"+tc.name, tc.target, time.Minute, "ping"))
		if err == nil {
			t.Errorf("%s: accepted", tc.name)
			continue
		}
		requireRejected(t, err, tc.code)
	}
	// An agent that has no run-agent/helper identity of its own cannot loop itself either.
	_, err := f.o.CreateLoop(context.Background(), CreateLoopRequest{
		OperationID: "op-stage-self", Interval: time.Minute, Prompt: "ping", Actor: state.Actor{Kind: "agent", ID: "stage:" + assistantsTestStageID},
	})
	requireRejected(t, err, assistants.CodeInvalidRequest)
	if len(f.loops()) != 0 {
		t.Fatalf("rejected creates left %d loops behind", len(f.loops()))
	}
}

func TestLoop_RejectsAnEndedStageBound(t *testing.T) {
	f := newLoopFixture(t)
	finishStage(t, f.o, loopOtherStageID)
	_, err := f.o.CreateLoop(context.Background(), CreateLoopRequest{
		OperationID: "op-until-done", Target: f.agent, Interval: time.Minute, Prompt: "ping", UntilStageID: loopOtherStageID, Actor: loopUser,
	})
	requireRejected(t, err, assistants.CodeInvalidStatus)
}

func TestLoop_RejectsBadInput(t *testing.T) {
	f := newLoopFixture(t)
	huge := strings.Repeat("x", agentcomm.MaxLoopPromptBytes+1)
	for _, tc := range []struct {
		name string
		edit func(*CreateLoopRequest)
		code assistants.ErrorCode
	}{
		{"zero interval", func(r *CreateLoopRequest) { r.Interval = 0 }, assistants.CodeInvalidRequest},
		{"negative interval", func(r *CreateLoopRequest) { r.Interval = -time.Minute }, assistants.CodeInvalidRequest},
		{"interval below one second", func(r *CreateLoopRequest) { r.Interval = 999 * time.Millisecond }, assistants.CodeInvalidRequest},
		{"empty prompt", func(r *CreateLoopRequest) { r.Prompt = "" }, assistants.CodeInvalidRequest},
		{"blank prompt", func(r *CreateLoopRequest) { r.Prompt = " \n\t" }, assistants.CodeInvalidRequest},
		{"oversized prompt", func(r *CreateLoopRequest) { r.Prompt = huge }, assistants.CodeInvalidRequest},
		{"unknown origin stage", func(r *CreateLoopRequest) { r.OriginStage = "nope" }, assistants.CodeInvalidRequest},
		{"unknown until stage", func(r *CreateLoopRequest) { r.UntilStageID = "nope" }, assistants.CodeInvalidRequest},
		{"reserved operation id", func(r *CreateLoopRequest) { r.OperationID = "team/qa" }, assistants.CodeInvalidRequest},
		{"system actor", func(r *CreateLoopRequest) { r.Actor = state.Actor{Kind: "system"} }, assistants.CodeActorNotAllowed},
		{"agent not in the run", func(r *CreateLoopRequest) { r.Actor = state.Actor{Kind: "agent", ID: "run:ghost"} }, assistants.CodeNotFound},
	} {
		req := loopReq("op-input", "my-qa", time.Minute, "ping")
		tc.edit(&req)
		_, err := f.o.CreateLoop(context.Background(), req)
		if err == nil {
			t.Errorf("%s: accepted", tc.name)
			continue
		}
		var rejected *state.OperationRejectedError
		if !errors.As(err, &rejected) || rejected.Code != tc.code {
			t.Errorf("%s: err = %v, want a %s rejection", tc.name, err, tc.code)
		}
	}
	if _, err := f.o.CreateLoop(context.Background(), loopReq("", "my-qa", time.Minute, "ping")); err == nil {
		t.Error("a create without a request id was accepted")
	}
	if len(f.loops()) != 0 {
		t.Fatalf("rejected creates left %d loops behind", len(f.loops()))
	}
}

func TestLoop_ExplicitStageBound(t *testing.T) {
	f := newLoopFixture(t)
	req := loopReq("op-bound", "my-qa", time.Minute, "ping")
	req.UntilStageID = loopOtherStageID
	view := f.create(t, req)
	if view.UntilStage != loopOtherStageID || view.Status != LoopActive {
		t.Fatalf("view = %+v, want active and bound to %s", view, loopOtherStageID)
	}

	finishStage(t, f.o, loopOtherStageID)
	page, err := f.o.ListLoops(context.Background(), ListLoopsRequest{Actor: loopUser})
	if err != nil {
		t.Fatal(err)
	}
	got := page.Loops[0]
	if got.Status != LoopEnded || !strings.Contains(got.EndReason, loopOtherStageID) || got.NextTickAt != nil {
		t.Fatalf("after the stage ended: %+v, want ended with the stage in its reason and no next tick", got)
	}
}

func TestLoop_PromptIsRedacted(t *testing.T) {
	f := newLoopFixture(t)
	f.o.opts.SecretValues = []string{"s3cr3t-value"}
	f.create(t, loopReq("op-secret", "my-qa", time.Minute, "use s3cr3t-value to log in"))
	l, _ := f.store.Snapshot().Assistants.LoopByShortID("L1")
	if strings.Contains(l.Prompt, "s3cr3t-value") || l.Prompt == "" {
		t.Fatalf("stored prompt = %q, want the secret redacted", l.Prompt)
	}
}

func TestLoop_CreateIsIdempotent(t *testing.T) {
	f := newLoopFixture(t)
	req := loopReq("op-retry", "my-qa", time.Minute, "ping")
	req.OriginStage = assistantsTestStageID
	first := f.create(t, req)
	again := f.create(t, req)
	if first.ID != again.ID || len(f.loops()) != 1 {
		t.Fatalf("retry: first %+v, again %+v, loops %d; want the same loop, once", first, again, len(f.loops()))
	}
	// A retry names the target by its id just as well: it is the same loop.
	byID := req
	byID.Target = f.agent
	if got := f.create(t, byID); got.ID != first.ID {
		t.Fatalf("retry by id = %+v, want %s", got, first.ID)
	}

	changed := req
	changed.Prompt = "something else"
	_, err := f.o.CreateLoop(context.Background(), changed)
	if !errors.Is(err, state.ErrOperationIDConflict) {
		t.Fatalf("same id, other prompt: err = %v, want an operation id conflict", err)
	}
	other := req
	other.Actor = loopOtherActor
	_, err = f.o.CreateLoop(context.Background(), other)
	if !errors.Is(err, state.ErrOperationIDConflict) {
		t.Fatalf("same id, other actor: err = %v, want an operation id conflict", err)
	}
	if len(f.loops()) != 1 {
		t.Fatalf("loops = %d, want 1", len(f.loops()))
	}
}

func TestLoop_AtMostSixteenActive(t *testing.T) {
	f := newLoopFixture(t)
	for i := range agentcomm.MaxActiveLoops {
		f.create(t, loopReq(fmt.Sprintf("op-many-%d", i), "my-qa", time.Minute, "ping"))
	}
	_, err := f.o.CreateLoop(context.Background(), loopReq("op-one-too-many", "my-qa", time.Minute, "ping"))
	requireRejected(t, err, assistants.CodeBudgetExceeded)
	if len(f.loops()) != agentcomm.MaxActiveLoops {
		t.Fatalf("loops = %d, want %d", len(f.loops()), agentcomm.MaxActiveLoops)
	}

	// A stopped loop frees its slot; an ended one does too.
	if _, err := f.o.StopLoop(context.Background(), StopLoopRequest{OperationID: "op-free", LoopID: "L1", Actor: loopUser}); err != nil {
		t.Fatalf("StopLoop: %v", err)
	}
	f.create(t, loopReq("op-after-stop", "my-qa", time.Minute, "ping"))
	_, err = f.o.CreateLoop(context.Background(), loopReq("op-full-again", "my-qa", time.Minute, "ping"))
	requireRejected(t, err, assistants.CodeBudgetExceeded)
}

func TestLoop_EndedLoopsFreeTheirSlots(t *testing.T) {
	f := newLoopFixture(t)
	for i := range agentcomm.MaxActiveLoops {
		req := loopReq(fmt.Sprintf("op-bound-%d", i), "my-qa", time.Minute, "ping")
		req.UntilStageID = loopOtherStageID
		f.create(t, req)
	}
	finishStage(t, f.o, loopOtherStageID)
	f.create(t, loopReq("op-fresh", "my-qa", time.Minute, "ping"))
}

func TestLoop_StopOwnership(t *testing.T) {
	f := newLoopFixture(t)
	ctx := context.Background()
	agentActor := state.Actor{Kind: "agent", ID: string(f.helper)}
	byUser := f.create(t, loopReq("op-own-1", "my-qa", time.Minute, "ping"))
	byAgent := f.create(t, CreateLoopRequest{OperationID: "op-own-2", Interval: time.Minute, Prompt: "ping", Actor: agentActor})

	_, err := f.o.StopLoop(ctx, StopLoopRequest{OperationID: "op-stop-x", LoopID: byAgent.ID, Actor: loopOtherActor})
	requireRejected(t, err, assistants.CodeActorNotAllowed)
	_, err = f.o.StopLoop(ctx, StopLoopRequest{OperationID: "op-stop-y", LoopID: byUser.ID, Actor: agentActor})
	requireRejected(t, err, assistants.CodeActorNotAllowed)
	_, err = f.o.StopLoop(ctx, StopLoopRequest{OperationID: "op-stop-z", LoopID: "L99", Actor: loopUser})
	requireRejected(t, err, assistants.CodeNotFound)
	// This same text reaches a human in the Feed too (the browser and agent adapters share one Service error):
	// it must read naturally for either audience, not say "ask the human" to a human reading it themselves.
	if strings.Contains(err.Error(), "ask the human") {
		t.Errorf("not-found error must not address an agent-only audience: %v", err)
	}
	_, err = f.o.StopLoop(ctx, StopLoopRequest{OperationID: "op-stop-s", LoopID: byUser.ID, Actor: state.Actor{Kind: "system"}})
	requireRejected(t, err, assistants.CodeActorNotAllowed)
	for _, l := range f.loops() {
		if l.Stopped() {
			t.Fatalf("a rejected stop stopped %s", l.ShortID)
		}
	}

	// The creator stops its own loop; the human stops anybody's, the id typed in lower case.
	stopped, err := f.o.StopLoop(ctx, StopLoopRequest{OperationID: "op-stop-own", LoopID: byAgent.ID, Actor: agentActor})
	if err != nil || stopped.Status != LoopStopped || stopped.NextTickAt != nil || stopped.EndReason == "" {
		t.Fatalf("creator stop: %+v, %v; want stopped with a reason and no next tick", stopped, err)
	}
	if _, err := f.o.StopLoop(ctx, StopLoopRequest{OperationID: "op-stop-human", LoopID: strings.ToLower(byUser.ID), Actor: loopUser}); err != nil {
		t.Fatalf("human stop: %v", err)
	}
	l, _ := f.store.Snapshot().Assistants.LoopByShortID(byUser.ID)
	if !l.Stopped() || l.StoppedBy != loopUser {
		t.Fatalf("stored loop = %+v, want stopped by the user", l)
	}
	_, err = f.o.StopLoop(ctx, StopLoopRequest{OperationID: "op-stop-again", LoopID: byUser.ID, Actor: loopUser})
	requireRejected(t, err, assistants.CodeInvalidStatus)
}

func TestLoop_StopIsIdempotent(t *testing.T) {
	f := newLoopFixture(t)
	ctx := context.Background()
	l1 := f.create(t, loopReq("op-s1", "my-qa", time.Minute, "ping"))
	l2 := f.create(t, loopReq("op-s2", "my-qa", time.Minute, "ping"))

	first, err := f.o.StopLoop(ctx, StopLoopRequest{OperationID: "op-stop", LoopID: l1.ID, Actor: loopUser})
	if err != nil {
		t.Fatalf("StopLoop: %v", err)
	}
	again, err := f.o.StopLoop(ctx, StopLoopRequest{OperationID: "op-stop", LoopID: l1.ID, Actor: loopUser})
	if err != nil || again.ID != first.ID || again.Status != LoopStopped {
		t.Fatalf("retry = %+v, %v; want the same stopped loop", again, err)
	}
	_, err = f.o.StopLoop(ctx, StopLoopRequest{OperationID: "op-stop", LoopID: l2.ID, Actor: loopUser})
	if !errors.Is(err, state.ErrOperationIDConflict) {
		t.Fatalf("same request id for another loop: err = %v, want an operation id conflict", err)
	}
	if l, _ := f.store.Snapshot().Assistants.LoopByShortID(l2.ID); l.Stopped() {
		t.Fatal("the conflicting stop stopped the other loop")
	}
}

func TestLoop_ListScopes(t *testing.T) {
	f := newLoopFixture(t)
	ctx := context.Background()
	agentActor := state.Actor{Kind: "agent", ID: string(f.helper)}
	f.create(t, loopReq("op-l1", "my-qa", time.Minute, "ping"))
	f.create(t, CreateLoopRequest{OperationID: "op-l2", Interval: time.Minute, Prompt: "ping", Actor: agentActor})
	f.create(t, loopReq("op-l3", string(f.helper), time.Minute, "ping"))
	if _, err := f.o.StopLoop(ctx, StopLoopRequest{OperationID: "op-stop-l3", LoopID: "L3", Actor: loopUser}); err != nil {
		t.Fatal(err)
	}

	ids := func(actor state.Actor) []string {
		t.Helper()
		page, err := f.o.ListLoops(ctx, ListLoopsRequest{Actor: actor})
		if err != nil {
			t.Fatalf("ListLoops(%v): %v", actor, err)
		}
		var out []string
		for _, l := range page.Loops {
			out = append(out, l.ID+":"+string(l.Status))
		}
		return out
	}
	if got := strings.Join(ids(loopUser), ","); got != "L1:active,L2:active,L3:stopped" {
		t.Fatalf("human list = %s, want every loop in creation order, stopped ones included", got)
	}
	if got := strings.Join(ids(agentActor), ","); got != "L2:active" {
		t.Fatalf("agent list = %s, want only its own loop", got)
	}
	if got := ids(loopOtherActor); len(got) != 0 {
		t.Fatalf("a stranger sees %v", got)
	}
	if _, err := f.o.ListLoops(ctx, ListLoopsRequest{Actor: state.Actor{Kind: "system"}}); err == nil {
		t.Fatal("a system actor listed loops")
	}
}

// What the list says about a loop: who made it, where it goes, how often, when it fires next.
func TestLoop_ListReportsEachLoop(t *testing.T) {
	f := newLoopFixture(t)
	req := loopReq("op-view", "my-qa", 90*time.Second, "ping")
	req.OriginStage = assistantsTestStageID
	req.UntilStageID = assistantsTestStageID
	f.create(t, req)

	page, err := f.o.ListLoops(context.Background(), ListLoopsRequest{Actor: loopUser})
	if err != nil || len(page.Loops) != 1 {
		t.Fatalf("ListLoops = %+v, %v", page, err)
	}
	l := page.Loops[0]
	stored, _ := f.store.Snapshot().Assistants.LoopByShortID("L1")
	if l.ID != "L1" || l.CreatorID != meshUserParticipantID || l.TargetID != f.agent || l.Every != "1m30s" ||
		l.OriginStage != assistantsTestStageID || l.UntilStage != assistantsTestStageID || l.Status != LoopActive {
		t.Fatalf("view = %+v", l)
	}
	// Nothing was delivered, and the clock is real: the next slot is the first one still in the future.
	if l.NextTickAt == nil || !l.NextTickAt.After(time.Now()) || l.NextTickAt.Before(stored.CreatedAt) {
		t.Fatalf("next tick = %v, want a future slot", l.NextTickAt)
	}
}

func TestLoop_EndReasons(t *testing.T) {
	f := newLoopFixture(t)
	ctx := context.Background()
	f.create(t, loopReq("op-e1", "my-qa", time.Minute, "ping"))
	f.create(t, loopReq("op-e2", string(f.helper), time.Minute, "ping"))

	statusOf := func(id string) LoopView {
		t.Helper()
		page, err := f.o.ListLoops(ctx, ListLoopsRequest{Actor: loopUser})
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range page.Loops {
			if l.ID == id {
				return l
			}
		}
		t.Fatalf("no loop %s", id)
		return LoopView{}
	}

	// The loop's own thread is opened by its first tick; once it is closed the loop is over.
	l, _ := f.store.Snapshot().Assistants.LoopByShortID("L1")
	open, err := threadOpenEffects(f.store.Snapshot(), l.ThreadID, "", agentcomm.ScopeRun, []agentcomm.ParticipantID{meshUserParticipantID, f.agent}, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ApplyRunOperation(state.Operation{ID: "op-open-loop-thread", Kind: opKindMeshOpenThread, Actor: loopUser, Effects: open}, nil); err != nil {
		t.Fatal(err)
	}
	if got := statusOf("L1"); got.Status != LoopActive {
		t.Fatalf("L1 with an open thread = %+v, want active", got)
	}
	if err := f.o.CloseThread(ctx, CloseThreadRequest{OperationID: "op-close-loop-thread", ThreadID: l.ThreadID, Actor: loopUser}); err != nil {
		t.Fatal(err)
	}
	if got := statusOf("L1"); got.Status != LoopEnded || !strings.Contains(got.EndReason, "closed") {
		t.Fatalf("L1 with a closed thread = %+v, want ended: closed", got)
	}

	// A helper's loop ends with the helper's stage.
	if got := statusOf("L2"); got.Status != LoopActive {
		t.Fatalf("L2 = %+v, want active", got)
	}
	finishStage(t, f.o, assistantsTestStageID)
	if got := statusOf("L2"); got.Status != LoopEnded || !strings.Contains(got.EndReason, assistantsTestStageID) {
		t.Fatalf("L2 after its stage ended = %+v, want ended with the stage in the reason", got)
	}
}

func TestLoop_EndsWhenTheFlowIsFinished(t *testing.T) {
	f := newLoopFixture(t)
	f.create(t, loopReq("op-flow", "my-qa", time.Minute, "ping"))
	finishStage(t, f.o, assistantsTestStageID)
	finishStage(t, f.o, loopOtherStageID)
	page, err := f.o.ListLoops(context.Background(), ListLoopsRequest{Actor: loopUser})
	if err != nil {
		t.Fatal(err)
	}
	if l := page.Loops[0]; l.Status != LoopEnded || l.EndReason == "" || l.NextTickAt != nil {
		t.Fatalf("view = %+v, want ended with a reason once every stage is terminal", l)
	}
}

func TestLoop_ThreadRoom(t *testing.T) {
	// A tick and its automatic reply must both fit.
	if !loopThreadHasRoom(agentcomm.MaxThreadMessages - 2) {
		t.Error("two free places: want room")
	}
	if loopThreadHasRoom(agentcomm.MaxThreadMessages - 1) {
		t.Error("one free place: want no room for a tick and its reply")
	}
}

func TestLoop_IntervalLabel(t *testing.T) {
	for in, want := range map[time.Duration]string{
		5 * time.Minute: "5m", time.Hour: "1h", 90 * time.Minute: "1h30m", 10 * time.Second: "10s", 90 * time.Second: "1m30s", time.Second: "1s",
	} {
		if got := formatLoopInterval(in); got != want {
			t.Errorf("formatLoopInterval(%v) = %q, want %q", in, got, want)
		}
	}
}

// A helper's finished task does not end its loop (the next tick runs the same helper again). A failed task
// ends it only reversibly (RetryTask can requeue it); a cancelled one is gone for good.
func TestLoop_HelperTaskEndReason(t *testing.T) {
	taskID := assistants.TaskID("t-helper")
	helper := meshHelperParticipantID(taskID)
	rsWith := func(status assistants.TaskStatus) state.RunState {
		return state.RunState{
			Stages: map[string]state.StageState{assistantsTestStageID: {Status: state.StatusRunning}},
			Assistants: state.AssistantsState{Tasks: map[assistants.TaskID]*state.TaskState{
				taskID: {Task: assistants.Task{ID: taskID, RootStageID: assistantsTestStageID, Status: status, StatusReason: "boom"}},
			}},
		}
	}
	loop := state.LoopState{TargetID: helper}
	type end struct{ ended, final bool }
	for status, want := range map[assistants.TaskStatus]end{
		assistants.StatusRunning: {}, assistants.StatusWatching: {}, assistants.StatusCompleted: {},
		assistants.StatusFailed: {ended: true}, assistants.StatusCancelled: {ended: true, final: true},
	} {
		reason, final := loopEndReason(rsWith(status), loop)
		if got := (end{reason != "", final}); got != want {
			t.Errorf("helper task %s: %+v, want %+v", status, got, want)
		}
	}
}

// A helper from an earlier attempt of its stage is gone for good: the new attempt attaches new tasks, and its
// result is never read from the old one. The same holds for a nested helper after its parent was retried.
func TestLoop_SupersededHelperEndsFinally(t *testing.T) {
	top, nested := assistants.TaskID("t-top"), assistants.TaskID("t-nested")
	rsWith := func(rootGen, parentGen, parentTaskGen uint64) state.RunState {
		parent := top
		return state.RunState{
			Stages: map[string]state.StageState{assistantsTestStageID: {Status: state.StatusRunning}},
			Assistants: state.AssistantsState{
				RootGenerations: map[string]uint64{assistantsTestStageID: 1},
				Tasks: map[assistants.TaskID]*state.TaskState{
					top: {Task: assistants.Task{ID: top, RootStageID: assistantsTestStageID, Status: assistants.StatusCompleted, RootGeneration: rootGen, TaskGeneration: parentTaskGen}},
					nested: {Task: assistants.Task{
						ID: nested, RootStageID: assistantsTestStageID, ParentTaskID: &parent, Status: assistants.StatusCompleted,
						RootGeneration: 1, ParentGeneration: parentGen,
					}},
				},
			},
		}
	}
	for _, tc := range []struct {
		name       string
		rs         state.RunState
		target     assistants.TaskID
		wantEnded  bool
		wantReason string
	}{
		{"top-level, current attempt", rsWith(1, 0, 0), top, false, ""},
		{"top-level, old attempt", rsWith(0, 0, 0), top, true, "superseded"},
		{"nested, current parent attempt", rsWith(1, 2, 2), nested, false, ""},
		{"nested, parent retried", rsWith(1, 1, 2), nested, true, "superseded"},
	} {
		reason, final := loopEndReason(tc.rs, state.LoopState{TargetID: meshHelperParticipantID(tc.target)})
		if (reason != "") != tc.wantEnded || (tc.wantEnded && (!final || !strings.Contains(reason, tc.wantReason))) {
			t.Errorf("%s: reason %q final %v, want ended=%v final with %q", tc.name, reason, final, tc.wantEnded, tc.wantReason)
		}
	}
}

// The Retry of a failed until-stage revives the loop's end, so the end is reversible: it still holds the slot,
// and the human can stop the loop for good before it comes back.
func TestLoop_StopWorksAfterReversibleEnd(t *testing.T) {
	f := newLoopFixture(t)
	req := loopReq("op-rev", "my-qa", time.Minute, "ping")
	req.UntilStageID = loopOtherStageID
	f.create(t, req)
	f.o.Trigger(loopOtherStageID, bus.EvFail, bus.GuardCtx{}, "boom")
	if got := f.listed(t)[0]; got.Status != LoopEnded || got.EndReason == "" || got.NextTickAt != nil {
		t.Fatalf("after the stage failed: %+v, want ended", got)
	}

	stopped, err := f.o.StopLoop(context.Background(), StopLoopRequest{OperationID: "op-rev-stop", LoopID: "L1", Actor: loopUser})
	if err != nil || stopped.Status != LoopStopped {
		t.Fatalf("StopLoop of a reversibly ended loop: %+v, %v; want stopped", stopped, err)
	}
	if _, ok := f.o.Trigger(loopOtherStageID, bus.EvManualRetry, bus.GuardCtx{}, ""); !ok {
		t.Fatal("EvManualRetry failed")
	}
	if got := f.listed(t)[0]; got.Status != LoopStopped || got.NextTickAt != nil {
		t.Fatalf("after the stage was retried: %+v, want the loop to stay stopped", got)
	}
}

// A loop whose until-stage failed may come back with a Retry, so it keeps its slot: the limit cannot be
// dodged by creating the next sixteen while the first ones look ended.
func TestLoop_ReversiblyEndedLoopsKeepTheirSlots(t *testing.T) {
	f := newLoopFixture(t)
	for i := range agentcomm.MaxActiveLoops {
		req := loopReq(fmt.Sprintf("op-rev-%d", i), "my-qa", time.Minute, "ping")
		req.UntilStageID = loopOtherStageID
		f.create(t, req)
	}
	f.o.Trigger(loopOtherStageID, bus.EvFail, bus.GuardCtx{}, "boom")
	_, err := f.o.CreateLoop(context.Background(), loopReq("op-rev-17", "my-qa", time.Minute, "ping"))
	requireRejected(t, err, assistants.CodeBudgetExceeded)

	for i := range agentcomm.MaxActiveLoops {
		id := fmt.Sprintf("L%d", i+1)
		if _, err := f.o.StopLoop(context.Background(), StopLoopRequest{OperationID: "op-rev-stop-" + id, LoopID: id, Actor: loopUser}); err != nil {
			t.Fatalf("StopLoop(%s): %v", id, err)
		}
	}
	f.create(t, loopReq("op-rev-17", "my-qa", time.Minute, "ping"))
}

// A loop must not be born over: the same derivation that ends a loop later refuses its creation.
func TestLoop_RejectsATargetThatIsAlreadyOver(t *testing.T) {
	f := newLoopFixture(t)
	finishStage(t, f.o, assistantsTestStageID) // the helper is still running, but its stage is done
	_, err := f.o.CreateLoop(context.Background(), loopReq("op-born-over", string(f.helper), time.Minute, "ping"))
	requireRejected(t, err, assistants.CodeInvalidStatus)
	if len(f.loops()) != 0 {
		t.Fatalf("a rejected create left %d loops behind", len(f.loops()))
	}
}

func TestLoop_CancelledRunAgentEndsItsLoop(t *testing.T) {
	f := newLoopFixture(t)
	f.create(t, loopReq("op-cancel", "my-qa", time.Minute, "ping"))
	taskID, ok := runTaskForParticipant(f.store.Snapshot(), f.agent)
	if !ok {
		t.Fatal("no task behind the run agent")
	}
	if n := activeLoopCount(f.store.Snapshot()); n != 1 {
		t.Fatalf("active loops = %d, want 1", n)
	}

	if err := f.o.CancelTask(context.Background(), taskID); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	if got := f.listed(t)[0]; got.Status != LoopEnded || got.EndReason == "" || got.NextTickAt != nil {
		t.Fatalf("after the run agent was cancelled: %+v, want ended with a reason and no next tick", got)
	}
	if n := activeLoopCount(f.store.Snapshot()); n != 0 {
		t.Fatalf("active loops = %d, want the slot freed", n)
	}
}

// A one-shot helper is usually completed between ticks. A new attempt of its stage leaves that task behind, so
// the loop on it ends for good and frees its slot instead of waiting on a result nobody reads.
func TestLoop_NewStageAttemptSupersedesTheHelper(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	release := make(chan struct{})
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		awaitRelease(t, release)
		return passResultOutcome(), nil
	}
	taskID := attachTestTask(context.Background(), t, o, "op-sup-helper")
	waitForTaskStatus(t, store, taskID, assistants.StatusRunning, 2*time.Second)
	f := loopFixture{o: o, store: store}
	f.create(t, loopReq("op-sup", string(taskID), time.Minute, "ping"))
	close(release)
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)
	if got := f.listed(t)[0]; got.Status != LoopActive {
		t.Fatalf("with its helper completed: %+v, want the loop active (the next tick reruns it)", got)
	}

	if err := o.BumpRootGeneration(assistantsTestStageID); err != nil {
		t.Fatalf("BumpRootGeneration: %v", err)
	}
	got := f.listed(t)[0]
	if got.Status != LoopEnded || !strings.Contains(got.EndReason, "superseded") || got.NextTickAt != nil {
		t.Fatalf("after a new attempt of the stage: %+v, want ended: superseded", got)
	}
	if n := activeLoopCount(store.Snapshot()); n != 0 {
		t.Fatalf("active loops = %d, want the slot freed", n)
	}
}

func (f loopFixture) listed(t *testing.T) []LoopView {
	t.Helper()
	page, err := f.o.ListLoops(context.Background(), ListLoopsRequest{Actor: loopUser})
	if err != nil {
		t.Fatalf("ListLoops: %v", err)
	}
	return page.Loops
}
