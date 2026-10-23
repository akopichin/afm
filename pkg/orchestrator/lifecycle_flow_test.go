package orchestrator_test

// Task 9: flow-level lifecycle events emitted from Orchestrator.Run's exits + bounded finalize flush.

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/lifecyclehooks"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

// flowEventRecorder captures delivery order through a real lifecycle dispatcher.
type flowEventRecorder struct {
	mu    sync.Mutex
	types []lifecyclehooks.EventType
}

func (r *flowEventRecorder) record(t lifecyclehooks.EventType) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.types = append(r.types, t)
}

func (r *flowEventRecorder) ordered() []lifecyclehooks.EventType {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]lifecyclehooks.EventType, len(r.types))
	copy(out, r.types)
	return out
}

// newFlowRecorderDispatcher builds a real *lifecyclehooks.Dispatcher whose single "all events" hook
// records delivered event types into rec instead of running any real command.
func newFlowRecorderDispatcher(t *testing.T, rec *flowEventRecorder) *lifecyclehooks.Dispatcher {
	t.Helper()
	d := lifecyclehooks.New(lifecyclehooks.DispatcherOptions{
		Config: lifecyclehooks.DispatcherConfig{RunID: "run-1"},
		Hooks: []lifecyclehooks.RegisteredHook{{
			Hook: lifecyclehooks.Hook{ID: "rec", Events: lifecyclehooks.EventSelector{All: true}, Command: "true"},
		}},
		RunFunc: func(_ context.Context, _ lifecyclehooks.Hook, _ lifecyclehooks.DispatcherConfig, p lifecyclehooks.Payload, _ string) error {
			rec.record(p.Event)
			return nil
		},
		OnError: func(hookID, eventID string, err error) { t.Logf("hook %s event %s: %v", hookID, eventID, err) },
	})
	d.Start()
	t.Cleanup(d.Stop)
	return d
}

// newScriptFlowOrch builds a single script-stage *orchestrator.Orchestrator wired to dispatcher, mirroring
// the setup in TestIntegration_ScriptStage_HappyPath.
func newScriptFlowOrch(t *testing.T, dispatcher *lifecyclehooks.Dispatcher, script string, resumed, withDashboard bool) (*orchestrator.Orchestrator, string) {
	t.Helper()
	return newScriptFlowOrchWith(t, dispatcher, script, func(o *orchestrator.Options) {
		o.Resumed = resumed
		if withDashboard {
			o.DashboardURL = "http://test.invalid"
		}
	})
}

// newScriptFlowOrchWith is newScriptFlowOrch with caller-controlled Options (tweak runs before New).
func newScriptFlowOrchWith(t *testing.T, dispatcher *lifecyclehooks.Dispatcher, script string, tweak func(*orchestrator.Options)) (*orchestrator.Orchestrator, string) {
	t.Helper()
	rootDir := t.TempDir()
	runDir := t.TempDir()
	stages := []flow.Stage{{ID: "notify", Name: "Notify", Script: script}}

	store, err := state.Open(runDir, []string{"notify"})
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	opts := orchestrator.Options{
		RunDir:  runDir,
		RootDir: rootDir,
		Stages:  stages,
		Store:   store,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
		Hooks:   dispatcher,
		Clock:   newFakeClockWithPump(t),
	}
	tweak(&opts)
	orch := orchestrator.New(opts)
	return orch, filepath.Join(runDir, "state.json")
}

// waitForFlowEvent polls rec until want has been recorded, or fails the test
// after timeout — same polling idiom as waitForStatus (integration_test.go).
func waitForFlowEvent(t *testing.T, rec *flowEventRecorder, want lifecyclehooks.EventType, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if containsEvent(rec.ordered(), want) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("event %s not observed within %s (got %v)", want, timeout, rec.ordered())
}

func containsEvent(evs []lifecyclehooks.EventType, want lifecyclehooks.EventType) bool {
	for _, e := range evs {
		if e == want {
			return true
		}
	}
	return false
}

// assertOrder fails unless both before and after are present and the FIRST occurrence of before comes
// strictly earlier than the LAST occurrence of after.
func assertOrder(t *testing.T, evs []lifecyclehooks.EventType, before, after lifecyclehooks.EventType) {
	t.Helper()
	bi, ai := -1, -1
	for i, e := range evs {
		if e == before && bi == -1 {
			bi = i
		}
		if e == after {
			ai = i
		}
	}
	if bi == -1 || ai == -1 {
		t.Fatalf("both %s and %s must be present: %v", before, after, evs)
	}
	if bi >= ai {
		t.Fatalf("%s (idx %d) must come before %s (idx %d): %v", before, bi, after, ai, evs)
	}
}

func TestRun_FlowEvents_Finished(t *testing.T) {
	rec := &flowEventRecorder{}
	dispatcher := newFlowRecorderDispatcher(t, rec)
	orch, stateFile := newScriptFlowOrch(t, dispatcher, "true", false, false)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	runOrchestratorAsync(ctx, t, orch, cancel)

	waitForStatus(t, stateFile, "notify", state.StatusDone, 20*time.Second)
	waitForFlowEvent(t, rec, lifecyclehooks.EventFlowFinished, 5*time.Second)

	evs := rec.ordered()
	if !containsEvent(evs, lifecyclehooks.EventFlowStarted) {
		t.Fatalf("missing flow_started: %v", evs)
	}
	if !containsEvent(evs, lifecyclehooks.EventStageFinished) {
		t.Fatalf("missing stage_finished: %v", evs)
	}
	assertOrder(t, evs, lifecyclehooks.EventFlowStarted, lifecyclehooks.EventFlowFinished)
	// The load-bearing assertion: finalize must run AFTER cancel+WaitAgents have stopped producers — proven
	// here by stage_finished still coming strictly before flow_finished.
	assertOrder(t, evs, lifecyclehooks.EventStageFinished, lifecyclehooks.EventFlowFinished)
}

// TestRun_FlowEvents_Resumed: Options.Resumed=true swaps flow_started for
// flow_resumed — and only that; flow_started must never appear.
func TestRun_FlowEvents_Resumed(t *testing.T) {
	rec := &flowEventRecorder{}
	dispatcher := newFlowRecorderDispatcher(t, rec)
	orch, stateFile := newScriptFlowOrch(t, dispatcher, "true", true, false)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	runOrchestratorAsync(ctx, t, orch, cancel)

	waitForStatus(t, stateFile, "notify", state.StatusDone, 20*time.Second)
	waitForFlowEvent(t, rec, lifecyclehooks.EventFlowFinished, 5*time.Second)

	evs := rec.ordered()
	if !containsEvent(evs, lifecyclehooks.EventFlowResumed) {
		t.Fatalf("missing flow_resumed: %v", evs)
	}
	if containsEvent(evs, lifecyclehooks.EventFlowStarted) {
		t.Fatalf("resumed run must not emit flow_started: %v", evs)
	}
}

// TestRun_FlowEvents_Repeated: Options.Repeated=true swaps flow_started for flow_repeated — and only that —
// and the cycle metadata from the dispatcher config reaches flow_repeated and flow_finished payloads.
func TestRun_FlowEvents_Repeated(t *testing.T) {
	var mu sync.Mutex
	var got []lifecyclehooks.Payload
	d := lifecyclehooks.New(lifecyclehooks.DispatcherOptions{
		Config: lifecyclehooks.DispatcherConfig{RunID: "run-2", Cycle: 2, MaxCycles: 3, PreviousRunID: "run-1"},
		Hooks: []lifecyclehooks.RegisteredHook{{
			Hook: lifecyclehooks.Hook{ID: "rec", Events: lifecyclehooks.EventSelector{All: true}, Command: "true"},
		}},
		RunFunc: func(_ context.Context, _ lifecyclehooks.Hook, _ lifecyclehooks.DispatcherConfig, p lifecyclehooks.Payload, _ string) error {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, p)
			return nil
		},
		OnError: func(hookID, eventID string, err error) { t.Logf("hook %s event %s: %v", hookID, eventID, err) },
	})
	d.Start()
	t.Cleanup(d.Stop)
	orch, stateFile := newScriptFlowOrchWith(t, d, "true", func(o *orchestrator.Options) { o.Repeated = true })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	runOrchestratorAsync(ctx, t, orch, cancel)

	waitForStatus(t, stateFile, "notify", state.StatusDone, 20*time.Second)
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		payloads := append([]lifecyclehooks.Payload(nil), got...)
		mu.Unlock()
		finished := false
		var types []lifecyclehooks.EventType
		for _, p := range payloads {
			types = append(types, p.Event)
			if p.Event == lifecyclehooks.EventFlowFinished {
				finished = true
				if p.Flow.Cycle != 2 || p.Flow.MaxCycles == nil || *p.Flow.MaxCycles != 3 || p.Flow.PreviousRunID != "run-1" {
					t.Fatalf("flow_finished flow block = %+v, want cycle 2 / max_cycles 3 / previous run-1", p.Flow)
				}
			}
		}
		if finished {
			if containsEvent(types, lifecyclehooks.EventFlowStarted) || containsEvent(types, lifecyclehooks.EventFlowResumed) {
				t.Fatalf("repeated run must emit only flow_repeated as start event: %v", types)
			}
			n := 0
			for _, e := range types {
				if e == lifecyclehooks.EventFlowRepeated {
					n++
				}
			}
			if n != 1 {
				t.Fatalf("want exactly one flow_repeated, got %d: %v", n, types)
			}
			assertOrder(t, types, lifecyclehooks.EventFlowRepeated, lifecyclehooks.EventFlowFinished)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("flow_finished not observed: %v", types)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestRun_FlowEvents_ResumedBeatsRepeated: a crash-resumed run keeps flow_resumed even when Repeated is set.
func TestRun_FlowEvents_ResumedBeatsRepeated(t *testing.T) {
	rec := &flowEventRecorder{}
	dispatcher := newFlowRecorderDispatcher(t, rec)
	orch, stateFile := newScriptFlowOrchWith(t, dispatcher, "true", func(o *orchestrator.Options) {
		o.Resumed = true
		o.Repeated = true
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	runOrchestratorAsync(ctx, t, orch, cancel)

	waitForStatus(t, stateFile, "notify", state.StatusDone, 20*time.Second)
	waitForFlowEvent(t, rec, lifecyclehooks.EventFlowFinished, 5*time.Second)

	evs := rec.ordered()
	if !containsEvent(evs, lifecyclehooks.EventFlowResumed) {
		t.Fatalf("missing flow_resumed: %v", evs)
	}
	if containsEvent(evs, lifecyclehooks.EventFlowRepeated) || containsEvent(evs, lifecyclehooks.EventFlowStarted) {
		t.Fatalf("resumed run must not emit flow_repeated/flow_started: %v", evs)
	}
}

func TestRun_FlowEvents_Interrupted(t *testing.T) {
	rec := &flowEventRecorder{}
	dispatcher := newFlowRecorderDispatcher(t, rec)
	orch, _ := newScriptFlowOrch(t, dispatcher, "sleep 30", false, false)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- orch.Run(ctx) }()
	time.Sleep(500 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("orch.Run did not return within 15s of cancellation")
	}

	evs := rec.ordered()
	if !containsEvent(evs, lifecyclehooks.EventFlowInterrupted) {
		t.Fatalf("missing flow_interrupted: %v", evs)
	}
	if containsEvent(evs, lifecyclehooks.EventFlowFinished) {
		t.Fatalf("interrupted run must not emit flow_finished: %v", evs)
	}
}

func TestRun_FlowEvents_Failed(t *testing.T) {
	rec := &flowEventRecorder{}
	dispatcher := newFlowRecorderDispatcher(t, rec)
	orch, stateFile := newScriptFlowOrch(t, dispatcher, "exit 1", false, false)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	runOrchestratorAsync(ctx, t, orch, cancel)

	waitForStatus(t, stateFile, "notify", state.StatusFailed, 20*time.Second)
	waitForFlowEvent(t, rec, lifecyclehooks.EventFlowFailed, 5*time.Second)

	evs := rec.ordered()
	if !containsEvent(evs, lifecyclehooks.EventStageFailed) {
		t.Fatalf("missing stage_failed: %v", evs)
	}
}
