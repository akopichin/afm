package executor_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/executor"
)

// TestObservation_UIBusDropDoesNotAffectObservations proves OnObservation is delivered synchronously and in
// full, independent of whatever a slow/overflowing consumer does with OnAction — the property bus.UIBus does
// NOT have (it silently drops events for a slow subscriber, see pkg/orchestrator/bus/bus.go's Publish). We
// stand in for "a slow UIBus subscriber" with a tiny, never-drained, non-blocking channel fed from OnAction —
// the same shape of drop bus.UIBus.Publish performs — and assert every one of N agent actions still reached
// OnObservation even though most of them were dropped on the OnAction side.
func TestObservation_UIBusDropDoesNotAffectObservations(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "implementation.log")

	const n = 200
	var script strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&script, `echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"echo %d"}}]}}'`+"\n", i)
	}
	script.WriteString(`echo '{"type":"result","subtype":"success"}'`)

	// A tiny, never-drained "slow subscriber" channel — mirrors bus.UIBus's own drop-on-full Publish.
	slowSub := make(chan struct{}, 1)
	var dropped atomic.Uint64
	var observed atomic.Uint64

	ex := executor.New(executor.Config{
		Command:     "bash",
		ExtraArgs:   []string{"-c", script.String()},
		IdleTimeout: 10 * time.Second,
		RunID:       "run-1",
		StageID:     "impl",
		OnAction: func(tool, detail string) {
			select {
			case slowSub <- struct{}{}:
			default:
				dropped.Add(1)
			}
		},
		OnObservation: func(o executor.Observation) {
			observed.Add(1)
		},
	})

	if err := ex.RunAgent(context.Background(), "implementation", "stage", "do work", logFile); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	if dropped.Load() == 0 {
		t.Fatal("test setup problem: the slow subscriber never actually dropped anything")
	}
	if observed.Load() != n {
		t.Fatalf("OnObservation saw %d observations, want exactly %d (should be unaffected by OnAction-side drops)", observed.Load(), n)
	}
}

// TestObservation_ExistingUIPublisherIsAdapter verifies OnAction's (tool, detail) pair is now DERIVED from the
// same Observation handed to OnObservation (via executor.ObservationToAction) rather than independently
// recomputed — old consumers (e.g. the dashboard's UI action publisher) see byte-identical output to before
// this feature existed, through what is now an adapter over the Observation channel.
func TestObservation_ExistingUIPublisherIsAdapter(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "implementation.log")

	script := `echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"a very long command that will be truncated for display purposes only"}}]}}'
echo '{"type":"result","subtype":"success"}'`

	var mu sync.Mutex
	var actionCalls []string
	var observations []executor.Observation

	ex := executor.New(executor.Config{
		Command:        "bash",
		ExtraArgs:      []string{"-c", script},
		IdleTimeout:    10 * time.Second,
		TruncateOutput: 10,
		RunID:          "run-1",
		StageID:        "impl",
		OnAction: func(tool, detail string) {
			mu.Lock()
			defer mu.Unlock()
			actionCalls = append(actionCalls, tool+"|"+detail)
		},
		OnObservation: func(o executor.Observation) {
			mu.Lock()
			defer mu.Unlock()
			observations = append(observations, o)
		},
	})

	if err := ex.RunAgent(context.Background(), "implementation", "stage", "do work", logFile); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	if len(observations) != 1 || len(actionCalls) != 1 {
		t.Fatalf("expected exactly 1 observation and 1 action call, got %d/%d", len(observations), len(actionCalls))
	}
	wantTool, wantDetail, ok := executor.ObservationToAction(observations[0], 10)
	if !ok {
		t.Fatal("ObservationToAction should accept the recorded observation")
	}
	if actionCalls[0] != wantTool+"|"+wantDetail {
		t.Fatalf("OnAction call %q does not match ObservationToAction's derivation %q|%q — it is not acting as an adapter over the observation", actionCalls[0], wantTool, wantDetail)
	}
	// And the observation itself must carry the FULL, untruncated command — truncation is a display-only
	// concern applied solely by the adapter, never by the normalization layer.
	if len(observations[0].Content) <= 10 {
		t.Fatalf("observation content looks truncated: %q", observations[0].Content)
	}
}
