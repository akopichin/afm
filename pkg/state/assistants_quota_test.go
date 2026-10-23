package state

import (
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
)

// quotaTaskStore открывает журнал с одной задачей t1 и даёт короткие записи её статусов и holds.
type quotaTaskStore struct {
	t     *testing.T
	dir   string
	store *Store
}

func newQuotaTaskStore(t *testing.T) *quotaTaskStore {
	t.Helper()
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	created := taskCreatedControl("op-1", "t1", "a", nil)
	created.Payload = mustMarshal(t, TaskCreatedPayload{ProfileID: "reviewer"})
	if err := store.AppendControl(created); err != nil {
		t.Fatal(err)
	}
	return &quotaTaskStore{t: t, dir: dir, store: store}
}

func (q *quotaTaskStore) status(p TaskStatusChangedPayload) {
	q.t.Helper()
	c := &Control{Kind: "task_status_changed", SchemaVersion: ControlSchemaVersion, Target: Target{TaskID: "t1"}, Payload: mustMarshal(q.t, p)}
	if err := q.store.AppendControl(c); err != nil {
		q.t.Fatalf("AppendControl(%+v): %v", p, err)
	}
}

func (q *quotaTaskStore) hold(kind, owner string) {
	q.t.Helper()
	c := &Control{Kind: kind, SchemaVersion: ControlSchemaVersion, Target: Target{TaskID: "t1"}, Payload: mustMarshal(q.t, TaskHoldPayload{Owner: owner})}
	if err := q.store.AppendControl(c); err != nil {
		q.t.Fatalf("AppendControl(%s): %v", kind, err)
	}
}

func (q *quotaTaskStore) task() *TaskState { return q.store.Snapshot().Assistants.Tasks["t1"] }

// replayed — проекция задачи, восстановленная из одного лишь журнала.
func (q *quotaTaskStore) replayed() *TaskState {
	q.t.Helper()
	if err := q.store.Close(); err != nil {
		q.t.Fatal(err)
	}
	rs, err := LoadRunState(q.dir)
	if err != nil {
		q.t.Fatalf("LoadRunState: %v", err)
	}
	return rs.Assistants.Tasks["t1"]
}

var quotaUntil = time.Date(2026, 10, 23, 23, 51, 0, 0, time.UTC)

func waitingQuota() *QuotaWait {
	return &QuotaWait{Provider: "claude", Window: "five_hour", Source: QuotaSourceStructured,
		Target: QuotaTargetHelper, Waits: 2, ResetAt: quotaUntil.Add(-time.Minute), Until: quotaUntil}
}

// Парк помощника — обычный queued с quota: статус тот же, а until/provider/причина живут в проекции и
// переживают replay; сохранённые ходом входящие остаются на месте.
func TestTaskQuota_ParkProjectsLiveAndOnReplay(t *testing.T) {
	q := newQuotaTaskStore(t)
	q.status(TaskStatusChangedPayload{Status: "running"})
	q.status(TaskStatusChangedPayload{Status: "running", Turn: 1, TurnInbox: []string{"m1"}})
	q.status(TaskStatusChangedPayload{Status: "queued", Reason: "usage limit", Quota: waitingQuota()})

	check := func(task *TaskState) {
		t.Helper()
		if task.Status != assistants.StatusQueued || task.Quota == nil {
			t.Fatalf("task = %s quota=%+v, want queued with the quota", task.Status, task.Quota)
		}
		if got := task.Quota; got.Provider != "claude" || !got.Until.Equal(quotaUntil) || got.Waits != 2 || got.Source != QuotaSourceStructured {
			t.Fatalf("quota = %+v", got)
		}
		if len(task.TurnInbox) != 1 || task.TurnInbox[0] != "m1" {
			t.Fatalf("TurnInbox = %v, want the parked turn's input kept", task.TurnInbox)
		}
	}
	check(q.task())
	check(q.replayed())
}

// Серия отказов переживает пробуждение: запись без quota её переносит, а старт хода гасит только «парк»
// (until/причину/пробу) — Waits и Target остаются для следующего отказа. Завершённый ход (consumed) и
// терминал серию обрывают.
func TestTaskQuota_CarriedThroughWakeAndResetByProgress(t *testing.T) {
	q := newQuotaTaskStore(t)
	q.status(TaskStatusChangedPayload{Status: "queued", Quota: waitingQuota()})
	q.status(TaskStatusChangedPayload{Status: "running"}) // wake: queued → running without a turn
	if got := q.task().Quota; got == nil || got.Waits != 2 || !got.Until.Equal(quotaUntil) {
		t.Fatalf("quota after the wake = %+v, want it carried", got)
	}
	q.status(TaskStatusChangedPayload{Status: "running", Turn: 1})
	got := q.task().Quota
	if got == nil || got.Waits != 2 || got.Target != QuotaTargetHelper || !got.Until.IsZero() || got.PauseReason != "" || got.ManualProbe {
		t.Fatalf("quota after the turn start = %+v, want the series kept and the park cleared", got)
	}
	if replayed := q.replayed().Quota; replayed == nil || replayed.Waits != 2 || !replayed.Until.IsZero() {
		t.Fatalf("replayed quota = %+v", replayed)
	}

	q2 := newQuotaTaskStore(t)
	q2.status(TaskStatusChangedPayload{Status: "queued", Quota: waitingQuota()})
	q2.status(TaskStatusChangedPayload{Status: "running", Turn: 1})
	q2.status(TaskStatusChangedPayload{Status: "queued", Turn: 1, Reason: "idle"}) // the turn consumed its input
	if got := q2.task().Quota; got != nil {
		t.Fatalf("quota after a consumed turn = %+v, want the series reset", got)
	}

	q3 := newQuotaTaskStore(t)
	q3.status(TaskStatusChangedPayload{Status: "queued", Quota: waitingQuota()})
	q3.status(TaskStatusChangedPayload{Status: "cancelled"})
	if got := q3.task().Quota; got != nil {
		t.Fatalf("quota of a cancelled task = %+v, want none", got)
	}
}

// Continue (снятие ручного hold'а) паркованной задачи записывает одноразовую ручную пробу: новая серия, без
// причины паузы. Проба — часть той же записи, поэтому переживает падение между Continue и стартом, а старт
// хода её тратит. Hold задачи без парка ничего не получает.
func TestTaskQuota_ContinueOfParkedTaskRecordsOneManualProbe(t *testing.T) {
	q := newQuotaTaskStore(t)
	paused := waitingQuota()
	paused.Until, paused.PauseReason = time.Time{}, QuotaPauseNoReset
	q.status(TaskStatusChangedPayload{Status: "queued", Quota: paused})
	q.hold("task_hold_set", "manual")
	q.hold("task_hold_cleared", "manual")

	check := func(task *TaskState) {
		t.Helper()
		if got := task.Quota; got == nil || !got.ManualProbe || got.Waits != 0 || got.PauseReason != "" {
			t.Fatalf("quota after Continue = %+v, want a manual probe in a new series", got)
		}
	}
	check(q.task())
	q.status(TaskStatusChangedPayload{Status: "running", Turn: 1})
	if got := q.task().Quota; got == nil || got.ManualProbe {
		t.Fatalf("quota after the turn start = %+v, want the probe spent", got)
	}
	// replay of the Continue alone
	q2 := newQuotaTaskStore(t)
	q2.status(TaskStatusChangedPayload{Status: "queued", Quota: paused})
	q2.hold("task_hold_set", "manual")
	q2.hold("task_hold_cleared", "manual")
	check(q2.replayed())

	idle := newQuotaTaskStore(t)
	idle.hold("task_hold_set", "manual")
	idle.hold("task_hold_cleared", "manual")
	if got := idle.task().Quota; got != nil {
		t.Fatalf("quota of a never-parked task = %+v, want none", got)
	}

	// an expired park's leftovers (the turn already started) are not a park a Continue can probe
	stale := newQuotaTaskStore(t)
	stale.status(TaskStatusChangedPayload{Status: "queued", Quota: waitingQuota()})
	stale.status(TaskStatusChangedPayload{Status: "running", Turn: 1})
	stale.status(TaskStatusChangedPayload{Status: "queued", Reason: "re-queued: paused while running"})
	stale.hold("task_hold_set", "manual")
	stale.hold("task_hold_cleared", "manual")
	if got := stale.task().Quota; got == nil || got.ManualProbe {
		t.Fatalf("quota after Continue of a task whose park already ended = %+v, want no probe", got)
	}
}

// queued с quota — работа, а не вопрос к человеку: required-гейт открыт и ждёт машину.
func TestTaskQuota_QueuedWithQuotaIsWorkNotAHumanDecision(t *testing.T) {
	f := newBarrierFixture()
	f.addTask(assistants.Task{ID: "a", RootStageID: "s1", Mode: assistants.ModeRequired, Status: assistants.StatusQueued})
	f.as.Tasks["a"].Quota = waitingQuota()
	rs := &RunState{Stages: map[string]StageState{"s1": {Status: StatusAwaitingAssistants}}, Assistants: f.as}

	if f.as.taskGateClosed("a") {
		t.Fatal("a queued task must not close its gate")
	}
	if blocker, ok := f.as.RequiredBlocker("s1"); !ok || blocker.ID != "a" {
		t.Fatalf("blocker = %+v, %v; want the parked task still blocking", blocker, ok)
	}
	if f.as.NeedsHumanDecision("s1") {
		t.Error("a task waiting for a quota reset is machine work, not a human decision")
	}
	if isIdle(rs) {
		t.Error("the stage waiting on a parked helper is not user-idle")
	}

	// The same holds for a park that cannot be waited out: a paused (held) queued task is no human *answer*
	// either, exactly like a task paused without a limit — Continue/Cancel are available on it.
	paused := waitingQuota()
	paused.Until, paused.PauseReason = time.Time{}, QuotaPauseMaxWaits
	f.as.Tasks["a"].Quota = paused
	if f.as.NeedsHumanDecision("s1") {
		t.Error("a quota-paused queued task is treated like any other held queued task")
	}
}
