package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
	"github.com/akopichin/afm/pkg/state"
)

// liveStopInvocation is the Stop hook capability of the turn that is running now, as the executor of that turn
// would mint it. Call it from inside the agent function of a runWithRetry loop.
func liveStopInvocation(t *testing.T, o *Orchestrator, s flow.Stage) *stopInvocation {
	t.Helper()
	turn, ok := o.liveTurns.Load(s.ID)
	if !ok {
		t.Fatal("no live turn: call this from inside the agent function")
	}
	return o.newStopInvocation(s, turn.(*turnRecord))
}

func stopRequest(id string) executor.StopRequest { return executor.StopRequest{RequestID: id} }

func requireAllow(t *testing.T, got executor.StopDecision) {
	t.Helper()
	if got.Block || got.Reason != "" {
		t.Fatalf("decision = %+v, want the session allowed to stop", got)
	}
}

func (r *summaryRun) requireNothingSpent() {
	t := r.t
	t.Helper()
	if got := r.summary(); got != nil {
		t.Fatalf("summary = %+v, want no correction spent", got)
	}
	if got := r.notices(eventSummaryHookReminder); len(got) != 0 {
		t.Fatalf("reminder notices = %+v, want none", got)
	}
}

func TestStopHook_MissingSummaryBlocksOnceAndTheAuthorFinishesInTheSession(t *testing.T) {
	r := newSummaryRun(t)
	_, live := r.o.ui.Subscribe(16)
	var first, repeat executor.StopDecision
	r.run(func(int) error {
		inv := liveStopInvocation(t, r.o, r.stage)
		first = inv.decide(stopRequest("r1"))
		// The author follows the reminder in the same session, then tries to stop again.
		r.writeSummary()
		repeat = inv.decide(stopRequest("r2"))
		return nil
	}, nil)

	if !first.Block || !strings.Contains(first.Reason, r.summaryPath()) || !strings.Contains(first.Reason, "missing") {
		t.Fatalf("first decision = %+v, want a reminder naming the file and the problem", first)
	}
	requireAllow(t, repeat)
	if r.calls != 1 {
		t.Fatalf("author calls = %d, want the session to finish the work without a post-exit run", r.calls)
	}
	r.requireDone()
	if got := r.summary(); got == nil || got.Hook != 1 || got.PostExit != 0 || got.Reason != "missing" {
		t.Fatalf("summary = %+v, want exactly one durable hook reminder", got)
	}
	if got := r.notices(eventSummaryCorrectionStarted); len(got) != 0 {
		t.Fatalf("post-exit start notices = %+v, want none: the session did the correction", got)
	}
	if got := r.notices(eventSummaryRecovered); len(got) != 1 {
		t.Fatalf("recovered notices = %+v, want one", got)
	}

	reminders := r.notices(eventSummaryHookReminder)
	if len(reminders) != 1 {
		t.Fatalf("reminder notices = %+v, want exactly one", reminders)
	}
	n := reminders[0]
	if n.StageID != "s1" || n.Data["path"] != r.summaryPath() || n.Data["reason"] != "missing" || n.Data["reservation"] == "" {
		t.Errorf("reminder notice = %+v, want stage, path, reason and reservation", n)
	}
	for key := range n.Data {
		switch key {
		case "path", "reason", "reservation", "message":
		default:
			t.Errorf("reminder notice carries %q: only the file, the reason and the reservation may travel", key)
		}
	}
	if !sawNotice(live, eventSummaryHookReminder) {
		t.Error("the reminder was not published live")
	}
}

func sawNotice(live <-chan bus.Event, typ bus.EventType) bool {
	for {
		select {
		case ev := <-live:
			if ev.Type == typ {
				return true
			}
		default:
			return false
		}
	}
}

func TestStopHook_EmptySummaryIsRemindedWithItsReason(t *testing.T) {
	r := newSummaryRun(t)
	if err := os.WriteFile(r.summaryPath(), []byte(" \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var got executor.StopDecision
	r.run(func(int) error {
		got = liveStopInvocation(t, r.o, r.stage).decide(stopRequest("r1"))
		r.writeSummary()
		return nil
	}, nil)

	if !got.Block || !strings.Contains(got.Reason, "empty") {
		t.Fatalf("decision = %+v, want a reminder for an empty file", got)
	}
	if s := r.summary(); s == nil || s.Reason != "empty" {
		t.Fatalf("summary = %+v, want the empty reason recorded", s)
	}
}

func TestStopHook_CompleteSummaryLetsTheSessionStop(t *testing.T) {
	r := newSummaryRun(t)
	r.writeSummary()
	r.run(func(int) error {
		requireAllow(t, liveStopInvocation(t, r.o, r.stage).decide(stopRequest("r1")))
		return nil
	}, nil)
	r.requireNothingSpent()
}

// A Stop that follows another Stop hook's continuation is never blocked again: that is how Claude and Codex
// protect a session from a hook loop.
func TestStopHook_HookActiveLetsTheSessionStop(t *testing.T) {
	r := newSummaryRun(t)
	r.run(func(int) error {
		inv := liveStopInvocation(t, r.o, r.stage)
		requireAllow(t, inv.decide(executor.StopRequest{RequestID: "r1", HookActive: true}))
		r.writeSummary()
		return nil
	}, nil)
	r.requireNothingSpent()
}

// An unreadable summary is AFM's exact file error, not a reminder.
func TestStopHook_UnreadableSummaryLetsTheSessionStop(t *testing.T) {
	r := newSummaryRun(t)
	if err := os.Mkdir(r.summaryPath(), 0o755); err != nil {
		t.Fatal(err)
	}
	r.run(func(int) error {
		requireAllow(t, liveStopInvocation(t, r.o, r.stage).decide(stopRequest("r1")))
		return nil
	}, nil)
	r.requireNothingSpent()
}

// The one reminder is per correction cycle: it survives the transport retry that follows, so a second session of
// the same cycle is not reminded again.
func TestStopHook_ReminderSurvivesATransportRetry(t *testing.T) {
	r := newSummaryRun(t)
	var decisions []executor.StopDecision
	r.run(func(call int) error {
		decisions = append(decisions, liveStopInvocation(t, r.o, r.stage).decide(stopRequest("r")))
		if call == 1 {
			return errors.New("connection reset by peer")
		}
		r.writeSummary()
		return nil
	}, nil)

	if len(decisions) != 2 || !decisions[0].Block {
		t.Fatalf("decisions = %+v, want a reminder in the first session", decisions)
	}
	requireAllow(t, decisions[1])
	if got := r.summary(); got == nil || got.Hook != 1 {
		t.Fatalf("summary = %+v, want the reminder spent once", got)
	}
	if got := r.notices(eventSummaryHookReminder); len(got) != 1 {
		t.Fatalf("reminder notices = %+v, want one", got)
	}
}

// The reminder and the two post-exit corrections are separate budgets: a session that ignores the reminder still
// gets both continuations before the stage fails.
func TestStopHook_ReminderDoesNotSpendThePostExitCorrections(t *testing.T) {
	r := newSummaryRun(t)
	r.run(func(call int) error {
		if call == 1 {
			if got := liveStopInvocation(t, r.o, r.stage).decide(stopRequest("r1")); !got.Block {
				t.Errorf("decision = %+v, want the reminder", got)
			}
		}
		return nil
	}, nil)

	if r.calls != 3 {
		t.Fatalf("author calls = %d, want the run and both post-exit corrections", r.calls)
	}
	r.requireFailed()
	if got := r.summary(); got == nil || got.Hook != 1 || got.PostExit != 2 {
		t.Fatalf("summary = %+v, want one reminder and two corrections", got)
	}
}

func TestStopHook_PausedStageLetsTheSessionStopAndSpendsNothing(t *testing.T) {
	r := newSummaryRun(t)
	r.run(func(int) error {
		inv := liveStopInvocation(t, r.o, r.stage)
		if _, ok := r.o.Trigger("s1", bus.EvPause, bus.GuardCtx{}, "manual pause"); !ok {
			t.Error("EvPause failed")
		}
		requireAllow(t, inv.decide(stopRequest("r1")))
		return executor.ErrUserInterrupted
	}, nil)
	r.requireNothingSpent()
}

// A Pause that lands after the hook policy read the stage but before its reservation commits: the reservation is
// fenced by the turn's epoch, so nothing is spent and nothing is announced.
func TestStopHook_PauseDuringReservationSpendsNothing(t *testing.T) {
	r := newSummaryRun(t)
	r.run(func(int) error {
		inv := liveStopInvocation(t, r.o, r.stage)
		inv.beforeReserve = func() {
			if _, ok := r.o.Trigger("s1", bus.EvPause, bus.GuardCtx{}, "manual pause"); !ok {
				t.Error("EvPause failed")
			}
		}
		requireAllow(t, inv.decide(stopRequest("r1")))
		return executor.ErrUserInterrupted
	}, nil)

	r.requireNothingSpent()
	if got := r.o.opts.Store.Get("s1"); got != state.StatusPaused {
		t.Fatalf("status = %v, want the Pause to stand", got)
	}
}

// The file appears between the probe and the reservation: the session stops and no reminder is announced.
func TestStopHook_MarkerAppearingBeforeTheReservationIsNotAReminder(t *testing.T) {
	r := newSummaryRun(t)
	r.run(func(int) error {
		inv := liveStopInvocation(t, r.o, r.stage)
		inv.beforeReserve = r.writeSummary
		requireAllow(t, inv.decide(stopRequest("r1")))
		return nil
	}, nil)

	if got := r.notices(eventSummaryHookReminder); len(got) != 0 {
		t.Fatalf("reminder notices = %+v, want none: the session was not reminded", got)
	}
	r.requireDone()
}

func TestStopHook_OpenQuestionLetsTheSessionStop(t *testing.T) {
	r := newSummaryRun(t)
	question := filepath.Join(r.dir, "autonomous_execution.q1.question.json")
	if err := os.WriteFile(question, []byte(`{"id":"q1","question":"Which database?"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	r.run(func(int) error {
		requireAllow(t, liveStopInvocation(t, r.o, r.stage).decide(stopRequest("r1")))
		r.writeSummary()
		return nil
	}, nil)
	r.requireNothingSpent()
}

// A turn that yields on a wait — also one answered or cancelled before the process exits — continues through the
// peer path: the summary is neither required nor reminded.
func TestStopHook_YieldLetsTheSessionStop(t *testing.T) {
	f := newExitFixture(t)
	stage := flow.Stage{ID: assistantsTestStageID}
	var got executor.StopDecision
	f.o.runWithRetry(context.Background(), stage, phaseAutonomous, func(string) error {
		_, wait := f.askUser(t, "op-ask")
		if err := f.o.CancelWait(context.Background(), CancelWaitRequest{
			OperationID: "op-cancel", WaitID: wait, Reason: "no longer needed", Actor: state.Actor{Kind: "user"},
		}); err != nil {
			t.Error(err)
		}
		got = liveStopInvocation(t, f.o, stage).decide(stopRequest("r1"))
		return nil
	}, func() error { return nil }, func() { t.Error("onUserInterrupted should not be called") })

	requireAllow(t, got)
	if s := f.store.Snapshot().Stages[assistantsTestStageID].Summary; s != nil {
		t.Fatalf("summary = %+v, want no reminder spent on a yield", s)
	}
}

func TestStopHook_RevokedOrFinishedInvocationLetsTheSessionStop(t *testing.T) {
	r := newSummaryRun(t)
	var late *stopInvocation
	r.run(func(int) error {
		inv := liveStopInvocation(t, r.o, r.stage)
		inv.revoke()
		requireAllow(t, inv.decide(stopRequest("revoked")))
		late = liveStopInvocation(t, r.o, r.stage)
		r.writeSummary()
		return nil
	}, nil)

	// The turn is over: its capability belongs to a turn that is no longer live.
	requireAllow(t, late.decide(stopRequest("late")))
	r.requireNothingSpent()
}

// A Stop of an invocation whose turn lost the stage to a Pause → Continue is stale, though the stage runs again.
func TestStopHook_StaleEpochLetsTheSessionStop(t *testing.T) {
	r := newSummaryRun(t)
	r.run(func(int) error {
		inv := liveStopInvocation(t, r.o, r.stage)
		if _, ok := r.o.Trigger("s1", bus.EvPause, bus.GuardCtx{}, "manual pause"); !ok {
			t.Fatal("EvPause failed")
		}
		if _, ok := r.o.Trigger("s1", bus.EvContinue, bus.GuardCtx{PausedFrom: state.StatusRunning}, ""); !ok {
			t.Fatal("EvContinue failed")
		}
		requireAllow(t, inv.decide(stopRequest("r1")))
		return executor.ErrUserInterrupted
	}, nil)
	r.requireNothingSpent()
}

// The reservation is a self-transition of the running stage. It must not look like a status change to anyone:
// no stage_status_changed (live or critical), no wait resolved, and the completion that follows happens once.
func TestStopHook_ReservationIsInvisibleToStatusWatchers(t *testing.T) {
	f := newExitFixture(t)
	stage := flow.Stage{ID: assistantsTestStageID}
	dir := filepath.Join(f.o.opts.RunDir, assistantsTestStageID)
	if _, err := f.o.Wait(context.Background(), WaitRequest{
		OperationID: "op-watch", Waiter: meshUserParticipantID, Actor: state.Actor{Kind: "user"},
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnStageStatus, StageID: assistantsTestStageID, Status: string(state.StatusDone)},
	}); err != nil {
		t.Fatal(err)
	}
	_, live := f.o.ui.Subscribe(16)
	critical := f.o.critical.Recv()
	drain(critical) // the stage's activation is already queued there
	epochBefore := f.store.Snapshot().Stages[assistantsTestStageID].Epoch

	var (
		got            executor.StopDecision
		liveEvents     []bus.Event
		criticalEvents []bus.Event
		waitsAfter     map[agentcomm.WaitID]*state.MeshWait
	)
	f.o.runWithRetry(context.Background(), stage, phaseAutonomous, func(string) error {
		got = liveStopInvocation(t, f.o, stage).decide(stopRequest("r1"))
		// Everything a transition publishes or resolves has happened by the time it returns.
		liveEvents, criticalEvents = drain(live), drain(critical)
		waitsAfter = f.store.Snapshot().Assistants.Mesh.Waits
		if err := os.WriteFile(filepath.Join(dir, stagefiles.SummaryFile), []byte("Done.\n"), 0o644); err != nil {
			t.Error(err)
		}
		return nil
	}, func() error { return stagefiles.CheckAutonomousCompletion(dir) }, func() { t.Error("onUserInterrupted should not be called") })

	if !got.Block {
		t.Fatalf("decision = %+v, want the reminder", got)
	}
	for _, ev := range append(liveEvents, criticalEvents...) {
		if ev.Type == bus.EventStageStatusChanged && ev.Data == string(state.StatusRunning) {
			t.Errorf("a running→running status change was published: %+v", ev)
		}
	}
	if st := f.store.Snapshot().Stages[assistantsTestStageID]; st.Epoch != epochBefore {
		t.Errorf("epoch %d → %d: the reservation must not start a new turn", epochBefore, st.Epoch)
	}
	if len(waitsAfter) == 0 {
		t.Fatal("the watcher's wait vanished")
	}
	for id, w := range waitsAfter {
		if w.Status != agentcomm.WaitPending {
			t.Errorf("wait %s is %s after the reservation, want it still pending", id, w.Status)
		}
	}
	completions := 0
	for _, tr := range historyOf(t, f.store) {
		if tr.Event == string(bus.EvComplete) {
			completions++
		}
	}
	if completions > 1 {
		t.Errorf("complete transitions = %d, want at most one", completions)
	}
}

func drain(ch <-chan bus.Event) []bus.Event {
	var out []bus.Event
	for {
		select {
		case ev := <-ch:
			out = append(out, ev)
		default:
			return out
		}
	}
}

func historyOf(t *testing.T, store *state.Store) []state.Transition {
	t.Helper()
	h, err := store.History()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// Only the main autonomous author gets the capability: it is bound to the live turn it is taken from.
func TestStopHook_OnlyTheAutonomousAuthorOfALiveTurnGetsTheCapability(t *testing.T) {
	r := newSummaryRun(t)
	if r.o.stopHookFor(r.stage, phaseAutonomous, "claude") != nil {
		t.Fatal("a capability without a live turn")
	}
	r.run(func(int) error {
		for _, phase := range []string{phaseImplementation, phaseReview, phasePlanning} {
			if r.o.stopHookFor(r.stage, phase, "claude") != nil {
				t.Errorf("phase %q got a Stop hook capability", phase)
			}
		}
		client := r.o.stopHookFor(r.stage, phaseAutonomous, "claude")
		if client == nil {
			t.Fatal("the autonomous author got no capability")
		}
		decide, revoke := client.Mint()
		if got := decide(stopRequest("r1")); !got.Block {
			t.Errorf("minted decision = %+v, want the reminder", got)
		}
		revoke()
		requireAllow(t, decide(stopRequest("r2")))
		r.writeSummary()
		return nil
	}, nil)
}

// The capability needs neither the dashboard nor the mesh, and an injected test runner never gets one.
func TestStopHook_ExecutorConfigCarriesItWithoutDashboardOrMesh(t *testing.T) {
	r := newSummaryRun(t)
	if r.o.opts.DashboardURL != "" {
		t.Fatal("this test needs the dashboard off")
	}
	r.run(func(int) error {
		auth := r.o.baseStageExecutorConfig(r.stage, phaseAutonomous, "claude", nil)
		if auth.StopHook == nil {
			t.Error("the autonomous author's executor has no Stop hook capability")
		}
		if auth.MeshClient != nil {
			t.Error("the mesh must stay off: the Stop hook does not depend on it")
		}
		if impl := r.o.baseStageExecutorConfig(r.stage, phaseImplementation, "claude", nil); impl.StopHook != nil {
			t.Error("an implementation author got a Stop hook capability")
		}
		r.writeSummary()
		return nil
	}, nil)

	r.o.opts.Runner = &fakeRunnerForHook{}
	r.o.runner = r.o.opts.Runner
	if got := r.o.runnerFor(r.stage, phaseAutonomous); got != r.o.runner {
		t.Errorf("runnerFor = %T, want the injected runner: it must not become an executor with a hook", got)
	}
}

type fakeRunnerForHook struct{ executor.Runner }

// Only the CLIs that take the hook get a capability, each with its own runtime; OpenAI, Cursor and unknown
// commands run without a bridge. The hook command is the running afm binary, by absolute path.
func TestStopHook_CapabilityIsGatedByAdapter(t *testing.T) {
	r := newSummaryRun(t)
	r.o.opts.Config.Docker.Agents = map[string]config.AgentRecipe{
		"glm":     {Type: config.ClaudeCommand},
		"plain":   {},
		"gpt":     {Type: config.RecipeTypeCodex},
		"oai":     {Type: config.RecipeTypeOpenAI},
		"oai-agt": {Type: config.RecipeTypeOpenAIAgent},
		"cursor":  {Type: config.RecipeTypeCursor},
	}
	want := map[string]executor.StopHookRuntime{
		"claude": executor.StopHookClaude, "glm": executor.StopHookClaude, "plain": executor.StopHookClaude,
		"codex-as-claude": executor.StopHookCodex, "gpt": executor.StopHookCodex,
		"oai": "", "oai-agt": "", "cursor": "", "my-own-cli": "",
	}
	r.run(func(int) error {
		for cmd, runtime := range want {
			client := r.o.stopHookFor(r.stage, phaseAutonomous, cmd)
			switch {
			case runtime == "" && client != nil:
				t.Errorf("command %q got a Stop hook capability", cmd)
			case runtime != "" && client == nil:
				t.Errorf("command %q got no Stop hook capability", cmd)
			case runtime != "":
				if client.Runtime != runtime {
					t.Errorf("command %q runtime = %q, want %q", cmd, client.Runtime, runtime)
				}
				if !filepath.IsAbs(client.Launcher) || client.Launcher != browserLauncherPath() {
					t.Errorf("command %q launcher = %q, want the running afm binary", cmd, client.Launcher)
				}
			default:
				// No capability wanted and none granted.
			}
		}
		r.writeSummary()
		return nil
	}, nil)
}

// A hook that cannot work is one durable notice per stage and reason, however many callbacks, retries or fresh
// capabilities report it. The payload is a fixed vocabulary: no token, no CLI output, no environment.
func TestStopHook_UnavailableIsOneDurableNoticePerReason(t *testing.T) {
	r := newSummaryRun(t)
	_, live := r.o.ui.Subscribe(16)
	r.run(func(int) error {
		for range 3 {
			client := r.o.stopHookFor(r.stage, phaseAutonomous, "claude")
			client.OnUnavailable(executor.StopHookNotAsked)
		}
		r.o.stopHookFor(r.stage, phaseAutonomous, "claude").OnUnavailable(executor.StopHookSettingsConflict)
		r.writeSummary()
		return nil
	}, nil)

	notices := r.notices(eventSummaryHookUnavailable)
	if len(notices) != 2 {
		t.Fatalf("got %d notices, want one per reason: %+v", len(notices), notices)
	}
	reasons := map[string]bool{}
	for _, n := range notices {
		reasons[n.Data["reason"]] = true
		if n.StageID != "s1" || n.Data["runtime"] != "claude" || n.Data["message"] == "" || len(n.Data) != 3 {
			t.Errorf("notice = %+v, want stage s1 and exactly reason, runtime, message", n)
		}
	}
	if !reasons["not_asked"] || !reasons["settings_conflict"] {
		t.Errorf("reasons = %v", reasons)
	}
	live2 := 0
	for _, ev := range drain(live) {
		if ev.Type == eventSummaryHookUnavailable {
			live2++
		}
	}
	if live2 != 2 {
		t.Errorf("published %d live notices, want 2", live2)
	}
}

// Every reason has its own text (the not-asked one also per runtime), and the Codex one names what makes Codex
// skip the hook.
func TestStopHook_UnavailableMessagesDifferByReason(t *testing.T) {
	seen := map[string]bool{}
	for _, tc := range []struct {
		runtime executor.StopHookRuntime
		reason  executor.StopHookUnavailable
	}{
		{executor.StopHookClaude, executor.StopHookSettingsConflict},
		{executor.StopHookClaude, executor.StopHookBridgeFailed},
		{executor.StopHookClaude, executor.StopHookNotAsked},
		{executor.StopHookCodex, executor.StopHookNotAsked},
	} {
		msg := summaryHookUnavailableMessage(tc.runtime, tc.reason)
		if msg == "" || seen[msg] {
			t.Errorf("message for %s/%s = %q is empty or repeats another", tc.runtime, tc.reason, msg)
		}
		seen[msg] = true
	}
	if msg := summaryHookUnavailableMessage(executor.StopHookCodex, executor.StopHookNotAsked); !strings.Contains(msg, "trust") {
		t.Errorf("Codex not-asked message %q must name the trust", msg)
	}
}
