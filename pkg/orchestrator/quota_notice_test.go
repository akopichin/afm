package orchestrator_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/clock"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/lifecyclehooks"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// Видимость usage-limit ожидания: notices (live и notices.jsonl), lifecycle-хуки. Критический until остаётся в
// журнале FSM; notice — best-effort и без секретов.

// Ключи data, которые вправе нести quota-notice стадии (контракт ленты/дашборда).
var quotaNoticeKeys = map[string]bool{
	"stage_id": true, "provider": true, "window": true, "source": true,
	"until": true, "reset_at": true, "pause_reason": true, "message": true,
}

// noticeRecord — одна строка notices.jsonl.
type noticeRecord struct {
	Type    string         `json:"type"`
	StageID string         `json:"stage_id"`
	Data    map[string]any `json:"data"`
}

// readNotices возвращает строки notices.jsonl данного типа (файла нет — пусто: уведомлений не было).
func readNotices(t *testing.T, runDir string, types ...string) []noticeRecord {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(runDir, "notices.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []noticeRecord
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var rec noticeRecord
		if line == "" || json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		for _, typ := range types {
			if rec.Type == typ {
				out = append(out, rec)
			}
		}
	}
	return out
}

// hookRecorder — lifecycle-dispatcher, собирающий payload'ы без запуска процессов.
type hookRecorder struct {
	mu   sync.Mutex
	seen []lifecyclehooks.Payload
	disp *lifecyclehooks.Dispatcher
}

func newHookRecorder(t *testing.T) *hookRecorder {
	t.Helper()
	h := &hookRecorder{}
	h.disp = lifecyclehooks.New(lifecyclehooks.DispatcherOptions{
		Config: lifecyclehooks.DispatcherConfig{RunID: "run-1"},
		Hooks: []lifecyclehooks.RegisteredHook{{
			Hook: lifecyclehooks.Hook{ID: "cap", Events: lifecyclehooks.EventSelector{All: true}, Command: "true"},
		}},
		RunFunc: func(_ context.Context, _ lifecyclehooks.Hook, _ lifecyclehooks.DispatcherConfig, p lifecyclehooks.Payload, _ string) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.seen = append(h.seen, p)
			return nil
		},
	})
	h.disp.Start()
	t.Cleanup(h.disp.Stop)
	return h
}

// events возвращает собранные payload'ы данного события после доставки очереди.
func (h *hookRecorder) events(t *testing.T, ev lifecyclehooks.EventType) []lifecyclehooks.Payload {
	t.Helper()
	if !h.disp.Flush(5 * time.Second) {
		t.Fatal("lifecycle Flush timed out")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []lifecyclehooks.Payload
	for _, p := range h.seen {
		if p.Event == ev {
			out = append(out, p)
		}
	}
	return out
}

// noticeRig — quotaRig с подписчиком UI-шины и lifecycle-хуками, подключёнными до Run.
type noticeRig struct {
	*quotaRig
	hooks *hookRecorder
	ui    <-chan bus.Event
}

func startNoticeRig(t *testing.T, runDir string, runner executor.Runner, clk *clock.FakeClock, seed bool) *noticeRig {
	t.Helper()
	hooks := newHookRecorder(t)
	var ui <-chan bus.Event
	rig := startQuotaRig(t, runDir, quotaStages(), quotaConfig(), runner, clk, seed, func(o *orchestrator.Orchestrator) {
		orchestrator.SetLifecycleHooksForTest(o, hooks.disp)
		_, ui = o.UIBus().Subscribe(256)
	})
	return &noticeRig{quotaRig: rig, hooks: hooks, ui: ui}
}

// liveNotices вычитывает уже опубликованные в UI-шину события данных типов.
func (r *noticeRig) liveNotices(types ...bus.EventType) []bus.Event {
	var out []bus.Event
	for {
		select {
		case ev := <-r.ui:
			for _, typ := range types {
				if ev.Type == typ {
					out = append(out, ev)
				}
			}
		default:
			return out
		}
	}
}

func refuseOnce(reset time.Time) *quotaRunner {
	return &quotaRunner{script: func(call int, _ string) error {
		if call == 1 {
			l := limitRefusal(reset)
			l.Message = "You've hit your limit key=sk-secret-token-123" //nolint:gosec // test fixture, not a real secret
			return l
		}
		return nil
	}}
}

func TestQuotaNotice_ParkPublishesQuotaWaitLiveAndToReplayLog(t *testing.T) {
	clk := clock.NewFake(quotaStart)
	reset := quotaStart.Add(2 * time.Minute)
	rig := startNoticeRig(t, t.TempDir(), refuseOnce(reset), clk, true)
	q := rig.waitParked("s1")

	live := rig.liveNotices("quota_wait")
	if len(live) != 1 {
		t.Fatalf("live quota_wait notices = %d, want 1", len(live))
	}
	if live[0].StageID != "s1" {
		t.Errorf("live notice stage = %q, want s1", live[0].StageID)
	}
	logged := readNotices(t, rig.runDir, "quota_wait")
	if len(logged) != 1 || logged[0].StageID != "s1" {
		t.Fatalf("notices.jsonl quota_wait = %+v, want one for s1", logged)
	}
	data := logged[0].Data
	for k := range data {
		if !quotaNoticeKeys[k] {
			t.Errorf("unexpected notice key %q (secrets and journal-only fields must not leak)", k)
		}
	}
	want := map[string]string{
		"stage_id": "s1", "provider": "claude", "window": "five_hour", "source": "structured",
		"until":    q.Until.UTC().Format(time.RFC3339),
		"reset_at": reset.UTC().Format(time.RFC3339),
	}
	for k, v := range want {
		if data[k] != v {
			t.Errorf("notice %s = %v, want %q (RFC3339 UTC, never a server-local clock time)", k, data[k], v)
		}
	}
	msg, _ := data["message"].(string)
	if !strings.Contains(msg, "UTC") {
		t.Errorf("message %q must name the time zone", msg)
	}
	raw, _ := json.Marshal(data)
	if strings.Contains(string(raw), "sk-secret-token-123") {
		t.Errorf("notice leaked the agent's raw limit message: %s", raw)
	}
	// Критический until живёт в журнале FSM, а не только в notice.
	rs, err := state.LoadRunState(rig.runDir)
	if err != nil || rs.Stages["s1"].Quota == nil || rs.Stages["s1"].Quota.Until.IsZero() {
		t.Errorf("the journal must hold the until: %+v, %v", rs.Stages["s1"].Quota, err)
	}
}

func TestQuotaNotice_WakePublishesQuotaResetOnce_AndHooksFireOncePerTransition(t *testing.T) {
	clk := clock.NewFake(quotaStart)
	rig := startNoticeRig(t, t.TempDir(), refuseOnce(quotaStart.Add(2*time.Minute)), clk, true)
	parked := rig.waitParked("s1")
	parkSeq := rig.stage("s1").Seq
	clk.Advance(5 * time.Minute)
	rig.waitStatus("s1", state.StatusDone)

	resets := readNotices(t, rig.runDir, "quota_reset")
	if len(resets) != 1 || resets[0].StageID != "s1" || resets[0].Data["provider"] != "claude" || resets[0].Data["stage_id"] != "s1" {
		t.Fatalf("quota_reset notices = %+v, want exactly one for s1/claude", resets)
	}
	if n := len(rig.liveNotices("quota_reset")); n != 1 {
		t.Errorf("live quota_reset = %d, want 1", n)
	}
	if n := len(readNotices(t, rig.runDir, "quota_wait")); n != 1 {
		t.Errorf("quota_wait notices = %d, want 1 (no duplicate on wake)", n)
	}

	awaiting := rig.hooks.events(t, lifecyclehooks.EventStageAwaitingQuota)
	if len(awaiting) != 1 {
		t.Fatalf("stage_awaiting_quota hooks = %d, want exactly 1", len(awaiting))
	}
	if awaiting[0].EventID != "run-1:transition:"+strconv.FormatUint(parkSeq, 10)+":stage_awaiting_quota" || awaiting[0].Stage.ID != "s1" || awaiting[0].Stage.To != "awaiting_quota" {
		t.Errorf("hook payload = %+v", awaiting[0])
	}
	if !strings.Contains(awaiting[0].Reason, "UTC") || !strings.Contains(awaiting[0].Reason, parked.Until.UTC().Format("2006-01-02 15:04")) {
		t.Errorf("hook reason %q must carry the wake time with an explicit UTC", awaiting[0].Reason)
	}
	if reset := rig.hooks.events(t, lifecyclehooks.EventStageQuotaReset); len(reset) != 1 || reset[0].Stage.From != "awaiting_quota" {
		t.Errorf("stage_quota_reset hooks = %+v, want exactly 1 from awaiting_quota", reset)
	}
}

func TestQuotaNotice_RestartReplaysWithoutDuplicatingNoticesOrHooks(t *testing.T) {
	runDir := t.TempDir()
	runner := refuseOnce(quotaStart.Add(2 * time.Minute))
	rig := startNoticeRig(t, runDir, runner, clock.NewFake(quotaStart), true)
	rig.waitParked("s1")
	rig.stop()

	clk2 := clock.NewFake(quotaStart.Add(time.Minute))
	rig2 := startNoticeRig(t, runDir, runner, clk2, false)
	rig2.waitStatus("s1", state.StatusAwaitingQuota)
	if n := len(readNotices(t, runDir, "quota_wait")); n != 1 {
		t.Errorf("quota_wait after restart = %d, want 1 (the park is not re-announced; the log is the reconnect replay)", n)
	}
	if got := rig2.hooks.events(t, lifecyclehooks.EventStageAwaitingQuota); len(got) != 0 {
		t.Errorf("restart re-emitted stage_awaiting_quota: %+v", got)
	}

	clk2.BlockUntil(2)
	clk2.Advance(5 * time.Minute)
	rig2.waitStatus("s1", state.StatusDone)
	if n := len(readNotices(t, runDir, "quota_reset")); n != 1 {
		t.Errorf("quota_reset total = %d, want 1", n)
	}
	if got := rig2.hooks.events(t, lifecyclehooks.EventStageQuotaReset); len(got) != 1 {
		t.Errorf("stage_quota_reset after restart = %d, want 1", len(got))
	}
}

func TestQuotaNotice_UnwaitableLimitAnnouncesWhyTheStagePauses(t *testing.T) {
	runner := &quotaRunner{script: func(int, string) error { return limitRefusal(quotaStart.Add(13 * time.Hour)) }}
	rig := startNoticeRig(t, t.TempDir(), runner, clock.NewFake(quotaStart), true)
	rig.waitStatus("s1", state.StatusPaused)

	waits := readNotices(t, rig.runDir, "quota_wait")
	if len(waits) != 1 || waits[0].Data["pause_reason"] != state.QuotaPauseExceedsMaxWait {
		t.Fatalf("quota_wait = %+v, want one carrying pause_reason %s", waits, state.QuotaPauseExceedsMaxWait)
	}
	if _, has := waits[0].Data["until"]; has {
		t.Errorf("a park that only pauses has no until: %+v", waits[0].Data)
	}
	if got := waits[0].Data["reset_at"]; got != quotaStart.Add(13*time.Hour).UTC().Format(time.RFC3339) {
		t.Errorf("reset_at = %v", got)
	}
	hooks := rig.hooks.events(t, lifecyclehooks.EventStageAwaitingQuota)
	if len(hooks) != 1 || !strings.Contains(hooks[0].Reason, "UTC") {
		t.Errorf("stage_awaiting_quota hooks = %+v, want one with a UTC time", hooks)
	}
	if paused := rig.hooks.events(t, lifecyclehooks.EventStagePaused); len(paused) != 1 {
		t.Errorf("stage_paused hooks = %d, want 1", len(paused))
	}
}
