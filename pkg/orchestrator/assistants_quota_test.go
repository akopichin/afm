package orchestrator

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/clock"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// Помощник, упёршийся в usage-limit, остаётся в обычном queued с durable quota: тот же task ID, снимок профиля,
// TurnInbox и досягаемость в mesh. Тесты ниже идут на fake-часах: время двигает только Advance.

// quotaProvider — ключ провайдера команды helper-cmd (профили тестового реестра запускают её).
const quotaProvider = "helper-cmd"

func helperLimit(reset time.Time) *executor.UsageLimitError {
	return &executor.UsageLimitError{
		Provider: quotaProvider, ResetAt: reset, Window: "five_hour", Source: state.QuotaSourceStructured,
		Message: "You've hit your limit",
	}
}

// limitOutcome — так RunHelperAgent сообщает о лимите: ошибкой и ProcessErr результата одновременно.
func limitOutcome(l *executor.UsageLimitError) (executor.HelperOutcome, error) {
	return executor.HelperOutcome{ProcessErr: l}, l
}

// scriptedHelper — fake helper-агент: по номеру вызова либо лимит, либо pass. Запоминает промпты.
type scriptedHelper struct {
	calls   atomic.Int32
	mu      sync.Mutex
	prompts []string
	limitAt func(call int32) *executor.UsageLimitError // nil — этот вызов отвечает pass
}

func (h *scriptedHelper) run(_ context.Context, _, _, _, prompt, _, _ string) (executor.HelperOutcome, error) {
	h.mu.Lock()
	h.prompts = append(h.prompts, prompt)
	h.mu.Unlock()
	if l := h.limitAt(h.calls.Add(1)); l != nil {
		return limitOutcome(l)
	}
	return passResultOutcome(), nil
}

func (h *scriptedHelper) promptOf(call int) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if call > len(h.prompts) {
		return ""
	}
	return h.prompts[call-1]
}

// limitFirst — помощник, первый вызов которого упирается в лимит со сбросом reset.
func limitFirst(reset time.Time) *scriptedHelper {
	return &scriptedHelper{limitAt: func(call int32) *executor.UsageLimitError {
		if call == 1 {
			return helperLimit(reset)
		}
		return nil
	}}
}

// newQuotaOrch — newHelperHoldOrch, чей run context отменяется ДО ожидания агентов: таймеры пробуждения живут на
// нём, а fake-часы сами drain-таймаут WaitAgents не сработают, так что без отмены ожидание таймера не кончится.
func newQuotaOrch(t *testing.T) (*Orchestrator, *state.Store, *clock.FakeClock) {
	t.Helper()
	o, store, clk := newHelperHoldOrch(t)
	runCtx, cancel := context.WithCancel(context.Background())
	o.runCtx = runCtx
	t.Cleanup(cancel) // cleanup'ы идут LIFO: раньше WaitAgents из newHelperHoldOrch
	return o, store, clk
}

// waitForQuotaPark ждёт парк: queued с quota, у которого есть until или причина паузы.
func waitForQuotaPark(t *testing.T, store *state.Store, id assistants.TaskID) *state.TaskState {
	t.Helper()
	var got *state.TaskState
	waitUntil(t, "the task parks for a usage limit", func() bool {
		tk := store.Snapshot().Assistants.Tasks[id]
		if tk != nil && tk.Status == assistants.StatusQueued && tk.Quota != nil && (!tk.Quota.Until.IsZero() || tk.Quota.PauseReason != "") {
			got = tk
			return true
		}
		return false
	})
	return got
}

// advanceUntil двигает fake-часы шагами, пока cond не станет истинным. Нужен там, где таймер взводится асинхронно и
// BlockUntil не отличит его от чужих ожиданий: единый большой Advance мог бы пройти раньше регистрации таймера, и тот
// никогда бы не сработал.
func advanceUntil(t *testing.T, clk *clock.FakeClock, step time.Duration, what string, cond func() bool) {
	t.Helper()
	waitUntil(t, what, func() bool {
		if cond() {
			return true
		}
		clk.Advance(step)
		return false
	})
}

func calledExactly(t *testing.T, h *scriptedHelper, want int32, what string) {
	t.Helper()
	waitUntil(t, what, func() bool { return h.calls.Load() >= want })
	neverTrue(t, what+": one more process started", func() bool { return h.calls.Load() > want })
}

func TestHelperQuota_RefusalParksQueuedTaskKeepingIdentityInboxAndMesh(t *testing.T) {
	o, store, clk := newQuotaOrch(t)
	reset := holdNow.Add(2 * time.Minute)
	h := limitFirst(reset)
	o.runHelperAgent = h.run
	msg := "please review the diff"
	res := addTestRunAgent(t, o, "op-add", &msg)
	created := store.Snapshot().Assistants.Tasks[res.TaskID].ProfileSnapshot

	parked := waitForQuotaPark(t, store, res.TaskID)

	if parked.Status != assistants.StatusQueued || parked.Paused() {
		t.Fatalf("task = %s paused=%v, want plain queued", parked.Status, parked.Paused())
	}
	q := parked.Quota
	if q.Provider != quotaProvider || q.Target != state.QuotaTargetHelper || q.Source != state.QuotaSourceStructured || q.Waits != 1 || !q.ResetAt.Equal(reset) {
		t.Fatalf("quota = %+v", q)
	}
	if q.Until.Before(reset.Add(54*time.Second)) || q.Until.After(reset.Add(66*time.Second)) {
		t.Fatalf("until = %s, want reset + 60s margin ±6s (reset %s)", q.Until, reset)
	}
	if !strings.Contains(parked.StatusReason, "usage limit") {
		t.Fatalf("status reason = %q, want it to say why", parked.StatusReason)
	}
	if parked.Turn != 1 || !reflect.DeepEqual(parked.TurnInbox, []agentcomm.MessageID{*res.MessageID}) {
		t.Fatalf("turn = %d inbox = %v, want the parked turn's input kept", parked.Turn, parked.TurnInbox)
	}
	if !reflect.DeepEqual(parked.ProfileSnapshot, created) {
		t.Fatalf("profile snapshot changed: %+v vs %+v", parked.ProfileSnapshot, created)
	}
	if p, ok := resolveParticipant(store.Snapshot(), res.ParticipantID); !ok || !p.Reachable {
		t.Fatalf("participant = %+v, want it reachable while parked", p)
	}
	listed := false
	for _, p := range o.liveRoster(store.Snapshot()) {
		if p.ID == res.ParticipantID {
			listed = p.Reachable
		}
	}
	if !listed {
		t.Fatal("afm agent list must show the parked participant as reachable")
	}

	// Admission never starts it before until — neither the timer, nor a Send/recovery calling maybeScheduleTask.
	clk.BlockUntil(1)
	clk.Advance(time.Minute)
	o.maybeScheduleTask(context.Background(), res.TaskID)
	calledExactly(t, h, 1, "no process before until")
	if tk := store.Snapshot().Assistants.Tasks[res.TaskID]; tk.Status != assistants.StatusQueued {
		t.Fatalf("status before until = %s, want queued", tk.Status)
	}

	clk.Advance(3 * time.Minute) // until is reset (2 min) + the margin (1 min ±6 s) after the start
	done := waitForRunAgentTurn(t, store, res.TaskID, 5*time.Second)
	if done.LastVerdict != assistants.VerdictPass || done.Quota != nil {
		t.Fatalf("after the wake: verdict %q quota %+v, want the answer and the series reset", done.LastVerdict, done.Quota)
	}
	if h.calls.Load() != 2 || !strings.Contains(h.promptOf(2), msg) {
		t.Fatalf("calls = %d, second prompt %q: want the same message handed to the retry", h.calls.Load(), h.promptOf(2))
	}
}

// Зависимый reply_to-wait стадии не заканчивается «nobody left can answer», пока помощник ждёт сброса, а после
// срока помощник отвечает и стадия получает peer_replied.
func TestHelperQuota_StageReplyWaitSurvivesTheParkAndEndsPeerReplied(t *testing.T) {
	runner := &scriptedStageRunner{}
	o, store := newUnreadTestOrchestrator(t, flow.AgentAuto, runner)
	clk := clock.NewFake(holdNow)
	o.clock = clk
	h := limitFirst(holdNow.Add(2 * time.Minute))
	o.runHelperAgent = h.run
	res, err := o.AddRunAgent(context.Background(), AddRunAgentRequest{OperationID: "op-qa", ProfileID: "qa", Title: "QA", Actor: state.Actor{Kind: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	th, err := o.OpenThread(context.Background(), OpenThreadRequest{
		OperationID: "op-qa-thread", Members: []agentcomm.ParticipantID{unreadTestStage, res.ParticipantID}, Scope: agentcomm.ScopeStage,
	})
	if err != nil {
		t.Fatal(err)
	}
	var waits []agentcomm.WaitID
	runner.turn = func(n int32) error {
		if n != 1 {
			return nil
		}
		waits = askReviewers(t, o, unreadTestStage, th.ID, []agentcomm.ParticipantID{res.ParticipantID})
		go func() {
			waitUntil(t, "the helper parks", func() bool {
				tk := store.Snapshot().Assistants.Tasks[res.TaskID]
				return tk != nil && tk.Status == assistants.StatusQueued && tk.Quota != nil && !tk.Quota.Until.IsZero()
			})
			waitUntil(t, "the stage parks", func() bool { return store.Get(unreadTestStageID) == state.StatusAwaitingPeer })
			if w := store.Snapshot().Assistants.Mesh.Waits[waits[0]]; w.Status != agentcomm.WaitPending {
				t.Errorf("wait while the helper is parked = %s %q, want pending", w.Status, w.Reason)
			}
			advanceUntil(t, clk, time.Minute, "the helper wakes", func() bool { return h.calls.Load() >= 2 })
		}()
		return nil
	}

	runToEnd(t, o)

	if got := store.Get(unreadTestStageID); got != state.StatusDone {
		t.Fatalf("stage status = %s, want done", got)
	}
	if w := store.Snapshot().Assistants.Mesh.Waits[waits[0]]; w.Status == agentcomm.WaitCancelled || strings.Contains(w.Reason, "nobody left") {
		t.Fatalf("wait = %s %q, want it answered", w.Status, w.Reason)
	}
	history, err := store.History()
	if err != nil {
		t.Fatal(err)
	}
	replied := false
	for _, tr := range history {
		replied = replied || (tr.StageID == unreadTestStageID && tr.Event == string(bus.EvPeerReplied))
	}
	if !replied {
		t.Fatal("the stage must continue through peer_replied")
	}
	if h.calls.Load() != 2 || runner.turns.Load() != 2 {
		t.Fatalf("helper calls = %d, stage turns = %d, want 2 and 2", h.calls.Load(), runner.turns.Load())
	}
}

// restartHelperOrch — перезапущенный процесс: свежий Store/Orchestrator на том же run dir, с часами clk.
func restartHelperOrch(t *testing.T, o *Orchestrator, clk *clock.FakeClock) (*Orchestrator, *state.Store) {
	t.Helper()
	if err := o.opts.Store.Close(); err != nil {
		t.Fatal(err)
	}
	store2, err := state.Open(o.opts.RunDir, []string{assistantsTestStageID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store2.Close() })
	o2 := reopenAssistantsOrchestrator(o, store2)
	o2.clock = clk
	runCtx, cancel := context.WithCancel(context.Background())
	o2.runCtx = runCtx
	t.Cleanup(func() { cancel(); o2.concurrency.WaitAgents() })
	return o2, store2
}

func TestHelperQuota_RestartWaitsTheRemainderThenRunsExactlyOnce(t *testing.T) {
	o, store, _ := newQuotaOrch(t)
	activateStage(t, o, assistantsTestStageID)
	o.runHelperAgent = limitFirst(holdNow.Add(2 * time.Minute)).run
	id := attachTestTask(context.Background(), t, o, "op-1")
	until := waitForQuotaPark(t, store, id).Quota.Until

	clk2 := clock.NewFake(holdNow)
	o2, store2 := restartHelperOrch(t, o, clk2)
	h2 := &scriptedHelper{limitAt: func(int32) *executor.UsageLimitError { return nil }}
	o2.runHelperAgent = h2.run

	o2.recoverAssistants(context.Background())

	if tk := store2.Snapshot().Assistants.Tasks[id]; tk.Status != assistants.StatusQueued || !tk.Quota.Until.Equal(until) {
		t.Fatalf("after the restart: %s quota %+v, want the same park", tk.Status, tk.Quota)
	}
	clk2.BlockUntil(1)
	clk2.Advance(until.Sub(holdNow) - 30*time.Second)
	calledExactly(t, h2, 0, "no process before the remainder passed")
	clk2.Advance(31 * time.Second)
	waitForTaskStatus(t, store2, id, assistants.StatusCompleted, 5*time.Second)
	calledExactly(t, h2, 1, "exactly one process after until")
}

func TestHelperQuota_RestartWithExpiredUntilRunsExactlyOneProcess(t *testing.T) {
	o, store, _ := newQuotaOrch(t)
	activateStage(t, o, assistantsTestStageID)
	o.runHelperAgent = limitFirst(holdNow.Add(2 * time.Minute)).run
	id := attachTestTask(context.Background(), t, o, "op-1")
	waitForQuotaPark(t, store, id)

	o2, store2 := restartHelperOrch(t, o, clock.NewFake(holdNow.Add(time.Hour)))
	h2 := &scriptedHelper{limitAt: func(int32) *executor.UsageLimitError { return nil }}
	o2.runHelperAgent = h2.run

	o2.recoverAssistants(context.Background())

	waitForTaskStatus(t, store2, id, assistants.StatusCompleted, 5*time.Second)
	calledExactly(t, h2, 1, "exactly one process for an expired until")
}

// Pause, затем Continue даёт ровно одну ручную пробу в обход hold'а провайдера; повторный отказ ставит новый
// until, и устаревший таймер прежнего парка его не трогает; hold не считается отказом (серия не растёт).
func TestHelperQuota_PauseThenContinueIsOneManualProbeAndStaleTimerIsHarmless(t *testing.T) {
	o, store, clk := newQuotaOrch(t)
	activateStage(t, o, assistantsTestStageID)
	h := &scriptedHelper{limitAt: func(call int32) *executor.UsageLimitError {
		switch call {
		case 1:
			return helperLimit(holdNow.Add(10 * time.Minute))
		case 2:
			return helperLimit(holdNow.Add(30 * time.Minute))
		}
		return nil
	}}
	o.runHelperAgent = h.run
	id := attachTestTask(context.Background(), t, o, "op-1")
	first := waitForQuotaPark(t, store, id).Quota.Until
	holdEnd := holdNow.Add(3 * time.Hour)
	parkStageForTest(t, o, "other", holdHelperPark(holdEnd)) // another agent of the provider holds it for hours

	if err := o.PauseTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if err := o.ContinueTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	calledExactly(t, h, 2, "the manual probe bypasses the hold once")
	var second time.Time
	waitUntil(t, "the refused probe parks again", func() bool {
		q := store.Snapshot().Assistants.Tasks[id].Quota
		if q == nil || !q.Until.After(first) || q.Waits != 1 {
			return false
		}
		second = q.Until
		return true
	})

	clk.BlockUntil(2)
	clk.Advance(first.Sub(holdNow) + time.Second) // the timer of the first, superseded park
	calledExactly(t, h, 2, "the stale timer starts nothing")
	if q := store.Snapshot().Assistants.Tasks[id].Quota; !q.Until.Equal(second) || q.Waits != 1 {
		t.Fatalf("quota after the stale timer = %+v, want the newer park untouched", q)
	}

	clk.Advance(second.Sub(first)) // the newer park ends, but the provider is still held: the probe was spent
	calledExactly(t, h, 2, "the spent probe does not bypass the hold again")
	if q := store.Snapshot().Assistants.Tasks[id].Quota; q.Waits != 1 {
		t.Fatalf("waits = %d after a provider hold, want 1: a hold is not a refusal", q.Waits)
	}

	if _, ok := o.Trigger("other", bus.EvPause, bus.GuardCtx{}, "release the hold"); !ok {
		t.Fatal("pause failed")
	}
	advanceUntil(t, clk, time.Hour, "the hold ended", func() bool { return h.calls.Load() >= 3 })
	waitForTaskStatus(t, store, id, assistants.StatusCompleted, 5*time.Second)
	calledExactly(t, h, 3, "one process once the hold ended")
}

// PauseTask и CancelTask, сделанные до срока, выигрывают у пробуждения.
func TestHelperQuota_PauseOrCancelBeforeTheWakeWins(t *testing.T) {
	for _, action := range []string{"pause", "cancel"} {
		t.Run(action, func(t *testing.T) {
			o, store, clk := newQuotaOrch(t)
			activateStage(t, o, assistantsTestStageID)
			h := limitFirst(holdNow.Add(2 * time.Minute))
			o.runHelperAgent = h.run
			id := attachTestTask(context.Background(), t, o, "op-1")
			waitForQuotaPark(t, store, id)
			clk.BlockUntil(1)

			var err error
			if action == "pause" {
				err = o.PauseTask(context.Background(), id)
			} else {
				err = o.CancelTask(context.Background(), id)
			}
			if err != nil {
				t.Fatal(err)
			}
			clk.Advance(10 * time.Minute)

			calledExactly(t, h, 1, "the wake after "+action)
			tk := store.Snapshot().Assistants.Tasks[id]
			if action == "pause" && (tk.Status != assistants.StatusQueued || !tk.Paused()) {
				t.Fatalf("task = %s paused=%v, want it held", tk.Status, tk.Paused())
			}
			if action == "cancel" && tk.Status != assistants.StatusCancelled {
				t.Fatalf("task = %s, want cancelled", tk.Status)
			}
		})
	}
}

// Лимит, который нельзя переждать, оставляет приостановленную задачу с точной причиной; ContinueTask делает
// ровно одну пробу.
func TestHelperQuota_UnwaitableLimitPausesWithReasonAndContinueProbesOnce(t *testing.T) {
	cases := []struct {
		name   string
		reason string
		limit  func(call int32) *executor.UsageLimitError
		maxWts int
	}{
		{"no reset", state.QuotaPauseNoReset, func(call int32) *executor.UsageLimitError {
			if call == 1 {
				return &executor.UsageLimitError{Provider: quotaProvider, NoReset: true, Source: state.QuotaSourceText, Message: "insufficient_quota"}
			}
			return nil
		}, 4},
		{"reset beyond max_wait", state.QuotaPauseExceedsMaxWait, func(call int32) *executor.UsageLimitError {
			if call == 1 {
				return helperLimit(holdNow.Add(13 * time.Hour))
			}
			return nil
		}, 4},
		{"too many refusals", state.QuotaPauseMaxWaits, func(call int32) *executor.UsageLimitError {
			if call <= 2 {
				return helperLimit(holdNow.Add(time.Minute))
			}
			return nil
		}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o, store, clk := newQuotaOrch(t)
			maxWaits := tc.maxWts
			o.opts.Config.UsageLimit.MaxWaits = &maxWaits
			activateStage(t, o, assistantsTestStageID)
			h := &scriptedHelper{limitAt: tc.limit}
			o.runHelperAgent = h.run
			id := attachTestTask(context.Background(), t, o, "op-1")

			if tc.reason == state.QuotaPauseMaxWaits {
				waitForQuotaPark(t, store, id)
				clk.BlockUntil(1)
				clk.Advance(5 * time.Minute) // the first wait ends: the second refusal exceeds max_waits
				waitUntil(t, "the second refusal", func() bool { return h.calls.Load() == 2 })
			}
			tk := waitForQuotaPark(t, store, id)
			waitUntil(t, "the pause is recorded", func() bool { return store.Snapshot().Assistants.Tasks[id].Paused() })

			if tk.Quota.PauseReason != tc.reason || !strings.Contains(tk.StatusReason, tc.reason) {
				t.Fatalf("quota %+v reason %q, want the pause reason %q", tk.Quota, tk.StatusReason, tc.reason)
			}
			if !tk.Quota.Until.IsZero() {
				t.Fatalf("until = %s, a limit that cannot be waited out has none", tk.Quota.Until)
			}
			parkedCalls := h.calls.Load()
			clk.Advance(48 * time.Hour)
			calledExactly(t, h, parkedCalls, "a paused park never wakes by itself")

			if err := o.ContinueTask(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			waitForTaskStatus(t, store, id, assistants.StatusCompleted, 5*time.Second)
			calledExactly(t, h, parkedCalls+1, "Continue runs exactly one probe")
		})
	}
}

// Дашборд предлагает Continue именно для queued-помощника с quota.pause_reason (парк с ручным hold). Повторный Pause
// ему безвреден, а Continue запускает ровно одну пробу, даже когда другой помощник провайдера держит его до until.
func TestHelperQuota_ContinueOnHeldParkProbesDespiteProviderHoldAndPauseIsHarmless(t *testing.T) {
	o, store, _ := newQuotaOrch(t)
	activateStage(t, o, assistantsTestStageID)
	h := &scriptedHelper{limitAt: func(call int32) *executor.UsageLimitError {
		switch call {
		case 1: // the held helper: its reset is beyond max_wait, so the park carries a pause reason
			return helperLimit(holdNow.Add(13 * time.Hour))
		case 2: // another helper of the provider: an ordinary park that holds the provider until its until
			return helperLimit(holdNow.Add(2 * time.Hour))
		}
		return nil
	}}
	o.runHelperAgent = h.run
	held := attachTestTask(context.Background(), t, o, "op-1")
	waitForQuotaPark(t, store, held)
	waitUntil(t, "the pause is recorded", func() bool { return store.Snapshot().Assistants.Tasks[held].Paused() })
	other := attachTestTask(context.Background(), t, o, "op-2")
	waitForQuotaPark(t, store, other)
	if o.admitProvider(quotaProvider, "stage") == nil {
		t.Fatal("the second helper's park must hold the provider")
	}

	if err := o.PauseTask(context.Background(), held); err != nil {
		t.Fatal(err)
	}
	if tk := store.Snapshot().Assistants.Tasks[held]; tk.Status != assistants.StatusQueued || !tk.Paused() || tk.Quota.PauseReason != state.QuotaPauseExceedsMaxWait {
		t.Fatalf("a second Pause changed the held park: %s paused=%v quota %+v", tk.Status, tk.Paused(), tk.Quota)
	}
	calledExactly(t, h, 2, "a Pause of a held park starts nothing")

	if err := o.ContinueTask(context.Background(), held); err != nil {
		t.Fatal(err)
	}
	waitForTaskStatus(t, store, held, assistants.StatusCompleted, 5*time.Second)
	calledExactly(t, h, 3, "Continue runs exactly one probe through the other helper's provider hold")
}

// Серию отказов обрывает ход, дочитавший входящие; без него серия идёт дальше. Здесь max_waits=1: второй отказ
// подряд паузит, а отказ после успешного хода — первый в новой серии.
func TestHelperQuota_ConsumedTurnResetsTheSeries(t *testing.T) {
	o, store, clk := newQuotaOrch(t)
	one := 1
	o.opts.Config.UsageLimit.MaxWaits = &one
	h := &scriptedHelper{limitAt: func(call int32) *executor.UsageLimitError {
		if call == 1 || call == 3 {
			return helperLimit(holdNow.Add(time.Minute))
		}
		return nil
	}}
	o.runHelperAgent = h.run
	msg := "first"
	res := addTestRunAgent(t, o, "op-add", &msg)
	waitForQuotaPark(t, store, res.TaskID)
	clk.BlockUntil(1)
	clk.Advance(5 * time.Minute)
	waitForRunAgentTurn(t, store, res.TaskID, 5*time.Second) // call 2 answered: the input is consumed

	sendAs(t, o, "op-again", res.ThreadID, meshUserParticipantID, res.ParticipantID, "second", nil, false)
	waitUntil(t, "the third call refuses", func() bool { return h.calls.Load() == 3 })
	tk := waitForQuotaPark(t, store, res.TaskID)

	if tk.Quota.Waits != 1 || tk.Quota.PauseReason != "" || tk.Paused() {
		t.Fatalf("quota %+v paused=%v, want the first refusal of a new series", tk.Quota, tk.Paused())
	}
}

// queued с quota — отложенный ход: ран не заканчивается под ним ни до until, ни при паузе, а idle-агент из
// них исключён только без парка.
func TestHelperQuota_QueuedTurnDueCountsAParkedTask(t *testing.T) {
	parked := &state.TaskState{Task: assistants.Task{
		ID: "t1", Scope: assistants.ScopeRun, Status: assistants.StatusQueued, StatusReason: "usage limit: waiting until 2026-10-24T00:00:00Z",
	}, Quota: &state.QuotaWait{Provider: quotaProvider, Until: holdNow.Add(time.Hour)}}
	rs := state.RunState{Assistants: state.AssistantsState{Tasks: map[assistants.TaskID]*state.TaskState{"t1": parked}}}

	if !queuedTurnDue(rs) {
		t.Fatal("a task waiting for its until still has a turn due: the run must stay open")
	}
	paused := *parked
	paused.Quota = &state.QuotaWait{Provider: quotaProvider, PauseReason: state.QuotaPauseNoReset}
	paused.Holds = map[string]state.Hold{holdOwnerManual: {}}
	rs.Assistants.Tasks["t1"] = &paused
	if !queuedTurnDue(rs) {
		t.Fatal("a quota-paused task keeps the run open until the human continues or cancels it")
	}
	idle := &state.TaskState{Task: assistants.Task{ID: "t1", Scope: assistants.ScopeRun, Status: assistants.StatusQueued, StatusReason: runAgentIdleReason}}
	rs.Assistants.Tasks["t1"] = idle
	if queuedTurnDue(rs) {
		t.Fatal("an idle run agent never holds the run open")
	}
}

func TestTaskQuota_AccessorReturnsACopyOfTheParkedQuota(t *testing.T) {
	o, store, _ := newQuotaOrch(t)
	activateStage(t, o, assistantsTestStageID)
	o.runHelperAgent = limitFirst(holdNow.Add(2 * time.Minute)).run
	id := attachTestTask(context.Background(), t, o, "op-1")
	parked := waitForQuotaPark(t, store, id)

	got := o.TaskQuota(id)
	if got == nil || !got.Until.Equal(parked.Quota.Until) || got == parked.Quota {
		t.Fatalf("TaskQuota = %+v, want a copy of %+v", got, parked.Quota)
	}
	if o.TaskQuota("t-unknown") != nil {
		t.Fatal("an unknown task has no quota")
	}
}

// Парк помощника держит провайдера так же, как парк стадии: другая стадия и другой помощник той же команды не
// стартуют до его until (тот же until, без своего джиттера), агент другого провайдера стартует, а собственный парк
// не держит самого помощника.
func TestHelperQuota_ParkHoldsTheProviderForStagesAndOtherHelpers(t *testing.T) {
	o, store, _ := newQuotaOrch(t)
	activateStage(t, o, assistantsTestStageID)
	h := limitFirst(holdNow.Add(2 * time.Hour))
	o.runHelperAgent = h.run
	first := attachTestTask(context.Background(), t, o, "op-1")
	parked := waitForQuotaPark(t, store, first)
	until := parked.Quota.Until

	hold := o.admitProvider(quotaProvider, "other")
	if hold == nil {
		t.Fatal("a stage of the provider is admitted while a helper of it is parked")
	}
	if hold.Source != state.QuotaSourceProviderHold || hold.Provider != quotaProvider || !hold.ResetAt.Equal(until) {
		t.Errorf("hold = %+v, want provider_hold on %s until exactly the helper's park %s", hold, quotaProvider, until)
	}
	if got := o.admitProvider("codex", "other"); got != nil {
		t.Errorf("a stage of another provider is held by the helper's park: %+v", got)
	}
	if got := o.taskProviderHold(parked.Task); got != nil {
		t.Errorf("the parked helper is held by its own park: %+v", got)
	}

	second := attachTestTask(context.Background(), t, o, "op-2")
	calledExactly(t, h, 1, "the second helper of the provider waits for the park")
	if tk := store.Snapshot().Assistants.Tasks[second]; tk.Status != assistants.StatusQueued || tk.Quota != nil {
		t.Errorf("second helper = %s quota %+v, want queued without its own park (a hold is not a refusal)", tk.Status, tk.Quota)
	}
	if got := o.taskProviderHold(store.Snapshot().Assistants.Tasks[second].Task); got == nil || !got.ResetAt.Equal(until) {
		t.Errorf("second helper hold = %+v, want until %s", got, until)
	}
}

// Истёкший парк помощника и парк под паузой (ручной hold задачи) провайдера не держат.
func TestHelperQuota_ExpiredOrPausedParkDoesNotHoldTheProvider(t *testing.T) {
	o, store, clk := newQuotaOrch(t)
	activateStage(t, o, assistantsTestStageID)
	h := limitFirst(holdNow.Add(2 * time.Hour))
	o.runHelperAgent = h.run
	id := attachTestTask(context.Background(), t, o, "op-1")
	until := waitForQuotaPark(t, store, id).Quota.Until

	if o.admitProvider(quotaProvider, "other") == nil {
		t.Fatal("the active park does not hold")
	}
	if err := o.PauseTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if got := o.admitProvider(quotaProvider, "other"); got != nil {
		t.Errorf("a paused helper holds the provider: %+v", got)
	}
	if err := o.ContinueTask(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	calledExactly(t, h, 2, "the manual probe of the continued helper")
	waitForTaskStatus(t, store, id, assistants.StatusCompleted, 5*time.Second)

	// The probe was answered: the park is gone, and a later park expires with its until.
	again := &scriptedHelper{limitAt: func(int32) *executor.UsageLimitError { return helperLimit(clk.Now().Add(time.Hour)) }}
	o.runHelperAgent = again.run
	second := attachTestTask(context.Background(), t, o, "op-2")
	parked := waitForQuotaPark(t, store, second)
	if o.admitProvider(quotaProvider, "other") == nil {
		t.Fatal("the second park does not hold")
	}
	clk.Advance(parked.Quota.Until.Sub(clk.Now()))
	if got := o.admitProvider(quotaProvider, "other"); got != nil {
		t.Errorf("a helper park at its until still holds: %+v (until %s)", got, until)
	}
}
