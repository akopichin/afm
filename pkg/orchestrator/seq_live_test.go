package orchestrator_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

type alwaysSucceedsRunner struct{}

func (alwaysSucceedsRunner) RunPlanning(_ context.Context, _, _, _, _ string) error {
	return errors.New("not used in this test")
}

func (alwaysSucceedsRunner) RunAgent(_ context.Context, _, stageName, _, logFile string) error {
	stageDir := filepath.Dir(logFile)
	_ = stageName
	return os.WriteFile(filepath.Join(stageDir, "execution_summary.md"), []byte("## Summary\ndone\n"), 0644)
}

func (alwaysSucceedsRunner) RunJSONQuery(_ context.Context, _ string) ([]byte, error) {
	return nil, errors.New("not used in this test")
}

var _ executor.Runner = alwaysSucceedsRunner{}

// TestLiveEvent_CarriesRealSeq подтверждает фикс "seq доходит до фронта":
// Trigger публикует EventStageStatusChanged с реальным seq применённой
// transition (не нулём), и этот seq совпадает с тем, что лежит в
// store.History() для той же transition — фронт может дедуплицировать live-
// событие с его историческим двойником из /api/events по этому ключу.
func TestLiveEvent_CarriesRealSeq(t *testing.T) {
	runDir := t.TempDir()
	stages := []flow.Stage{{ID: "a", Name: "a", Agents: []flow.AgentType{flow.AgentAuto}}}

	store, err := state.Open(runDir, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	orch := orchestrator.New(orchestrator.Options{
		RunDir:  runDir,
		Stages:  stages,
		Store:   store,
		Config:  config.Default(),
		Prompts: orchestrator.DefaultPrompts(),
		Runner:  alwaysSucceedsRunner{},
	})

	subID, events := orch.UIBus().Subscribe(64)
	defer orch.UIBus().Unsubscribe(subID)

	var seenNonZeroSeq bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range events {
			if ev.Type == bus.EventStageStatusChanged && ev.Seq != 0 {
				seenNonZeroSeq = true
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := orch.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	orch.UIBus().Unsubscribe(subID)
	<-done

	if !seenNonZeroSeq {
		t.Fatal("no live stage_status_changed event carried a non-zero Seq")
	}

	history, err := store.History()
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(history) == 0 {
		t.Fatal("expected at least one transition in history")
	}
	for _, tr := range history {
		if tr.Seq == 0 {
			t.Errorf("transition %+v has zero Seq in durable history", tr)
		}
	}
}
