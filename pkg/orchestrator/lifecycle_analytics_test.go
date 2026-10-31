package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/agentjobs"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/clock"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/lifecyclehooks"
	"github.com/akopichin/afm/pkg/memorypipeline"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// The terminal hook snapshot is checked on transitions driven by hand on a fake state clock, with jobs put on
// the journal the way the executor does, so every boundary is chosen by the test. Fake helpers and stub
// scripts only: no claude or codex process is started.

// hookAnalytics is data.analytics as a hook reads it from stdin; pointers tell null from zero.
type hookAnalytics struct {
	Version       int        `json:"version"`
	AsOf          time.Time  `json:"as_of"`
	StageEventSeq uint64     `json:"stage_event_seq"`
	Scope         string     `json:"scope"`
	Timing        hookTiming `json:"timing"`
	AgentJobs     hookJobs   `json:"agent_jobs"`
	Usage         hookUsage  `json:"usage"`
}

type hookTiming struct {
	TotalMS      *int64 `json:"total_ms"`
	WorkMS       *int64 `json:"work_ms"`
	WaitingMS    *int64 `json:"waiting_ms"`
	PausedMS     *int64 `json:"paused_ms"`
	HelpersMS    *int64 `json:"helpers_ms"`
	SchedulingMS *int64 `json:"scheduling_ms"`
	Complete     bool   `json:"complete"`
	Reason       string `json:"reason"`
}

type hookJobs struct {
	Count       *int   `json:"count"`
	Active      *int   `json:"active"`
	Measured    *int   `json:"measured"`
	UnknownTime *int   `json:"unknown_time"`
	DurationMS  *int64 `json:"duration_ms"`
	Complete    bool   `json:"complete"`
	Reason      string `json:"reason"`
}

type hookUsage struct {
	Health      string      `json:"health"`
	Coverage    string      `json:"coverage"`
	MeteredJobs *int        `json:"metered_jobs"`
	PendingJobs *int        `json:"pending_jobs"`
	Tokens      *hookTokens `json:"tokens"`
	Reason      string      `json:"reason"`
}

type hookTokens struct {
	Total uint64 `json:"total"`
}

// hookDelivery is one payload exactly as a hook's stdin would carry it.
type hookDelivery struct {
	hook  string
	event lifecyclehooks.EventType
	raw   []byte
}

// hookEnvelope is the part of the payload around data.analytics the tests read.
type hookEnvelope struct {
	OccurredAt time.Time `json:"occurred_at"`
	Data       hookData  `json:"data"`
}

type hookData struct {
	Analytics json.RawMessage `json:"analytics"`
}

func (d hookDelivery) envelope(t *testing.T) (occurredAt time.Time, analytics json.RawMessage) {
	t.Helper()
	var p hookEnvelope
	if err := json.Unmarshal(d.raw, &p); err != nil {
		t.Fatalf("payload %s: %v", d.raw, err)
	}
	return p.OccurredAt, p.Data.Analytics
}

func (d hookDelivery) analytics(t *testing.T) hookAnalytics {
	t.Helper()
	_, raw := d.envelope(t)
	if raw == nil {
		t.Fatalf("%s payload has no data.analytics: %s", d.event, d.raw)
	}
	var a hookAnalytics
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatalf("analytics %s: %v", raw, err)
	}
	return a
}

// payloadRecorder is a real dispatcher with hooks "a" and "b" that record what they would read on stdin. Hook "b"
// can be held on its first stage_failed until hold closes: what it marshals then shows what Emit had fixed.
type payloadRecorder struct {
	disp *lifecyclehooks.Dispatcher
	mu   sync.Mutex
	seen []hookDelivery
	more chan struct{}
}

func newPayloadRecorder(t *testing.T, hold <-chan struct{}) *payloadRecorder {
	t.Helper()
	r := &payloadRecorder{more: make(chan struct{}, 1)}
	var held bool
	hook := func(id string) lifecyclehooks.RegisteredHook {
		return lifecyclehooks.RegisteredHook{Hook: lifecyclehooks.Hook{ID: id, Events: lifecyclehooks.EventSelector{All: true}, Command: "true"}}
	}
	r.disp = lifecyclehooks.New(lifecyclehooks.DispatcherOptions{
		Config: lifecyclehooks.DispatcherConfig{RunID: "run1"},
		Hooks:  []lifecyclehooks.RegisteredHook{hook("a"), hook("b")},
		RunFunc: func(_ context.Context, h lifecyclehooks.Hook, _ lifecyclehooks.DispatcherConfig, p lifecyclehooks.Payload, _ string) error {
			if h.ID == "b" && hold != nil && !held && p.Event == lifecyclehooks.EventStageFailed {
				held = true // only hook b's own worker touches it
				awaitRelease(t, hold)
			}
			raw, err := json.Marshal(p)
			if err != nil {
				return err
			}
			r.mu.Lock()
			r.seen = append(r.seen, hookDelivery{hook: h.ID, event: p.Event, raw: raw})
			r.mu.Unlock()
			select {
			case r.more <- struct{}{}:
			default:
			}
			return nil
		},
	})
	r.disp.Start()
	t.Cleanup(r.disp.Stop)
	return r
}

func (r *payloadRecorder) find(hook string, event lifecyclehooks.EventType, n int) (hookDelivery, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range r.seen {
		if d.hook == hook && d.event == event {
			if n--; n == 0 {
				return d, true
			}
		}
	}
	return hookDelivery{}, false
}

// await returns the n-th delivery (from 1) of event to hook, waiting for the hook's worker.
func (r *payloadRecorder) await(t *testing.T, hook string, event lifecyclehooks.EventType, n int) hookDelivery {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		if d, ok := r.find(hook, event, n); ok {
			return d
		}
		select {
		case <-r.more:
		case <-timeout:
			t.Fatalf("no delivery #%d of %s to hook %s", n, event, hook)
		}
	}
}

// delivered returns everything delivered so far, after the queues drained.
func (r *payloadRecorder) delivered(t *testing.T) []hookDelivery {
	t.Helper()
	if !r.disp.Flush(10 * time.Second) {
		t.Fatal("hook deliveries did not drain")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]hookDelivery(nil), r.seen...)
}

type analyticsRig struct {
	o     *Orchestrator
	store *state.Store
	jobs  *agentjobs.Store
	acct  *accounting.Store
	fc    *clock.FakeClock
	hooks *payloadRecorder
}

// newAnalyticsRig is a live run whose agent journal started recording at jobsT0, with hooks configured.
func newAnalyticsRig(t *testing.T, hold <-chan struct{}) *analyticsRig {
	t.Helper()
	fc := useFakeStateClock(t, jobsT0)
	o, store := newAssistantsTestOrchestrator(t, true)
	jobs, err := agentjobs.Open(o.opts.RunDir, agentjobs.Header{RecordingStartedAt: jobsT0})
	if err != nil {
		t.Fatal(err)
	}
	acct, err := accounting.Open(o.opts.RunDir, accounting.NewResolver(accounting.PricingConfig{}))
	if err != nil {
		t.Fatal(err)
	}
	// The helpers finish before the journals close, as in a real shutdown.
	t.Cleanup(func() { o.concurrency.WaitAgents(); _ = jobs.Close(); _ = acct.Close() })
	o.opts.Jobs, o.opts.Accounting, o.liveSince = jobs, acct, jobsT0
	if err := store.BeginRun(); err != nil {
		t.Fatal(err)
	}
	r := &analyticsRig{o: o, store: store, jobs: jobs, acct: acct, fc: fc, hooks: newPayloadRecorder(t, hold)}
	o.hooks = r.hooks.disp
	return r
}

func (r *analyticsRig) trigger(t *testing.T, stageID string, ev bus.FSMEvent) uint64 {
	t.Helper()
	_, seq, ok := r.o.triggerWithSeq(stageID, ev, bus.GuardCtx{}, "")
	if !ok {
		t.Fatalf("%s on %s was refused", ev, stageID)
	}
	return seq
}

// apply commits a transition without the orchestrator, as a concurrent caller would.
func (r *analyticsRig) apply(t *testing.T, stageID string, from, to state.StageStatus, ev bus.FSMEvent) uint64 {
	t.Helper()
	tr := &state.Transition{StageID: stageID, From: from, To: to, Event: string(ev)}
	if err := r.store.Apply(tr); err != nil {
		t.Fatal(err)
	}
	return tr.Seq
}

func (r *analyticsRig) authorJob(stageID string) *executor.JobConfig {
	return r.o.jobConfig(r.o.stageJobMeta(flow.Stage{ID: stageID}, phaseImplementation, "agent"), "")
}

func (r *analyticsRig) helperJob(taskID assistants.TaskID) *executor.JobConfig {
	return r.o.jobConfig(r.o.taskJobMeta(r.store.Snapshot(), taskID, "helper-cmd"), "")
}

func (r *analyticsRig) usage(t *testing.T, job *executor.JobConfig, id string, in, out uint64) {
	t.Helper()
	obs := accounting.Observation{Metered: true, Channel: "anthropic", Model: "claude-opus-4-8", ModelSource: "observed",
		Tokens: accounting.Tokens{UncachedInput: in, Output: out}, JobID: id}
	if err := r.acct.Append(obs, job.Meta.StageID, job.Meta.Phase, ""); err != nil {
		t.Fatal(err)
	}
}

// finishedJob is one whole job in the executor's order: started, final usage, finished.
func (r *analyticsRig) finishedJob(t *testing.T, job *executor.JobConfig, id string, start, end time.Time, in, out uint64) {
	t.Helper()
	publishStarted(t, job, id, start)
	r.usage(t, job, id, in, out)
	publishFinished(t, job, id, end, end.Sub(start))
}

// blockingHelper makes every helper turn announce its task and wait for release.
func (r *analyticsRig) blockingHelper(t *testing.T) (entered <-chan assistants.TaskID, release chan struct{}) {
	ch, release := make(chan assistants.TaskID, 4), make(chan struct{})
	r.o.runHelperAgent = func(_ context.Context, taskID, _, _, _, _, _ string) (executor.HelperOutcome, error) {
		ch <- assistants.TaskID(taskID)
		awaitRelease(t, release)
		return passResultOutcome(), nil
	}
	return ch, release
}

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatal("timed out")
	}
	var zero T
	return zero
}

func wantInt[T int | int64](t *testing.T, name string, got *T, want T) {
	t.Helper()
	if got == nil || *got != want {
		t.Errorf("%s = %v, want %v", name, deref(got), want)
	}
}

func deref[T any](p *T) any {
	if p == nil {
		return "null"
	}
	return *p
}

// Each terminal event carries the stage's cumulative picture at its own transition: a retry adds to the earlier
// attempt and helpers of the stage count, other stages and run overhead do not. The payload is fixed at Emit:
// a hook held until the retry was over still gets the first snapshot byte for byte.
func TestStageAnalytics_CumulativeAcrossRetriesAndHelpers(t *testing.T) {
	hold := make(chan struct{})
	r := newAnalyticsRig(t, hold)
	entered, release := r.blockingHelper(t)
	author := r.authorJob(assistantsTestStageID)

	activateStage(t, r.o, assistantsTestStageID)
	r.finishedJob(t, author, "a1", jobsT0.Add(time.Minute), jobsT0.Add(2*time.Minute), 100, 10)
	r.finishedJob(t, r.authorJob("other"), "f1", jobsT0.Add(time.Minute), jobsT0.Add(2*time.Minute), 1000, 100)
	overhead := r.o.memoryJob(memorypipeline.AgentSpec{Kind: memorypipeline.KindAggregate, Scope: memorypipeline.ScopeRunOverhead})
	r.finishedJob(t, overhead, "m1", jobsT0.Add(time.Minute), jobsT0.Add(2*time.Minute), 5000, 500)
	r.fc.Advance(3 * time.Minute)
	failSeq := r.trigger(t, assistantsTestStageID, bus.EvFail)

	first := r.hooks.await(t, "a", lifecyclehooks.EventStageFailed, 1)
	occurred, firstRaw := first.envelope(t)
	got := first.analytics(t)
	if got.Version != 1 || got.Scope != "stage_cumulative" || got.StageEventSeq != failSeq {
		t.Fatalf("identity = %+v, want version 1, stage_cumulative, seq %d", got, failSeq)
	}
	failedAt := jobsT0.Add(3 * time.Minute)
	if !got.AsOf.Equal(failedAt) || !occurred.Equal(failedAt) {
		t.Fatalf("as_of = %s, occurred_at = %s, want the transition time %s", got.AsOf, occurred, failedAt)
	}
	wantInt(t, "first timing.total_ms", got.Timing.TotalMS, 180_000)
	wantInt(t, "first timing.work_ms", got.Timing.WorkMS, 180_000)
	wantInt(t, "first agent_jobs.count", got.AgentJobs.Count, 1)
	wantInt(t, "first agent_jobs.duration_ms", got.AgentJobs.DurationMS, 60_000)
	if !got.Timing.Complete || !got.AgentJobs.Complete || got.Usage.Tokens == nil || got.Usage.Tokens.Total != 110 {
		t.Fatalf("first snapshot = %+v, want complete with the first attempt's 110 tokens only", got)
	}

	// Retry: a second attempt and a helper of the stage.
	r.fc.Advance(time.Minute)
	r.trigger(t, assistantsTestStageID, bus.EvManualRetry)
	activateStage(t, r.o, assistantsTestStageID)
	helper := attachTestTask(context.Background(), t, r.o, "op-helper")
	if id := receive(t, entered); id != helper {
		t.Fatalf("helper turn of %s, want %s", id, helper)
	}
	r.finishedJob(t, r.helperJob(helper), "h1", jobsT0.Add(5*time.Minute), jobsT0.Add(6*time.Minute), 300, 30)
	close(release)
	r.finishedJob(t, author, "a2", jobsT0.Add(5*time.Minute), jobsT0.Add(7*time.Minute), 200, 20)
	r.fc.Advance(4 * time.Minute)
	doneSeq := r.trigger(t, assistantsTestStageID, bus.EvComplete)
	close(hold)

	second := r.hooks.await(t, "a", lifecyclehooks.EventStageFinished, 1).analytics(t)
	if second.StageEventSeq != doneSeq {
		t.Fatalf("seq = %d, want %d", second.StageEventSeq, doneSeq)
	}
	wantInt(t, "timing.work_ms", second.Timing.WorkMS, 420_000) // 3 min of the first attempt + 4 of the second
	wantInt(t, "agent_jobs.count", second.AgentJobs.Count, 3)
	wantInt(t, "agent_jobs.measured", second.AgentJobs.Measured, 3)
	wantInt(t, "agent_jobs.duration_ms", second.AgentJobs.DurationMS, 240_000)
	wantInt(t, "usage.metered_jobs", second.Usage.MeteredJobs, 3)
	if second.Usage.Tokens == nil || second.Usage.Tokens.Total != 110+330+220 {
		t.Fatalf("tokens = %+v, want both attempts and the helper, without the other stage and run overhead", second.Usage.Tokens)
	}
	if !second.AgentJobs.Complete || second.Usage.Coverage != "full" || !second.Timing.Complete {
		t.Fatalf("second snapshot = %+v, want complete", second)
	}

	_, heldRaw := r.hooks.await(t, "b", lifecyclehooks.EventStageFailed, 1).envelope(t)
	if !bytes.Equal(heldRaw, firstRaw) {
		t.Fatalf("held delivery of the same event differs:\n%s\n%s", heldRaw, firstRaw)
	}
}

// A Retry that commits between the terminal CAS and the snapshot leaves that transition's timing unknown; the
// jobs are still cut at the transition, so the new attempt stays out and a job that ended later is active.
func TestStageAnalytics_StageMovedOnBeforeTheSnapshot(t *testing.T) {
	r := newAnalyticsRig(t, nil)
	author := r.authorJob(assistantsTestStageID)
	late := r.o.jobConfig(agentjobs.JobMeta{AgentID: "helper:t-late", AgentKind: accounting.AgentKindHelper,
		StageID: assistantsTestStageID, RootStageID: assistantsTestStageID, TaskID: "t-late"}, "")

	activateStage(t, r.o, assistantsTestStageID)
	r.finishedJob(t, author, "a1", jobsT0.Add(time.Minute), jobsT0.Add(2*time.Minute), 100, 10)
	publishStarted(t, late, "late", jobsT0.Add(150*time.Second))
	r.fc.Advance(3 * time.Minute)
	failSeq := r.apply(t, assistantsTestStageID, state.StatusRunning, state.StatusFailed, bus.EvFail)

	r.fc.Advance(time.Minute)
	r.apply(t, assistantsTestStageID, state.StatusFailed, state.StatusPending, bus.EvManualRetry)
	r.apply(t, assistantsTestStageID, state.StatusPending, state.StatusReady, bus.EvReady)
	r.apply(t, assistantsTestStageID, state.StatusReady, state.StatusRunning, bus.EvStartRun)
	r.finishedJob(t, author, "a2", jobsT0.Add(270*time.Second), jobsT0.Add(5*time.Minute), 200, 20)
	r.usage(t, late, "late", 999, 1)
	publishFinished(t, late, "late", jobsT0.Add(4*time.Minute), 90*time.Second)

	r.o.emitLifecycleTransition(assistantsTestStageID, state.StatusRunning, state.StatusFailed, failSeq, bus.EvFail, "boom")

	d := r.hooks.await(t, "a", lifecyclehooks.EventStageFailed, 1)
	occurred, _ := d.envelope(t)
	got := d.analytics(t)
	failedAt := jobsT0.Add(3 * time.Minute)
	if got.StageEventSeq != failSeq || !got.AsOf.Equal(failedAt) || !occurred.Equal(failedAt) {
		t.Fatalf("seq %d as_of %s occurred_at %s, want seq %d at %s", got.StageEventSeq, got.AsOf, occurred, failSeq, failedAt)
	}
	if got.Timing.Complete || got.Timing.Reason != "stage_advanced_before_snapshot" || got.Timing.TotalMS != nil || got.Timing.WorkMS != nil {
		t.Fatalf("timing = %+v, want unknown with stage_advanced_before_snapshot", got.Timing)
	}
	wantInt(t, "agent_jobs.count", got.AgentJobs.Count, 2) // a1 and the late helper job; a2 started after
	wantInt(t, "agent_jobs.measured", got.AgentJobs.Measured, 1)
	wantInt(t, "agent_jobs.active", got.AgentJobs.Active, 1)
	wantInt(t, "agent_jobs.duration_ms", got.AgentJobs.DurationMS, 60_000)
	wantInt(t, "usage.pending_jobs", got.Usage.PendingJobs, 1)
	if got.AgentJobs.Complete || got.Usage.Tokens == nil || got.Usage.Tokens.Total != 110 || got.Usage.Coverage != "partial" {
		t.Fatalf("snapshot = %+v, want a partial picture with a1's 110 tokens", got)
	}
}

// Only committed stage_finished and stage_failed carry analytics: other events keep their old payload, a
// refused CAS emits nothing, and a verify failure is a stage_failed with its own snapshot.
func TestStageAnalytics_OnlyCommittedTerminalEvents(t *testing.T) {
	r := newAnalyticsRig(t, nil)
	activateStage(t, r.o, assistantsTestStageID)
	r.trigger(t, assistantsTestStageID, bus.EvAskUser)
	r.trigger(t, assistantsTestStageID, bus.EvUserAnswered)
	if _, ok := r.o.Trigger(assistantsTestStageID, bus.EvApprove, bus.GuardCtx{}, ""); ok {
		t.Fatal("approve of a running stage must be refused")
	}
	r.fc.Advance(time.Minute)
	seq := r.trigger(t, assistantsTestStageID, bus.EvVerifyFail)
	if _, ok := r.o.Trigger(assistantsTestStageID, bus.EvComplete, bus.GuardCtx{}, ""); ok {
		t.Fatal("complete of a failed stage must be refused")
	}

	var terminal int
	for _, d := range r.hooks.delivered(t) {
		_, raw := d.envelope(t)
		switch {
		case d.event == lifecyclehooks.EventStageFailed:
			terminal++
			if got := d.analytics(t); got.StageEventSeq != seq {
				t.Fatalf("verify failure seq = %d, want %d", got.StageEventSeq, seq)
			}
		case d.event == lifecyclehooks.EventStageFinished || d.event == lifecyclehooks.EventStageApproved:
			t.Fatalf("a refused transition was delivered: %s", d.raw)
		case raw != nil:
			t.Fatalf("%s must not carry analytics: %s", d.event, d.raw)
		default: // a non-terminal event keeps its old payload
		}
	}
	if terminal != 2 { // one delivery per hook
		t.Fatalf("stage_failed deliveries = %d, want 2", terminal)
	}
}

// Work still going on at the terminal transition makes the snapshot partial instead of holding the event back:
// an author whose dialog was cancelled, a helper mid-turn, a helper whose job is not on the journal yet.
func TestStageAnalytics_LiveWorkIsPartial(t *testing.T) {
	t.Run("author still running", func(t *testing.T) {
		r := newAnalyticsRig(t, nil)
		activateStage(t, r.o, assistantsTestStageID)
		publishStarted(t, r.authorJob(assistantsTestStageID), "a1", jobsT0.Add(time.Minute))
		r.fc.Advance(2 * time.Minute)
		r.trigger(t, assistantsTestStageID, bus.EvFail)

		got := r.hooks.await(t, "a", lifecyclehooks.EventStageFailed, 1).analytics(t)
		wantInt(t, "agent_jobs.active", got.AgentJobs.Active, 1)
		wantInt(t, "usage.pending_jobs", got.Usage.PendingJobs, 1)
		if got.AgentJobs.Complete || got.AgentJobs.Reason != "active_jobs" || got.Usage.Reason != "pending_usage" {
			t.Fatalf("snapshot = %+v, want partial with active work", got)
		}
	})
	t.Run("helper mid-turn, another not on the journal yet", func(t *testing.T) {
		r := newAnalyticsRig(t, nil)
		entered, _ := r.blockingHelper(t)
		activateStage(t, r.o, assistantsTestStageID)
		helper := attachTestTask(context.Background(), t, r.o, "op-helper")
		receive(t, entered)
		attachTestTask(context.Background(), t, r.o, "op-unpublished")
		receive(t, entered)
		publishStarted(t, r.helperJob(helper), "h1", jobsT0.Add(time.Minute))
		r.fc.Advance(2 * time.Minute)
		r.trigger(t, assistantsTestStageID, bus.EvComplete)

		got := r.hooks.await(t, "a", lifecyclehooks.EventStageFinished, 1).analytics(t)
		wantInt(t, "agent_jobs.count", got.AgentJobs.Count, 1)
		wantInt(t, "agent_jobs.active", got.AgentJobs.Active, 1)
		// The active count already shows the live job; the one reason goes to what no count shows.
		if got.AgentJobs.Complete || got.AgentJobs.Reason != "pending_start" {
			t.Fatalf("agent_jobs = %+v, want partial with pending_start", got.AgentJobs)
		}
		if got.Usage.Coverage == "full" || got.Usage.Reason != "pending_start" {
			t.Fatalf("usage = %+v, want unproven with pending_start", got.Usage)
		}
	})
	t.Run("helper job not published yet", func(t *testing.T) {
		r := newAnalyticsRig(t, nil)
		entered, _ := r.blockingHelper(t)
		activateStage(t, r.o, assistantsTestStageID)
		attachTestTask(context.Background(), t, r.o, "op-helper")
		receive(t, entered)
		r.fc.Advance(2 * time.Minute)
		r.trigger(t, assistantsTestStageID, bus.EvComplete)

		got := r.hooks.await(t, "a", lifecyclehooks.EventStageFinished, 1).analytics(t)
		wantInt(t, "agent_jobs.count", got.AgentJobs.Count, 0)
		if got.AgentJobs.Complete || got.AgentJobs.Reason != "pending_start" {
			t.Fatalf("agent_jobs = %+v, want partial with pending_start", got.AgentJobs)
		}
		// Zero tokens so far are not a proven zero: the job that has not started yet may still add some.
		if got.Usage.Coverage == "full" || got.Usage.Reason != "pending_start" {
			t.Fatalf("usage = %+v, want unproven with pending_start", got.Usage)
		}
	})
}

// The journal is frozen before usage is read. Usage of a job that is not on that journal — a Retry that
// published its job and usage in between — stays out of the earlier transition's snapshot.
func TestStageAnalytics_UsageOfAJobPublishedAfterTheFreezeStaysOut(t *testing.T) {
	r := newAnalyticsRig(t, nil)
	author := r.authorJob(assistantsTestStageID)
	activateStage(t, r.o, assistantsTestStageID)
	r.finishedJob(t, author, "a1", jobsT0.Add(time.Minute), jobsT0.Add(2*time.Minute), 100, 10)
	r.fc.Advance(3 * time.Minute)
	failSeq := r.apply(t, assistantsTestStageID, state.StatusRunning, state.StatusFailed, bus.EvFail)

	// The retry's usage is on the ledger, its started line not yet on the journal the snapshot reads.
	r.usage(t, author, "a2", 17, 3)
	r.o.emitLifecycleTransition(assistantsTestStageID, state.StatusRunning, state.StatusFailed, failSeq, bus.EvFail, "boom")
	publishStarted(t, author, "a2", jobsT0.Add(4*time.Minute))

	got := r.hooks.await(t, "a", lifecyclehooks.EventStageFailed, 1).analytics(t)
	wantInt(t, "usage.metered_jobs", got.Usage.MeteredJobs, 1)
	if got.Usage.Tokens == nil || got.Usage.Tokens.Total != 110 {
		t.Fatalf("tokens = %+v, want the frozen 110 without the retry's 20", got.Usage.Tokens)
	}
}

// A stage migrated from state.json ran before anything recorded it: its hook payload says so in every section
// instead of reporting zero jobs and tokens as a fact.
func TestStageAnalytics_StageMigratedFromStateJSONIsNotAProvenZero(t *testing.T) {
	o, _, _ := newLegacyMigratedOrchestrator(t)
	hooks := newPayloadRecorder(t, nil)
	o.hooks = hooks.disp
	seq := o.opts.Store.Snapshot().Stages["old"].Seq
	o.emitLifecycleTransition("old", state.StatusRunning, state.StatusDone, seq, bus.EvComplete, "")

	got := hooks.await(t, "a", lifecyclehooks.EventStageFinished, 1).analytics(t)
	const reason = "started_before_recording"
	if got.Timing.Complete || got.Timing.TotalMS != nil || got.Timing.Reason != reason {
		t.Fatalf("timing = %+v, want unknown with %s", got.Timing, reason)
	}
	if got.AgentJobs.Complete || got.AgentJobs.Reason != reason {
		t.Fatalf("agent_jobs = %+v, want unproven with %s", got.AgentJobs, reason)
	}
	if got.Usage.Coverage != string(accounting.CoverageNone) || got.Usage.Tokens != nil || got.Usage.MeteredJobs != nil || got.Usage.Reason != reason {
		t.Fatalf("usage = %+v, want unknown tokens with %s", got.Usage, reason)
	}
}

// A required helper holds the stage in awaiting_assistants; the completion its result releases sees the
// helper's closed job and final usage, and nothing is left partial.
func TestStageAnalytics_RequiredHelperBarrier(t *testing.T) {
	r := newAnalyticsRig(t, nil)
	entered, release := r.blockingHelper(t)
	activateStage(t, r.o, assistantsTestStageID)
	helper, err := r.o.Attach(context.Background(), AttachRequest{
		OperationID: "op-required", StageID: assistantsTestStageID, ProfileID: "reviewer", Mode: assistants.ModeRequired,
		Instruction: "check", Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	receive(t, entered)
	r.fc.Advance(time.Minute)
	r.o.completeStage(context.Background(), assistantsTestStageID, state.StatusRunning, "main done")
	if got := r.o.currentStatus(assistantsTestStageID); got != state.StatusAwaitingAssistants {
		t.Fatalf("status = %s, want awaiting_assistants", got)
	}
	r.finishedJob(t, r.helperJob(helper), "h1", jobsT0.Add(30*time.Second), jobsT0.Add(90*time.Second), 300, 30)
	r.fc.Advance(time.Minute)
	close(release)

	got := r.hooks.await(t, "a", lifecyclehooks.EventStageFinished, 1).analytics(t)
	wantInt(t, "timing.work_ms", got.Timing.WorkMS, 60_000)
	wantInt(t, "timing.helpers_ms", got.Timing.HelpersMS, 60_000)
	wantInt(t, "timing.total_ms", got.Timing.TotalMS, 120_000)
	wantInt(t, "agent_jobs.measured", got.AgentJobs.Measured, 1)
	if !got.AgentJobs.Complete || got.Usage.Tokens == nil || got.Usage.Tokens.Total != 330 || got.Usage.Coverage != "full" {
		t.Fatalf("snapshot = %+v, want the helper's closed job with its 330 tokens", got)
	}
}

// A stage that ran no agent while the journal was recording is a proven zero, not an unknown.
func TestStageAnalytics_StageWithoutAgentsIsAProvenZero(t *testing.T) {
	r := newAnalyticsRig(t, nil)
	activateStage(t, r.o, assistantsTestStageID)
	r.fc.Advance(2 * time.Second)
	r.trigger(t, assistantsTestStageID, bus.EvComplete)

	got := r.hooks.await(t, "a", lifecyclehooks.EventStageFinished, 1).analytics(t)
	wantInt(t, "timing.total_ms", got.Timing.TotalMS, 2_000)
	wantInt(t, "agent_jobs.count", got.AgentJobs.Count, 0)
	wantInt(t, "agent_jobs.duration_ms", got.AgentJobs.DurationMS, 0)
	wantInt(t, "usage.metered_jobs", got.Usage.MeteredJobs, 0)
	if !got.AgentJobs.Complete || got.Usage.Health != "ok" || got.Usage.Coverage != "full" || got.Usage.Tokens == nil || got.Usage.Tokens.Total != 0 {
		t.Fatalf("snapshot = %+v, want a complete zero", got)
	}
}

// Unreadable stores do not lose the event: it still arrives with the stage time and machine reasons.
func TestStageAnalytics_UnavailableStoresStillDeliverTheEvent(t *testing.T) {
	r := newAnalyticsRig(t, nil)
	broken := t.TempDir()
	if err := os.WriteFile(filepath.Join(broken, agentjobs.FileName), []byte("not a header\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	jobs, err := agentjobs.Open(broken, agentjobs.Header{RecordingStartedAt: jobsT0})
	if err == nil {
		t.Fatal("a journal without a header must open unavailable")
	}
	r.o.opts.Jobs, r.o.opts.Accounting = jobs, nil
	activateStage(t, r.o, assistantsTestStageID)
	r.fc.Advance(time.Minute)
	r.trigger(t, assistantsTestStageID, bus.EvComplete)

	got := r.hooks.await(t, "a", lifecyclehooks.EventStageFinished, 1).analytics(t)
	wantInt(t, "timing.total_ms", got.Timing.TotalMS, 60_000)
	if got.AgentJobs.Count != nil || got.AgentJobs.Complete || got.AgentJobs.Reason != "jobs_store_unavailable" {
		t.Fatalf("agent_jobs = %+v, want unknown with jobs_store_unavailable", got.AgentJobs)
	}
	if got.Usage.Health != "unavailable" || got.Usage.Tokens != nil || got.Usage.MeteredJobs != nil || got.Usage.Reason != "usage_store_unavailable" {
		t.Fatalf("usage = %+v, want unavailable with usage_store_unavailable", got.Usage)
	}
	if got := r.o.currentStatus(assistantsTestStageID); got != state.StatusDone {
		t.Fatalf("status = %s, want done", got)
	}
}

// Hooks get time and tokens whatever accounting shows, and never a price.
func TestStageAnalytics_NoMoneyWhateverTheAccountingSettings(t *testing.T) {
	on, off := true, false
	for _, enabled := range []*bool{nil, &on, &off} {
		for _, showMoney := range []*bool{nil, &on, &off} {
			t.Run(fmtBoolPtr(enabled)+"/"+fmtBoolPtr(showMoney), func(t *testing.T) {
				r := newAnalyticsRig(t, nil)
				r.o.opts.Config.Accounting = config.AccountingConfig{Enabled: enabled, ShowMoney: showMoney}
				activateStage(t, r.o, assistantsTestStageID)
				r.finishedJob(t, r.authorJob(assistantsTestStageID), "a1", jobsT0, jobsT0.Add(time.Minute), 100, 10)
				r.fc.Advance(time.Minute)
				r.trigger(t, assistantsTestStageID, bus.EvComplete)

				d := r.hooks.await(t, "a", lifecyclehooks.EventStageFinished, 1)
				if got := d.analytics(t); got.Usage.Tokens == nil || got.Usage.Tokens.Total != 110 {
					t.Fatalf("tokens = %+v, want 110", got.Usage.Tokens)
				}
				var payload any
				if err := json.Unmarshal(d.raw, &payload); err != nil {
					t.Fatal(err)
				}
				if key := moneyKey(payload); key != "" {
					t.Fatalf("payload carries %q: %s", key, d.raw)
				}
			})
		}
	}
}

func fmtBoolPtr(p *bool) string {
	switch {
	case p == nil:
		return "unset"
	case *p:
		return "on"
	default:
		return "off"
	}
}

// moneyKey finds a key that names money at any depth of a decoded JSON value.
func moneyKey(v any) string {
	switch v := v.(type) {
	case map[string]any:
		for k, child := range v {
			lower := strings.ToLower(k)
			for _, banned := range []string{"cost", "usd", "price", "money", "currency"} {
				if strings.Contains(lower, banned) {
					return k
				}
			}
			if key := moneyKey(child); key != "" {
				return key
			}
		}
	case []any:
		for _, child := range v {
			if key := moneyKey(child); key != "" {
				return key
			}
		}
	default: // scalars have no keys
	}
	return ""
}

// An agent that writes its summary early and keeps running: the stage completes only after its process ended,
// so the snapshot holds the closed job and its final usage.
func TestStageAnalytics_EarlySummaryCarriesTheFinalUsage(t *testing.T) {
	script := writeJobScript(t, `printf '## Summary\ndone\n' > "$AFM_STAGE_DIR/execution_summary.md"`+"\nsleep 0.3")
	rig := newJobsTestRig(t, flow.Stage{ID: "build", Name: "Build", Agents: []flow.AgentType{flow.AgentAuto}, Command: script})
	hooks := newPayloadRecorder(t, nil)
	rig.o.hooks = hooks.disp
	rig.runAsync(t)

	got := hooks.await(t, "a", lifecyclehooks.EventStageFinished, 1).analytics(t)
	wantInt(t, "agent_jobs.count", got.AgentJobs.Count, 1)
	wantInt(t, "agent_jobs.active", got.AgentJobs.Active, 0)
	wantInt(t, "usage.metered_jobs", got.Usage.MeteredJobs, 1)
	if !got.AgentJobs.Complete || !got.Timing.Complete || got.Usage.Tokens == nil || got.Usage.Tokens.Total != 15 || got.Usage.Coverage != "full" {
		t.Fatalf("snapshot = %+v, want the closed job with its 15 tokens", got)
	}
}

// A script stage of a real run reaches its hook as a proven zero.
func TestStageAnalytics_ScriptStageOfARun(t *testing.T) {
	rig := newJobsTestRig(t, flow.Stage{ID: "notes", Name: "Notes", Script: "true"})
	hooks := newPayloadRecorder(t, nil)
	rig.o.hooks = hooks.disp
	rig.runAsync(t)

	got := hooks.await(t, "a", lifecyclehooks.EventStageFinished, 1).analytics(t)
	wantInt(t, "agent_jobs.count", got.AgentJobs.Count, 0)
	if !got.AgentJobs.Complete || !got.Timing.Complete || got.Usage.Tokens == nil || got.Usage.Tokens.Total != 0 {
		t.Fatalf("snapshot = %+v, want a complete zero", got)
	}
}

// Timing is a number only when it is exact: never for a stage that moved on, nor for one whose start was
// not recorded.
func TestAnalyticsTiming_UnknownIsNotZero(t *testing.T) {
	tm := state.StageTiming{Started: true, WorkMs: 5, ApprovalMs: 1, HelpersMs: 2, BackoffMs: 3, SchedulingMs: 4, PausedMs: 6}
	got := analyticsTiming(tm, true)
	if !got.Complete || got.TotalMS != lifecyclehooks.Known[int64](21) || got.WaitingMS != lifecyclehooks.Known[int64](10) {
		t.Fatalf("exact timing = %+v, want total 21 and waiting 10", got)
	}
	for name, c := range map[string]struct {
		tm      state.StageTiming
		current bool
		reason  string
	}{
		"moved on":   {tm: tm, current: false, reason: "stage_advanced_before_snapshot"},
		"unrecorded": {tm: state.StageTiming{Started: true, Incomplete: true}, current: true, reason: "started_before_recording"},
	} {
		got := analyticsTiming(c.tm, c.current)
		if got.Complete || got.Reason != c.reason || got.TotalMS.Valid || got.WorkMS.Valid || got.PausedMS.Valid {
			t.Errorf("%s: timing = %+v, want unknown with %s", name, got, c.reason)
		}
	}
}

// A running helper turn counts as started only by a started record of that very turn.
func TestStartMissing(t *testing.T) {
	started := func(task string, turn int) agentjobs.Record {
		return agentjobs.Record{Type: agentjobs.TypeStarted, JobMeta: agentjobs.JobMeta{TaskID: task, Turn: turn}}
	}
	failed := agentjobs.Record{Type: agentjobs.TypeStartFailed, JobMeta: agentjobs.JobMeta{TaskID: "t1", Turn: 2}}
	cases := map[string]struct {
		records []agentjobs.Record
		running map[string]int
		want    bool
	}{
		"no running turn":          {records: nil, running: map[string]int{}, want: false},
		"turn on the journal":      {records: []agentjobs.Record{started("t1", 2)}, running: map[string]int{"t1": 2}, want: false},
		"only an earlier turn":     {records: []agentjobs.Record{started("t1", 1)}, running: map[string]int{"t1": 2}, want: true},
		"start failed is no start": {records: []agentjobs.Record{failed}, running: map[string]int{"t1": 2}, want: true},
		"two jobs of one turn, other task missing": {
			records: []agentjobs.Record{started("t1", 2), started("t1", 2)},
			running: map[string]int{"t1": 2, "t2": 1}, want: true,
		},
	}
	for name, c := range cases {
		if got := startMissing(agentjobs.Snapshot{Records: c.records}, c.running); got != c.want {
			t.Errorf("%s: startMissing = %v, want %v", name, got, c.want)
		}
	}
}
