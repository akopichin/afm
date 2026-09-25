package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// An existing plan does not make a failed stage's dependencies optional.
// Exercise both retry branches that reuse plan.md, including eager planning:
// that flag permits early planning, but never early implementation.
func TestRetryWithExistingPlanWaitsForDependencies(t *testing.T) {
	for _, variant := range []string{"preplanned", "planned", "eager-planning"} {
		for _, depStatus := range []state.StageStatus{state.StatusRunning, state.StatusFailed} {
			t.Run(variant+"/"+string(depStatus), func(t *testing.T) {
				runDir := t.TempDir()
				store, err := state.Open(runDir, []string{"dep", "work"})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { store.Close() })
				for id, status := range map[string]state.StageStatus{"dep": depStatus, "work": state.StatusFailed} {
					if err := store.Apply(&state.Transition{StageID: id, From: state.StatusPending, To: status, Event: "test_setup"}); err != nil {
						t.Fatal(err)
					}
				}

				stageDir := filepath.Join(runDir, "work")
				if err := os.MkdirAll(stageDir, 0755); err != nil {
					t.Fatal(err)
				}
				plan := []byte("## Tasks\n- implement\n## Assumptions\n- none\n## Acceptance Criteria\n- done\n")
				if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), plan, 0644); err != nil {
					t.Fatal(err)
				}
				work := flow.Stage{
					ID: "work", Name: "Work", DependsOn: []string{"dep"}, AutoApprove: true,
					Agents: []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation},
				}
				if variant == "preplanned" {
					work.Agents = []flow.AgentType{flow.AgentImplementation}
					work.Plan = filepath.Join(runDir, "source-plan.md")
					if err := os.WriteFile(work.Plan, plan, 0644); err != nil {
						t.Fatal(err)
					}
				}
				work.EagerPlanning = variant == "eager-planning"
				runner := &ctxCapturingRunner{gotCtx: make(chan context.Context, 4)}
				o := New(Options{
					RunDir: runDir, Store: store, Config: config.Default(), Runner: runner,
					Stages: []flow.Stage{{ID: "dep", Agents: []flow.AgentType{flow.AgentImplementation}}, work},
				})
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				t.Cleanup(func() {
					cancel()
					o.concurrency.WaitAgents()
				})

				if err := o.Retry(ctx, "work"); err != nil {
					t.Fatal(err)
				}
				o.concurrency.WaitAgents()
				if got := store.Get("work"); got != state.StatusPending {
					t.Fatalf("retry with unfinished dependency: got %s, want pending", got)
				}
				if got := len(runner.gotCtx); got != 0 {
					t.Fatalf("implementation started %d times before dependency completed", got)
				}

				// Resume once the dependency has completed. The durable pending
				// stage must still be schedulable and execute exactly once.
				if err := store.Apply(&state.Transition{StageID: "dep", From: depStatus, To: state.StatusDone, Event: "test_setup"}); err != nil {
					t.Fatal(err)
				}
				if err := o.Run(ctx); err != nil {
					t.Fatal(err)
				}
				if got := store.Get("work"); got != state.StatusDone {
					t.Fatalf("after dependency completed: got %s, want done", got)
				}
				if got := len(runner.gotCtx); got != 1 {
					t.Fatalf("implementation calls after dependency completed: got %d, want 1", got)
				}
			})
		}
	}
}

func TestInteractiveStageUsesDescriptionAfterDependency(t *testing.T) {
	for _, depDone := range []bool{false, true} {
		t.Run(map[bool]string{false: "dependency-completes", true: "dependency-already-done"}[depDone], func(t *testing.T) {
			runDir := t.TempDir()
			store, err := state.Open(runDir, []string{"dep", "work"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { store.Close() })
			if depDone {
				if err := store.Apply(&state.Transition{StageID: "dep", From: state.StatusPending, To: state.StatusDone, Event: "test_setup"}); err != nil {
					t.Fatal(err)
				}
			}
			agent := filepath.Join(runDir, "agent.sh")
			if err := os.WriteFile(agent, []byte("#!/bin/sh\nprintf 'run\\n' >> \"$AFM_STAGE_DIR/calls\"\nprintf 'done' > \"$AFM_STAGE_DIR/.done\"\n"), 0755); err != nil {
				t.Fatal(err)
			}
			work := flow.Stage{ID: "work", Description: "Implement the requested change", Interactive: true,
				DependsOn: []string{"dep"}, Agents: []flow.AgentType{flow.AgentImplementation}, Command: agent}
			o := New(Options{
				RunDir: runDir, Store: store, Config: config.Default(),
				Stages: []flow.Stage{{ID: "dep", Script: "true"}, work},
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := o.Run(ctx); err != nil {
				t.Fatal(err)
			}
			plan, err := os.ReadFile(filepath.Join(runDir, "work", "plan.md"))
			if err != nil || string(plan) != work.Description {
				t.Fatalf("interactive plan = %q, err = %v; want stage description", plan, err)
			}
			calls, err := os.ReadFile(filepath.Join(runDir, "work", "calls"))
			if store.Get("work") != state.StatusDone || err != nil || string(calls) != "run\n" {
				t.Fatalf("stage status = %s, calls = %q, err = %v; want done, one execution", store.Get("work"), calls, err)
			}
		})
	}
}
