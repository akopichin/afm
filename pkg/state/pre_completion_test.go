package state

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
)

const pcStage = "s"

// pcRun is a live store with a running stage and two pre_completion helpers: required "req" and advisory "adv".
type pcRun struct {
	t     *testing.T
	dir   string
	store *Store
	ops   int
}

func newPCRun(t *testing.T) *pcRun {
	t.Helper()
	dir := t.TempDir()
	store, err := Open(dir, []string{pcStage})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	mustRecordPolicy(t, store)
	r := &pcRun{t: t, dir: dir, store: store}
	r.move(StatusPending, StatusRunning, "test")
	r.attach("req", assistants.ModeRequired)
	r.attach("adv", assistants.ModeAdvisory)
	return r
}

func (r *pcRun) attach(id string, mode assistants.Mode) {
	r.t.Helper()
	created := TaskCreatedPayload{ProfileID: "p", Mode: string(mode), Trigger: string(assistants.TriggerPreCompletion), Scope: string(assistants.ScopeStage)}
	r.op("attach-"+id,
		Effect{Kind: kindTaskCreated, Target: Target{TaskID: id, StageID: pcStage}, Payload: mustMarshal(r.t, created)},
		r.status(id, TaskStatusChangedPayload{Status: string(assistants.StatusWatching)}))
}

func (r *pcRun) status(id string, p TaskStatusChangedPayload) Effect {
	return Effect{Kind: kindTaskStatusChanged, Target: Target{TaskID: id}, Payload: mustMarshal(r.t, p)}
}

func (r *pcRun) op(id string, effects ...Effect) {
	r.t.Helper()
	r.ops++
	id = fmt.Sprintf("%s-%d", id, r.ops)
	if _, err := r.store.ApplyRunOperation(Operation{ID: id, Kind: "test", Effects: effects}, nil); err != nil {
		r.t.Fatalf("%s: %v", id, err)
	}
}

func (r *pcRun) move(from, to StageStatus, event string, mutate ...func(*Transition)) {
	r.t.Helper()
	tr := &Transition{StageID: pcStage, From: from, To: to, Event: event}
	for _, m := range mutate {
		m(tr)
	}
	if err := r.store.Apply(tr); err != nil {
		r.t.Fatalf("%s: %s -> %s: %v", event, from, to, err)
	}
}

func withStep(step PreCompletionStep) func(*Transition) {
	return func(tr *Transition) { tr.PreCompletion = &step }
}

func (r *pcRun) round() *PreCompletionState { return r.store.Snapshot().Stages[pcStage].PreCompletion }

func (r *pcRun) request() agentcomm.MessageID { return PreCompletionRequestID(r.round().RoundSeq) }

// answer commits a helper's turn for the round request: result with verdict, then the outcome.
func (r *pcRun) answer(id string, turn uint64, verdict assistants.Verdict) {
	r.t.Helper()
	req := string(r.request())
	result := TaskResultPayload{Result: assistants.Result{Verdict: verdict, Summary: "r"}, RequestID: req, Turn: turn}
	r.op("start-"+id, r.status(id, TaskStatusChangedPayload{Status: string(assistants.StatusRunning), Turn: turn, RequestID: req}))
	r.op("result-"+id, Effect{Kind: kindTaskResult, Target: Target{TaskID: id}, Payload: mustMarshal(r.t, result)},
		r.status(id, TaskStatusChangedPayload{Status: string(assistants.StatusWatching), Turn: turn, RequestID: req, RequestOutcome: RequestOutcomeResult}))
}

func (r *pcRun) decide(lim PreCompletionLimits) RoundDecisionKind {
	return PreCompletionDecision(r.store.Snapshot(), pcStage, lim).Kind
}

func (r *pcRun) replayed() RunState {
	r.t.Helper()
	rs, err := LoadRunState(r.dir)
	if err != nil {
		r.t.Fatalf("LoadRunState: %v", err)
	}
	return rs
}

var pcLimits = PreCompletionLimits{MaxFailures: 1, MaxRetries: 3}

func TestPreCompletion_RoundStartFixesTasksQueuesRequestAndReplays(t *testing.T) {
	r := newPCRun(t)
	r.move(StatusRunning, StatusAwaitingPreCompletion, preCompletionEventAwait,
		withStep(PreCompletionStep{Phase: "implementation", Attempt: 2, VerifyFailures: 1, VerifyStalemates: 1, VerifyFingerprint: "fp", Feedback: true}))

	got := r.round()
	want := &PreCompletionState{
		Round: 1, RoundSeq: got.RoundSeq, Phase: "implementation", Tasks: []assistants.TaskID{"req", "adv"},
		Attempt: 2, VerifyFailures: 1, VerifyStalemates: 1, VerifyFingerprint: "fp", Feedback: true,
	}
	if got.RoundSeq == 0 || !reflect.DeepEqual(got, want) {
		t.Fatalf("round = %+v, want %+v", got, want)
	}
	for _, id := range []assistants.TaskID{"req", "adv"} {
		if q := r.store.Snapshot().Assistants.Tasks[id].PendingRequests; !reflect.DeepEqual(q, []agentcomm.MessageID{r.request()}) {
			t.Fatalf("%s queue = %v, want the round request %s", id, q, r.request())
		}
	}
	live := r.store.Snapshot().Stages[pcStage]
	if replayed := r.replayed().Stages[pcStage]; !reflect.DeepEqual(replayed.PreCompletion, live.PreCompletion) {
		t.Fatalf("replayed round = %+v, live %+v", replayed.PreCompletion, live.PreCompletion)
	}
}

func TestPreCompletion_HelperAttachedAfterTheRoundStartedIsNotInIt(t *testing.T) {
	r := newPCRun(t)
	r.move(StatusRunning, StatusAwaitingPreCompletion, preCompletionEventAwait, withStep(PreCompletionStep{}))
	r.attach("late", assistants.ModeRequired)
	if got := r.round().Tasks; !reflect.DeepEqual(got, []assistants.TaskID{"req", "adv"}) {
		t.Fatalf("round set = %v, want the helpers at the start of the round", got)
	}
	if q := r.store.Snapshot().Assistants.Tasks["late"].PendingRequests; len(q) != 0 {
		t.Fatalf("late helper queue = %v, want none", q)
	}
}

func TestPreCompletionDecision_Table(t *testing.T) {
	type answers map[string]assistants.Verdict
	tests := []struct {
		name    string
		answers answers
		lim     PreCompletionLimits
		prep    func(*pcRun)
		want    RoundDecisionKind
	}{
		{"nobody answered yet", nil, pcLimits, nil, RoundDecisionPending},
		{"required passed, advisory still working", answers{"req": assistants.VerdictPass}, pcLimits, nil, RoundDecisionPending},
		{"all passed", answers{"req": assistants.VerdictPass, "adv": assistants.VerdictPass}, pcLimits, nil, RoundDecisionAccepted},
		{"required needs changes", answers{"req": assistants.VerdictNeedsChanges, "adv": assistants.VerdictPass}, pcLimits, nil, RoundDecisionCorrect},
		{"advisory needs changes is no veto", answers{"req": assistants.VerdictPass, "adv": assistants.VerdictNeedsChanges}, pcLimits, nil, RoundDecisionAccepted},
		{"required inconclusive blocks", answers{"req": assistants.VerdictInconclusive, "adv": assistants.VerdictPass}, pcLimits, nil, RoundDecisionBlocked},
		{"needs changes beats a blocked required", answers{"req": assistants.VerdictNeedsChanges, "adv": assistants.VerdictInconclusive}, pcLimits, nil, RoundDecisionCorrect},
		{"zero budget exhausts at once", answers{"req": assistants.VerdictNeedsChanges, "adv": assistants.VerdictPass}, PreCompletionLimits{MaxFailures: 0, MaxRetries: 3}, nil, RoundDecisionExhausted},
		{"attempts used up exhaust", answers{"req": assistants.VerdictNeedsChanges, "adv": assistants.VerdictPass}, PreCompletionLimits{MaxFailures: 2, MaxRetries: 0}, nil, RoundDecisionExhausted},
		{"waived round request passes", answers{"adv": assistants.VerdictPass}, pcLimits, func(r *pcRun) { r.waive("req") }, RoundDecisionAccepted},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newPCRun(t)
			r.move(StatusRunning, StatusAwaitingPreCompletion, preCompletionEventAwait, withStep(PreCompletionStep{}))
			for _, id := range []string{"req", "adv"} {
				if v, ok := tc.answers[id]; ok {
					r.answer(id, 1, v)
				}
			}
			if tc.prep != nil {
				tc.prep(r)
			}
			if got := r.decide(tc.lim); got != tc.want {
				t.Fatalf("decision = %s, want %s", got, tc.want)
			}
		})
	}
}

func (r *pcRun) waive(id string) {
	r.t.Helper()
	p := TaskWaivedPayload{Reason: "ok", RequestID: string(r.request())}
	r.op("waive-"+id, Effect{Kind: kindTaskWaived, Target: Target{TaskID: id}, Payload: mustMarshal(r.t, p)})
}

// hold sets (or, with kindTaskHoldCleared, clears) the manual hold of PauseTask on helper id.
func (r *pcRun) hold(kind, id string) {
	r.t.Helper()
	r.op(kind+"-"+id, Effect{Kind: kind, Target: Target{TaskID: id}, Payload: mustMarshal(r.t, TaskHoldPayload{Owner: "manual"})})
}

// A held helper has not finished its request, whatever its mode: the round waits for it as a human's decision (Continue,
// Waive or Cancel of the helper), and neither an acceptance nor a correction goes past it. Continue makes it work again.
func TestPreCompletionDecision_HeldHelperHoldsTheRound(t *testing.T) {
	type answers map[string]assistants.Verdict
	tests := []struct {
		name    string
		answers answers
		held    string
		lim     PreCompletionLimits
	}{
		{"held advisory beside a passed required", answers{"req": assistants.VerdictPass}, "adv", pcLimits},
		{"held advisory beside a required needs_changes", answers{"req": assistants.VerdictNeedsChanges}, "adv", pcLimits},
		{"held required beside a required needs_changes", answers{"req": assistants.VerdictNeedsChanges, "adv": assistants.VerdictPass}, "req2", pcLimits},
		{"held required beside an exhausted correction", answers{"req": assistants.VerdictNeedsChanges, "adv": assistants.VerdictPass}, "req2", PreCompletionLimits{MaxRetries: 3}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newPCRun(t)
			r.attach("req2", assistants.ModeRequired)
			r.move(StatusRunning, StatusAwaitingPreCompletion, preCompletionEventAwait, withStep(PreCompletionStep{}))
			for _, id := range []string{"req", "adv"} {
				if v, ok := tc.answers[id]; ok {
					r.answer(id, 1, v)
				}
			}
			if tc.held == "adv" {
				r.answer("req2", 1, assistants.VerdictPass)
			}
			r.hold(kindTaskHoldSet, tc.held)
			if got := r.decide(tc.lim); got != RoundDecisionBlocked {
				t.Fatalf("decision with %s held = %s, want blocked until a human settles it", tc.held, got)
			}
			if !PreCompletionNeedsHuman(ptr(r.store.Snapshot()), pcStage) {
				t.Fatal("a round held by a paused helper must need a human")
			}
			r.hold(kindTaskHoldCleared, tc.held)
			if got := r.decide(tc.lim); got != RoundDecisionPending {
				t.Fatalf("decision once %s is continued = %s, want pending for its answer", tc.held, got)
			}
		})
	}
}

func TestPreCompletionDecision_DeadRequiredHelper(t *testing.T) {
	r := newPCRun(t)
	r.move(StatusRunning, StatusAwaitingPreCompletion, preCompletionEventAwait, withStep(PreCompletionStep{}))
	r.answer("adv", 1, assistants.VerdictPass)
	r.op("fail-req", r.status("req", TaskStatusChangedPayload{Status: string(assistants.StatusFailed)}))
	if got := r.decide(pcLimits); got != RoundDecisionBlocked {
		t.Fatalf("failed required helper: %s, want blocked for a human", got)
	}
	exit := pcLimits
	exit.ExitOnFail = true
	if got := r.decide(exit); got != RoundDecisionFailed {
		t.Fatalf("with exit_on_fail: %s, want failed", got)
	}
	if !PreCompletionNeedsHuman(ptr(r.store.Snapshot()), pcStage) {
		t.Fatal("a blocked round must need a human")
	}
}

func ptr[T any](v T) *T { return &v }

func TestPreCompletion_RoundScopedWaiverDoesNotCloseTheNextRound(t *testing.T) {
	r := newPCRun(t)
	r.move(StatusRunning, StatusAwaitingPreCompletion, preCompletionEventAwait, withStep(PreCompletionStep{}))
	r.waive("req")
	r.answer("adv", 1, assistants.VerdictPass)
	r.move(StatusAwaitingPreCompletion, StatusRunning, preCompletionEventDecided, withStep(PreCompletionStep{Decision: PreCompletionAccepted}))
	r.move(StatusRunning, StatusRunning, preCompletionEventConsumed, withStep(PreCompletionStep{}))
	r.move(StatusRunning, StatusAwaitingPreCompletion, preCompletionEventAwait, withStep(PreCompletionStep{}))
	if task := r.store.Snapshot().Assistants.Tasks["req"]; task.Waived {
		t.Fatal("a round waiver must not mark the helper waived for good")
	}
	r.answer("adv", 2, assistants.VerdictPass)
	if got := r.decide(pcLimits); got != RoundDecisionPending {
		t.Fatalf("second round: %s, want pending (the earlier waiver covers only the first request)", got)
	}
}

func TestPreCompletion_NeedsChangesCountsCorrectionAndKeepsCounters(t *testing.T) {
	r := newPCRun(t)
	r.move(StatusRunning, StatusAwaitingPreCompletion, preCompletionEventAwait, withStep(PreCompletionStep{Phase: "review"}))
	r.move(StatusAwaitingPreCompletion, StatusRunning, preCompletionEventDecided,
		withStep(PreCompletionStep{Decision: PreCompletionNeedsChanges, Attempt: 1, VerifyFailures: 2}))
	got := r.round()
	if got.Decision != PreCompletionNeedsChanges || got.Corrections != 1 || got.Accepted || got.Attempt != 1 || got.VerifyFailures != 2 || got.Phase != "review" || got.Open() {
		t.Fatalf("after needs_changes: %+v", got)
	}
	r.move(StatusRunning, StatusAwaitingPreCompletion, preCompletionEventAwait, withStep(PreCompletionStep{Attempt: 1, VerifyFailures: 2}))
	if got := r.round(); got.Round != 2 || got.Corrections != 1 || got.Decision != "" || !got.Open() {
		t.Fatalf("second round: %+v", got)
	}
}

func TestPreCompletion_RetryAndReviseStartANewCycle(t *testing.T) {
	for _, tc := range []struct {
		name  string
		leave func(*pcRun)
	}{
		{"retry", func(r *pcRun) {
			r.move(StatusAwaitingPreCompletion, StatusFailed, "fail", withStep(PreCompletionStep{Decision: PreCompletionExhausted}))
			r.move(StatusFailed, StatusPending, "manual_retry")
		}},
		{"revise", func(r *pcRun) { r.move(StatusAwaitingPreCompletion, StatusRevising, "revise") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newPCRun(t)
			r.move(StatusRunning, StatusAwaitingPreCompletion, preCompletionEventAwait, withStep(PreCompletionStep{Attempt: 3}))
			tc.leave(r)
			want := &PreCompletionState{Cycle: 1}
			if got := r.round(); !reflect.DeepEqual(got, want) {
				t.Fatalf("after %s: %+v, want a fresh cycle %+v", tc.name, got, want)
			}
			if PreCompletionWindowOpen(ptr(r.store.Snapshot()), pcStage) {
				t.Fatal("a new cycle has no open window")
			}
		})
	}
}

func TestPreCompletion_FailureRecordsTheDecision(t *testing.T) {
	r := newPCRun(t)
	r.move(StatusRunning, StatusAwaitingPreCompletion, preCompletionEventAwait, withStep(PreCompletionStep{}))
	r.move(StatusAwaitingPreCompletion, StatusFailed, "fail", withStep(PreCompletionStep{Decision: PreCompletionExhausted}))
	if got := r.round(); got.Decision != PreCompletionExhausted || got.Accepted || got.Open() {
		t.Fatalf("after exhausted failure: %+v", got)
	}
}

func TestPreCompletion_AcceptedIntentDefaultDeny(t *testing.T) {
	accept := func(r *pcRun) {
		r.move(StatusRunning, StatusAwaitingPreCompletion, preCompletionEventAwait, withStep(PreCompletionStep{}))
		r.move(StatusAwaitingPreCompletion, StatusRunning, preCompletionEventDecided, withStep(PreCompletionStep{Decision: PreCompletionAccepted}))
		if !r.round().Accepted {
			t.Fatal("accepted decision must set the intent")
		}
	}
	keeps := []struct {
		name string
		do   func(*pcRun)
	}{
		{"pause and continue", func(r *pcRun) {
			r.move(StatusRunning, StatusPaused, "pause")
			r.move(StatusPaused, StatusRunning, "continue")
		}},
		{"verify park and wake", func(r *pcRun) {
			r.move(StatusRunning, StatusAwaitingQuota, "await_quota", func(tr *Transition) { tr.Quota = &QuotaWait{Target: QuotaTargetVerify} })
			r.move(StatusAwaitingQuota, StatusRunning, "quota_reset")
		}},
	}
	for _, tc := range keeps {
		t.Run("keeps "+tc.name, func(t *testing.T) {
			r := newPCRun(t)
			accept(r)
			tc.do(r)
			if !r.round().Accepted || !PreCompletionWindowOpen(ptr(r.store.Snapshot()), pcStage) {
				t.Fatalf("intent lost: %+v", r.round())
			}
		})
	}
	consumes := []struct {
		name string
		do   func(*pcRun)
	}{
		{"consumed record", func(r *pcRun) {
			r.move(StatusRunning, StatusRunning, preCompletionEventConsumed, withStep(PreCompletionStep{}))
		}},
		{"retry schedule", func(r *pcRun) { r.move(StatusRunning, StatusRetrying, "schedule_retry") }},
		{"ask user", func(r *pcRun) { r.move(StatusRunning, StatusAwaitingUserInput, "ask_user") }},
		{"peer wait", func(r *pcRun) { r.move(StatusRunning, StatusAwaitingPeer, "await_peer") }},
		{"other quota park", func(r *pcRun) {
			r.move(StatusRunning, StatusAwaitingQuota, "await_quota", func(tr *Transition) { tr.Quota = &QuotaWait{Target: "implementation"} })
		}},
		{"unknown record", func(r *pcRun) { r.move(StatusRunning, StatusRunning, "something_new") }},
		{"completion", func(r *pcRun) { r.move(StatusRunning, StatusDone, "complete") }},
	}
	for _, tc := range consumes {
		t.Run("consumes "+tc.name, func(t *testing.T) {
			r := newPCRun(t)
			accept(r)
			tc.do(r)
			if r.round().Accepted {
				t.Fatalf("intent survived %s: %+v", tc.name, r.round())
			}
		})
	}
}

func TestPreCompletion_AcceptEmptyOpensWindowAndReplays(t *testing.T) {
	r := newPCRun(t)
	r.move(StatusRunning, StatusRunning, preCompletionEventAcceptEmpty, withStep(PreCompletionStep{Phase: "autonomous"}))
	got := r.round()
	if !got.Accepted || got.Decision != PreCompletionAccepted || got.Round != 0 || got.Open() || got.RoundSeq == 0 {
		t.Fatalf("empty acceptance: %+v", got)
	}
	if !PreCompletionWindowOpen(ptr(r.store.Snapshot()), pcStage) {
		t.Fatal("an unspent empty acceptance keeps the window closed to Attach")
	}
	if replayed := r.replayed().Stages[pcStage].PreCompletion; !reflect.DeepEqual(replayed, got) {
		t.Fatalf("replayed %+v, live %+v", replayed, got)
	}
}

func TestPreCompletion_WindowOpenWhileRoundIsUnresolvedInAnyStatus(t *testing.T) {
	r := newPCRun(t)
	if PreCompletionWindowOpen(ptr(r.store.Snapshot()), pcStage) {
		t.Fatal("no round yet: the window is open for Attach")
	}
	r.move(StatusRunning, StatusAwaitingPreCompletion, preCompletionEventAwait, withStep(PreCompletionStep{}))
	r.move(StatusAwaitingPreCompletion, StatusPaused, "pause")
	if !PreCompletionWindowOpen(ptr(r.store.Snapshot()), pcStage) {
		t.Fatal("a paused unresolved round must still refuse Attach")
	}
	r.move(StatusPaused, StatusAwaitingPreCompletion, "continue")
	r.move(StatusAwaitingPreCompletion, StatusRunning, preCompletionEventDecided, withStep(PreCompletionStep{Decision: PreCompletionNeedsChanges}))
	if PreCompletionWindowOpen(ptr(r.store.Snapshot()), pcStage) {
		t.Fatal("after needs_changes the correction turn accepts Attach again")
	}
}

func TestPreCompletion_RoundHelpersAreOutsideTheStagesCompletionGate(t *testing.T) {
	r := newPCRun(t)
	rs := r.store.Snapshot()
	if _, blocked := rs.Assistants.RequiredBlocker(pcStage); blocked {
		t.Fatal("a required pre_completion helper must not block the stage's own completion gate")
	}
	if _, dead := rs.Assistants.DeadRequiredTask(pcStage); dead {
		t.Fatal("a dead required pre_completion helper is the round's business")
	}
}

func TestPreCompletion_NeedsHumanShowsAsIdleAndApprovalTime(t *testing.T) {
	r := newPCRun(t)
	r.move(StatusRunning, StatusAwaitingPreCompletion, preCompletionEventAwait, withStep(PreCompletionStep{}))
	rs := r.store.Snapshot()
	if isIdle(&rs) {
		t.Fatal("a round with helpers at work is machine activity")
	}
	r.answer("req", 1, assistants.VerdictInconclusive)
	r.answer("adv", 1, assistants.VerdictPass)
	rs = r.store.Snapshot()
	if !isIdle(&rs) {
		t.Fatal("a blocked round waits for a human: idle")
	}
	if got := classifyStageTime(&rs, pcStage); got != TimeApproval {
		t.Fatalf("blocked round time kind = %s, want approval", got)
	}
}
