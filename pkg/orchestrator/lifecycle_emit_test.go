package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/lifecyclehooks"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// captureDispatcher собирает Payload'ы, доставленные реальным Dispatcher'ом,
// без запуска реальных команд (RunFunc — фейк).
type captureDispatcher struct {
	events chan lifecyclehooks.Payload
}

func newCaptureDispatcher() *captureDispatcher {
	return &captureDispatcher{events: make(chan lifecyclehooks.Payload, 64)}
}

// captureDispatcherAsReal строит настоящий *lifecyclehooks.Dispatcher —
// переиспользует его реальный матчинг хуков и построение event_id/seq
// (BuildPayload/eventID), не запуская никаких процессов: RunFunc просто
// записывает доставленный Payload в capture.events.
func captureDispatcherAsReal(t *testing.T, capture *captureDispatcher) *lifecyclehooks.Dispatcher {
	t.Helper()
	d := lifecyclehooks.New(lifecyclehooks.DispatcherOptions{
		Config: lifecyclehooks.DispatcherConfig{RunID: "run-1"},
		Hooks: []lifecyclehooks.RegisteredHook{{
			Hook: lifecyclehooks.Hook{ID: "cap", Events: lifecyclehooks.EventSelector{All: true}, Command: "true"},
		}},
		RunFunc: func(_ context.Context, _ lifecyclehooks.Hook, _ lifecyclehooks.DispatcherConfig, p lifecyclehooks.Payload, _ string) error {
			capture.events <- p
			return nil
		},
		OnError: func(hookID, eventID string, err error) { t.Logf("hook %s event %s: %v", hookID, eventID, err) },
	})
	d.Start()
	t.Cleanup(d.Stop)
	return d
}

// TestEmitLifecycleTransition_MapsFSMEvent проверяет мапинг bus.EvFail →
// lifecyclehooks.EventStageFailed и то, что from/to/seq/reason реально
// доходят до payload'а хука — сквозь настоящий Dispatcher, а не мок.
func TestEmitLifecycleTransition_MapsFSMEvent(t *testing.T) {
	o := newTestOrchestrator(t) // существующий хелпер пакета (memory_agent_test.go): stage "s1", Name="Stage"
	capture := newCaptureDispatcher()
	o.hooks = captureDispatcherAsReal(t, capture)

	o.emitLifecycleTransition("s1", state.StatusRunning, state.StatusFailed, 42, bus.EvFail, "agent exited 1")

	if !o.hooks.Flush(2 * time.Second) {
		t.Fatal("Flush timed out — delivery never completed")
	}

	select {
	case p := <-capture.events:
		if p.Event != lifecyclehooks.EventStageFailed {
			t.Fatalf("Event = %v, want %v", p.Event, lifecyclehooks.EventStageFailed)
		}
		wantEventID := "run-1:transition:42:stage_failed" // seq=42 обязан дойти в event_id
		if p.EventID != wantEventID {
			t.Fatalf("EventID = %q, want %q", p.EventID, wantEventID)
		}
		if p.Reason != "agent exited 1" {
			t.Fatalf("Reason = %q, want %q", p.Reason, "agent exited 1")
		}
		if p.Stage == nil {
			t.Fatalf("Stage is nil, want populated: %+v", p)
		}
		if p.Stage.From != "running" || p.Stage.To != "failed" {
			t.Fatalf("Stage.From/To = %q/%q, want running/failed", p.Stage.From, p.Stage.To)
		}
		if p.Stage.ID != "s1" || p.Stage.Name != "Stage" {
			t.Fatalf("Stage.ID/Name = %q/%q, want s1/Stage", p.Stage.ID, p.Stage.Name)
		}
	default:
		t.Fatal("no event captured after Flush")
	}
}

// TestEmitLifecycleTransition_UnmappedFSMEventIgnored — EvReady это внутренняя
// планировка без публичного lifecycle-аналога (fsmLifecycleEvents не содержит
// её), emitLifecycleTransition должен молча ничего не эмитить.
func TestEmitLifecycleTransition_UnmappedFSMEventIgnored(t *testing.T) {
	o := newTestOrchestrator(t)
	capture := newCaptureDispatcher()
	o.hooks = captureDispatcherAsReal(t, capture)

	o.emitLifecycleTransition("s1", state.StatusPending, state.StatusReady, 1, bus.EvReady, "")

	o.hooks.Flush(2 * time.Second)
	select {
	case p := <-capture.events:
		t.Fatalf("EvReady must not produce a lifecycle event: %+v", p)
	default:
	}
}

// TestEmitLifecycle_NilSafe — без настроенных хуков (o.hooks == nil, обычный
// путь всех прочих тестов orchestrator и ранов без hooks) все три
// точки отправки — no-op, не паника.
func TestEmitLifecycle_NilSafe(t *testing.T) {
	o := newTestOrchestrator(t)
	o.hooks = nil
	o.emitLifecycle(lifecyclehooks.Event{Type: lifecyclehooks.EventFlowStarted})
	o.emitLifecycleTransition("s1", state.StatusRunning, state.StatusFailed, 1, bus.EvFail, "x")
	o.emitStageEvent(lifecyclehooks.EventStageScriptStarted, "s1", "")
	// не паникует — тест проходит
}
