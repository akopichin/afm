package orchestrator_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

// cascadeRetryRunner: RunAgent для стадии "a" возвращает не-retryable ошибку
// на первом вызове (имитирует реальный сбой), затем успешно пишет
// execution_summary.md на всех последующих вызовах (успех после ручного retry).
// Стадия "b" (depends_on: ["a"]) при первом прогоне никогда не активируется —
// её каскадно валит failBlockedStages ДО того, как для неё создаётся stageDir.
type cascadeRetryRunner struct {
	mu     sync.Mutex
	aCalls int
}

func (r *cascadeRetryRunner) RunPlanning(_ context.Context, _, _, _, _ string) error {
	return errors.New("not used in this test")
}

func (r *cascadeRetryRunner) RunAgent(_ context.Context, _, stageName, _, logFile string) error {
	stageDir := filepath.Dir(logFile)
	if stageName == "a" {
		r.mu.Lock()
		r.aCalls++
		n := r.aCalls
		r.mu.Unlock()
		if n == 1 {
			return errors.New("boom: first attempt always fails")
		}
	}
	return os.WriteFile(filepath.Join(stageDir, "execution_summary.md"), []byte("## Summary\ndone\n"), 0644)
}

func (r *cascadeRetryRunner) RunJSONQuery(_ context.Context, _ string) ([]byte, error) {
	return nil, errors.New("not used in this test")
}

var _ executor.Runner = (*cascadeRetryRunner)(nil)

// TestIntegration_CascadeFailedStageRetrySucceeds воспроизводит баг из
// прод-лога GogaFeature-20260726-133106-3c1c: стадия "a" падает, стадия "b"
// (depends_on: ["a"]) каскадно валится через blocked_by_dep, никогда не
// получив свой stageDir. Ручной retry "a" должен пройти успешно, а затем
// ручной retry "b" должен СОЗДАТЬ ей stageDir и autonomous.flag и успешно
// завершиться — а не падать вечно с "open log file: ... no such file or
// directory" (баг: retryStage спавнил runAutonomousAgent без MkdirAll).
func TestIntegration_CascadeFailedStageRetrySucceeds(t *testing.T) {
	dir := t.TempDir()
	stages := []flow.Stage{
		{ID: "a", Name: "a", Agents: []flow.AgentType{flow.AgentAuto}},
		{ID: "b", Name: "b", Agents: []flow.AgentType{flow.AgentAuto}, DependsOn: []string{"a"}},
	}

	store, err := state.Open(dir, []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stateFile := filepath.Join(dir, "state.json")

	runner := &cascadeRetryRunner{}
	orch := orchestrator.New(orchestrator.Options{
		RunDir: dir,
		Stages: stages,
		Store:  store,
		Config: config.Default(),
		// DashboardURL непустой — Run() не должен завершиться сразу после
		// того, как обе стадии стали terminal (failed): нужен живой run-ctx
		// для ручных Retry() ниже, как в реальном прогоне с открытым дашбордом.
		DashboardURL: "http://test.invalid",
		Prompts:      orchestrator.DefaultPrompts(),
		Runner:       runner,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	runOrchestratorAsync(ctx, t, orch, cancel)

	waitForStatus(t, stateFile, "a", state.StatusFailed, 10*time.Second)
	waitForStatus(t, stateFile, "b", state.StatusFailed, 10*time.Second)

	// "b" никогда не активировалась — её директории на диске не существует.
	bDir := filepath.Join(dir, "b")
	if _, err := os.Stat(bDir); err == nil {
		t.Fatalf("expected %s to not exist before any activation, but it does", bDir)
	}

	if err := orch.Retry(ctx, "a"); err != nil {
		t.Fatalf("Retry(a): %v", err)
	}
	waitForStatus(t, stateFile, "a", state.StatusDone, 10*time.Second)

	if err := orch.Retry(ctx, "b"); err != nil {
		t.Fatalf("Retry(b): %v", err)
	}
	waitForStatus(t, stateFile, "b", state.StatusDone, 10*time.Second)

	if _, err := os.Stat(bDir); err != nil {
		t.Errorf("stageDir for b was not created by retry: %v", err)
	}
	if _, err := os.Stat(filepath.Join(bDir, "autonomous.flag")); err != nil {
		t.Errorf("autonomous.flag for b was not created by retry: %v", err)
	}
}

// blockingAutonomousRunner: "a" fails on its first RunAgent call (so it can be
// manually retried), then on its second call blocks on release before writing
// execution_summary.md. "b" (depends_on: ["a"]) always succeeds immediately —
// the test asserts it is never called until "a" is actually done.
type blockingAutonomousRunner struct {
	mu      sync.Mutex
	aCalls  int
	bCalls  int
	release <-chan struct{}
}

func (r *blockingAutonomousRunner) RunPlanning(_ context.Context, _, _, _, _ string) error {
	return errors.New("not used in this test")
}

func (r *blockingAutonomousRunner) RunAgent(ctx context.Context, _, stageName, _, logFile string) error {
	stageDir := filepath.Dir(logFile)
	switch stageName {
	case "a":
		r.mu.Lock()
		r.aCalls++
		n := r.aCalls
		r.mu.Unlock()
		if n == 1 {
			return errors.New("boom: first attempt always fails")
		}
		select {
		case <-r.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	case "b":
		r.mu.Lock()
		r.bCalls++
		r.mu.Unlock()
	default:
	}
	return os.WriteFile(filepath.Join(stageDir, "execution_summary.md"), []byte("## Summary\ndone\n"), 0644)
}

func (r *blockingAutonomousRunner) RunJSONQuery(_ context.Context, _ string) ([]byte, error) {
	return nil, errors.New("not used in this test")
}

func (r *blockingAutonomousRunner) bCallCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bCalls
}

var _ executor.Runner = (*blockingAutonomousRunner)(nil)

// TestIntegration_ManualRetryAutonomousWaitsForDeps воспроизводит живой баг:
// пользователь ретраил стадию "a" и, пока она ещё выполнялась (не done),
// вручную ретраил зависящую от неё автономную стадию "b" — retryStage
// безусловно переводила "b" в ready→running, не проверяя depsDone (в отличие
// от соседней ветки для НЕ-автономных стадий чуть ниже в scheduling.go,
// у которой проверка уже была). "b" должна остаться pending и получить
// собственный агент только после того, как "a" реально завершится.
func TestIntegration_ManualRetryAutonomousWaitsForDeps(t *testing.T) {
	dir := t.TempDir()
	stages := []flow.Stage{
		{ID: "a", Name: "a", Agents: []flow.AgentType{flow.AgentAuto}},
		{ID: "b", Name: "b", Agents: []flow.AgentType{flow.AgentAuto}, DependsOn: []string{"a"}},
	}

	store, err := state.Open(dir, []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	stateFile := filepath.Join(dir, "state.json")

	release := make(chan struct{})
	runner := &blockingAutonomousRunner{release: release}
	orch := orchestrator.New(orchestrator.Options{
		RunDir:       dir,
		Stages:       stages,
		Store:        store,
		Config:       config.Default(),
		DashboardURL: "http://test.invalid",
		Prompts:      orchestrator.DefaultPrompts(),
		Runner:       runner,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	runOrchestratorAsync(ctx, t, orch, cancel)

	// "a" fails its first attempt; "b" cascades to failed without ever
	// getting its own stageDir.
	waitForStatus(t, stateFile, "a", state.StatusFailed, 10*time.Second)
	waitForStatus(t, stateFile, "b", state.StatusFailed, 10*time.Second)

	// Retry "a": its second RunAgent call blocks on release, so "a" sits in
	// running until we let it through below.
	if err := orch.Retry(ctx, "a"); err != nil {
		t.Fatalf("Retry(a): %v", err)
	}
	waitForStatus(t, stateFile, "a", state.StatusRunning, 10*time.Second)

	// Manual retry of "b" while "a" is still running (not done): "b" must
	// stay pending, not jump straight to running.
	if err := orch.Retry(ctx, "b"); err != nil {
		t.Fatalf("Retry(b): %v", err)
	}
	waitForStatus(t, stateFile, "b", state.StatusPending, 10*time.Second)

	// Give the (buggy, pre-fix) code a chance to wrongly spawn "b" before we
	// release "a" — this is the window the live bug fell into.
	time.Sleep(200 * time.Millisecond)
	if got := runner.bCallCount(); got != 0 {
		t.Fatalf("b's agent was called %d time(s) before its dependency finished", got)
	}
	if rs := loadStateJSON(t, stateFile); rs.Stages["b"].Status != state.StatusPending {
		t.Fatalf("b: expected pending while dep still running, got %v", rs.Stages["b"].Status)
	}

	// Let "a" finish; "b" should now be picked up by the normal deps-aware
	// scheduling path and complete on its own.
	close(release)
	waitForStatus(t, stateFile, "a", state.StatusDone, 10*time.Second)
	waitForStatus(t, stateFile, "b", state.StatusDone, 10*time.Second)

	if got := runner.bCallCount(); got != 1 {
		t.Errorf("expected b's agent to be called exactly once, got %d", got)
	}
}
