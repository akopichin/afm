package lifecyclehooks

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeRun собирает доставки; блокировка через канал gate даёт управляемый
// темп, чтобы проверить сериализацию и flush.
func newRecordingDispatcher(t *testing.T, hooks []RegisteredHook) (*Dispatcher, *recorder) {
	t.Helper()
	rec := &recorder{}
	d := New(DispatcherOptions{
		Config: DispatcherConfig{FlowName: "f", RunID: "run-1", RunDir: t.TempDir(), RootDir: t.TempDir()},
		Hooks:  hooks,
		LogDir: t.TempDir(),
		RunFunc: func(ctx context.Context, h Hook, cfg DispatcherConfig, p Payload, logPath string) error {
			rec.mu.Lock()
			rec.deliveries = append(rec.deliveries, h.ID+":"+string(p.Event)+":"+p.EventID)
			rec.mu.Unlock()
			if rec.gate != nil {
				select {
				case <-rec.gate:
				case <-ctx.Done():
				}
			}
			return rec.err
		},
		OnError: func(hookID, eventID string, err error) {
			rec.mu.Lock()
			rec.failures = append(rec.failures, hookID+"|"+eventID+"|"+err.Error())
			rec.mu.Unlock()
		},
	})
	return d, rec
}

type recorder struct {
	mu         sync.Mutex
	deliveries []string
	failures   []string
	gate       chan struct{}
	err        error
}

func (r *recorder) snapshot() ([]string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := append([]string(nil), r.deliveries...)
	b := append([]string(nil), r.failures...)
	return a, b
}

func TestDispatcher_RoutesAndBuildsEventIDs(t *testing.T) {
	global := RegisteredHook{Hook: Hook{ID: "g", Events: EventSelector{All: true}, Command: "true"}}
	scoped := RegisteredHook{Hook: Hook{ID: "s", Events: EventSelector{All: true}, Command: "true"}, StageID: "deploy"}
	d, rec := newRecordingDispatcher(t, []RegisteredHook{global, scoped})
	d.Start()
	defer d.Stop()

	d.Emit(Event{Type: EventStageFailed, StageID: "build", Seq: 7, Time: time.Now()})
	d.Emit(Event{Type: EventStageFailed, StageID: "deploy", Seq: 8, Time: time.Now()})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if dl, _ := rec.snapshot(); len(dl) == 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	dl, _ := rec.snapshot()
	if len(dl) != 3 {
		t.Fatalf("deliveries: %+v", dl)
	}
	has := func(s string) bool {
		for _, d := range dl {
			if d == s {
				return true
			}
		}
		return false
	}
	if !has("g:stage_failed:run-1:transition:7:stage_failed") || !has("g:stage_failed:run-1:transition:8:stage_failed") {
		t.Fatalf("global hook ids: %+v", dl)
	}
	if !has("s:stage_failed:run-1:transition:8:stage_failed") {
		t.Fatalf("scoped hook: %+v", dl)
	}
}

func TestDispatcher_FlowEventID(t *testing.T) {
	d, rec := newRecordingDispatcher(t, []RegisteredHook{{Hook: Hook{ID: "g", Events: EventSelector{All: true}, Command: "true"}}})
	d.Start()
	defer d.Stop()
	d.Emit(Event{Type: EventFlowFinished, Time: time.Now()})
	waitFor(t, rec, 1)
	dl, _ := rec.snapshot()
	if len(dl) != 1 || dl[0] != "g:flow_finished:run-1:flow:flow_finished" {
		t.Fatalf("flow id: %+v", dl)
	}
}

func TestDispatcher_NonFSMStageEventID(t *testing.T) {
	d, rec := newRecordingDispatcher(t, []RegisteredHook{{Hook: Hook{ID: "g", Events: EventSelector{Events: []EventType{EventStageScriptStarted}}, Command: "true"}}})
	d.Start()
	defer d.Stop()
	d.Emit(Event{Type: EventStageScriptStarted, StageID: "deploy", Time: time.Now()})
	waitFor(t, rec, 1)
	dl, _ := rec.snapshot()
	if len(dl) != 1 || dl[0] != "g:stage_script_started:run-1:deploy:stage_script_started" {
		t.Fatalf("non-fsm id: %+v", dl)
	}
}

func TestDispatcher_SequentialPerHook(t *testing.T) {
	d, rec := newRecordingDispatcher(t, []RegisteredHook{{Hook: Hook{ID: "g", Events: EventSelector{All: true}, Command: "true"}}})
	rec.gate = make(chan struct{})
	d.Start()
	defer d.Stop()

	d.Emit(Event{Type: EventFlowStarted, Time: time.Now()})
	d.Emit(Event{Type: EventFlowFinished, Time: time.Now()})
	time.Sleep(100 * time.Millisecond) // обе должны стоять в очереди, NONE выполняется
	if dl, _ := rec.snapshot(); len(dl) != 1 {
		t.Fatalf("worker must process strictly sequentially, got %+v", dl)
	}
	close(rec.gate)
	if ok := d.Flush(2 * time.Second); !ok {
		t.Fatal("flush timeout")
	}
	if dl, _ := rec.snapshot(); len(dl) != 2 {
		t.Fatalf("after flush: %+v", dl)
	}
}

func TestDispatcher_FinalFailureReportsOnError(t *testing.T) {
	d, rec := newRecordingDispatcher(t, []RegisteredHook{{Hook: Hook{ID: "g", Events: EventSelector{All: true}, Command: "true"}}})
	rec.err = errors.New("boom")
	d.Start()
	defer d.Stop()
	d.Emit(Event{Type: EventFlowStarted, Time: time.Now()})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, fl := rec.snapshot(); len(fl) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, fl := rec.snapshot()
	if len(fl) != 1 || fl[0] != "g|run-1:flow:flow_started|boom" {
		t.Fatalf("failures: %+v", fl)
	}
}

func TestDispatcher_QueueOverflowDropsNotBlocks(t *testing.T) {
	// один хук с заблокированным раннером + очередь 256: эмиттер не блокируется
	d, rec := newRecordingDispatcher(t, []RegisteredHook{{Hook: Hook{ID: "g", Events: EventSelector{All: true}, Command: "true"}}})
	rec.gate = make(chan struct{})
	d.Start()
	defer d.Stop()
	for i := 0; i < hookQueueSize+50; i++ {
		start := time.Now()
		d.Emit(Event{Type: EventFlowStarted, Time: time.Now()})
		if time.Since(start) > 500*time.Millisecond {
			t.Fatalf("Emit blocked at %d", i)
		}
	}
	_, fl := rec.snapshot()
	if len(fl) == 0 {
		t.Fatal("overflow must be reported via OnError")
	}
	close(rec.gate)
}

func TestDispatcher_ConcurrentEmitStopNoPanic(t *testing.T) {
	// codex CRIT#2: поздние эмиттеры (poller, agent-горутины) против Stop.
	// Гоняется вместе с -race.
	d, _ := newRecordingDispatcher(t, []RegisteredHook{{Hook: Hook{ID: "g", Events: EventSelector{All: true}, Command: "true"}}})
	d.Start()
	done := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Go(func() {
			for {
				select {
				case <-done:
					return
				default:
					d.Emit(Event{Type: EventFlowStarted, Time: time.Now()})
				}
			}
		})
	}
	time.Sleep(50 * time.Millisecond)
	d.Stop() // на гонящихся эмиттерах
	d.Stop() // идемпотентность
	close(done)
	wg.Wait()
}

func waitFor(t *testing.T, rec *recorder, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if dl, _ := rec.snapshot(); len(dl) >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}
