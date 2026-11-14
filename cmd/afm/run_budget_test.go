package main

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/akopichin/afm-engine/engine"
	"github.com/akopichin/afm/contracts/budget"
	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/contracts/flow"
	"github.com/akopichin/afm/contracts/lifecyclehooks"
)

// A hard or medium limit ends repeat as an ordinary finish: one cycle, exit 0, even when Ctrl+C arrives during
// the core's final hold.
func TestCycleLoop_BudgetOutcomeEndsRepeat(t *testing.T) {
	cases := []struct {
		name         string
		launch       launchMode
		cancelInCall bool
	}{
		{"dashboard", launchDashboard, false},
		{"core", launchCore, false},
		{"core, Ctrl+C during the final hold", launchCore, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newCycleFixture(t)
			fx.write("flow.yaml", fx.flowYAML(flowSpec{repeat: "{max_cycles: 3}"}))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var calls atomic.Int32
			prev := executeCycle
			executeCycle = func(_ context.Context, _ *flow.Flow, _ config.Config, _ []lifecyclehooks.RegisteredHook, _ runOptions) (cycleResult, error) {
				calls.Add(1)
				if tc.cancelInCall {
					cancel()
				}
				return cycleResult{RunID: "r1", Completed: true, Budget: &engine.BudgetOutcome{Exhausted: []budget.ScopeID{budget.RunScope()}}}, nil
			}
			t.Cleanup(func() { executeCycle = prev })

			f, err := flow.ParseFile(fx.flowPath)
			if err != nil {
				t.Fatal(err)
			}
			cfg := testCycleConfig()
			l := fx.loop(cfg)
			l.launch = tc.launch
			var runErr error
			out, _ := captureOutput(t, func() {
				_, runErr = l.run(ctx, f, combineRunHooks(cfg, f))
			})

			if runErr != nil {
				t.Fatalf("run = %v, want nil", runErr)
			}
			if got := calls.Load(); got != 1 {
				t.Errorf("executeCycle called %d times, want 1", got)
			}
			if !strings.Contains(out, "repeat stopped: budget limit reached") {
				t.Errorf("output does not name the budget as the reason:\n%s", out)
			}
		})
	}
}

// A cycle that did not complete keeps its failure: the budget only ends repeat for a completed one.
func TestCycleLoop_IncompleteBudgetCycleKeepsFailure(t *testing.T) {
	fx := newCycleFixture(t)
	fx.write("flow.yaml", fx.flowYAML(flowSpec{repeat: "true"}))
	prev := executeCycle
	executeCycle = func(context.Context, *flow.Flow, config.Config, []lifecyclehooks.RegisteredHook, runOptions) (cycleResult, error) {
		return cycleResult{RunID: "r1", Budget: &engine.BudgetOutcome{}}, nil
	}
	t.Cleanup(func() { executeCycle = prev })

	f, err := flow.ParseFile(fx.flowPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg := testCycleConfig()
	var runErr error
	captureOutput(t, func() {
		_, runErr = fx.loop(cfg).run(context.Background(), f, combineRunHooks(cfg, f))
	})
	if runErr == nil || !strings.Contains(runErr.Error(), "did not complete successfully") {
		t.Fatalf("run = %v, want the incomplete-cycle error", runErr)
	}
}
