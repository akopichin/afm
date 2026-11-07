package afmsdk

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// shortGraces keeps process-lifecycle tests fast; the package defaults are production values.
func shortGraces(t *testing.T) {
	t.Helper()
	oldShutdown, oldInterrupt, oldDelay := shutdownGrace, waitInterruptGrace, processWaitDelay
	shutdownGrace, waitInterruptGrace, processWaitDelay = 1500*time.Millisecond, 1500*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { shutdownGrace, waitInterruptGrace, processWaitDelay = oldShutdown, oldInterrupt, oldDelay })
}

func startFake(t *testing.T, cfg fakeCfg, mode Mode) *Run {
	t.Helper()
	c := useFakeAfm(t, cfg, mode)
	run, err := c.Start(context.Background(), "flow.yaml", t.TempDir())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = run.Shutdown(context.Background()) })
	return run
}

func TestShutdown_CleanExit(t *testing.T) {
	run := startFake(t, fakeCfg{}, ModeDashboard)
	if err := run.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := run.Shutdown(context.Background()); err != nil {
		t.Fatalf("repeated Shutdown: %v", err)
	}
}

func TestShutdown_ReturnsExitError(t *testing.T) {
	run := startFake(t, fakeCfg{ExitCode: 130}, ModeDashboard)
	var exitErr *exec.ExitError
	if err := run.Shutdown(context.Background()); !errors.As(err, &exitErr) || exitErr.ExitCode() != 130 {
		t.Fatalf("Shutdown = %v, want exit 130", err)
	}
}

func TestShutdown_IgnoredSigintIsKilled(t *testing.T) {
	shortGraces(t)
	run := startFake(t, fakeCfg{IgnoreSigint: true}, ModeDashboard)
	err := run.Shutdown(context.Background())
	if !errors.Is(err, ErrKilled) {
		t.Fatalf("Shutdown = %v, want ErrKilled", err)
	}
	if err2 := run.Shutdown(context.Background()); err2 == nil || err2.Error() != err.Error() {
		t.Fatalf("repeated Shutdown = %v, want same as first (%v)", err2, err)
	}
}

func TestShutdown_ContextDeadlineBoundsGrace(t *testing.T) {
	run := startFake(t, fakeCfg{IgnoreSigint: true}, ModeDashboard)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := run.Shutdown(ctx); !errors.Is(err, ErrKilled) {
		t.Fatalf("Shutdown = %v, want ErrKilled", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("Shutdown ignored the context deadline: %v", time.Since(start))
	}
}

func TestShutdown_ChildHoldingPipeDoesNotHang(t *testing.T) {
	shortGraces(t)
	pidFile := filepath.Join(t.TempDir(), "orphan.pid")
	run := startFake(t, fakeCfg{Orphan: true, OrphanPIDs: pidFile}, ModeDashboard)
	t.Cleanup(func() { killOrphan(t, pidFile) })
	done := make(chan error, 1)
	go func() { done <- run.Shutdown(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Shutdown = %v, want nil for a clean exit with a pipe-holding child", err)
		}
		pid := waitOrphanPID(t, pidFile)
		if !processAlive(pid) {
			t.Fatalf("orphan %d is gone: the test no longer exercises WaitDelay", pid)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Shutdown hung on a child holding the output pipe")
	}
}

func waitOrphanPID(t *testing.T, pidFile string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(string(raw)); err == nil {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("orphan child never started")
	return 0
}

// killOrphan stops the pipe-holding child so it does not outlive the test.
func killOrphan(t *testing.T, pidFile string) {
	t.Helper()
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		return
	}
	if pid, err := strconv.Atoi(string(raw)); err == nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

func TestShutdown_ConcurrentWithWait(t *testing.T) {
	run := startFake(t, fakeCfg{}, ModeDashboard)
	var wg sync.WaitGroup
	errs := make([]error, 3)
	wg.Add(3)
	go func() { defer wg.Done(); errs[0] = run.Shutdown(context.Background()) }()
	go func() { defer wg.Done(); errs[1] = run.Shutdown(context.Background()) }()
	go func() { defer wg.Done(); errs[2] = run.Wait(context.Background()) }()
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("call %d: %v", i, err)
		}
	}
}

// waitReaped blocks until afm is reaped; with a pipe-holding child Run.exited is still open at that point.
func waitReaped(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("afm was never reaped")
}

func waitStatusOf(t *testing.T, err error) syscall.WaitStatus {
	t.Helper()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("err = %v, want *exec.ExitError", err)
	}
	ws, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok {
		t.Fatalf("no wait status in %v", err)
	}
	return ws
}

func TestShutdown_SignalExitWhilePipesHeldIsNotErrKilled(t *testing.T) {
	shortGraces(t)
	processWaitDelay = 2 * time.Second
	pidFile := filepath.Join(t.TempDir(), "orphan.pid")
	run := startFake(t, fakeCfg{Orphan: true, OrphanPIDs: pidFile, DieBySignal: true}, ModeDashboard)
	t.Cleanup(func() { killOrphan(t, pidFile) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- run.Shutdown(ctx) }()
	waitOrphanPID(t, pidFile)
	waitReaped(t, run.PID())
	cancel() // the deadline fires after afm died of SIGTERM but before the pipes drained

	err := <-errCh
	if errors.Is(err, ErrKilled) {
		t.Fatalf("Shutdown = %v, afm died of SIGTERM and nobody killed it", err)
	}
	if ws := waitStatusOf(t, err); !ws.Signaled() || ws.Signal() != syscall.SIGTERM {
		t.Fatalf("wait status = %v, want SIGTERM", ws)
	}
	if err2 := run.Shutdown(context.Background()); err2 == nil || err2.Error() != err.Error() || errors.Is(err2, ErrKilled) {
		t.Fatalf("repeated Shutdown = %v, want same as first (%v)", err2, err)
	}
}

func TestShutdown_LateKillDoesNotRewriteOutcome(t *testing.T) {
	run := startFake(t, fakeCfg{DieBySignal: true}, ModeDashboard)
	err1 := run.Shutdown(context.Background())
	if err1 == nil || errors.Is(err1, ErrKilled) {
		t.Fatalf("Shutdown = %v, want a SIGTERM exit error without ErrKilled", err1)
	}
	run.kill() // a concurrent caller whose deadline fired after the outcome was published
	expired, cancel := context.WithCancel(context.Background())
	cancel()
	err2 := run.Shutdown(expired)
	if err2 == nil || err2.Error() != err1.Error() || errors.Is(err2, ErrKilled) {
		t.Fatalf("late Shutdown = %v, want same as first (%v)", err2, err1)
	}
}

func TestShutdown_KilledOutcomeIsSharedByEveryCaller(t *testing.T) {
	shortGraces(t)
	run := startFake(t, fakeCfg{IgnoreSigint: true}, ModeDashboard)
	expired, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	results := make([]error, 3)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); results[0] = run.Shutdown(expired) }()
	go func() { defer wg.Done(); results[1] = run.Shutdown(context.Background()) }()
	wg.Wait()
	results[2] = run.Shutdown(context.Background())
	for i, err := range results {
		if !errors.Is(err, ErrKilled) || err.Error() != results[0].Error() {
			t.Fatalf("call %d = %v, want ErrKilled identical to %v", i, err, results[0])
		}
		if ws := waitStatusOf(t, err); ws.Signal() != syscall.SIGKILL {
			t.Fatalf("call %d: wait status = %v, want SIGKILL", i, ws)
		}
	}
}

func TestShutdown_ConcurrentCallersAgree(t *testing.T) {
	shortGraces(t)
	run := startFake(t, fakeCfg{DieBySignal: true}, ModeDashboard)
	expired, cancel := context.WithCancel(context.Background())
	cancel()
	results := make([]error, 8)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.Background()
			if i%2 == 0 {
				ctx = expired
			}
			results[i] = run.Shutdown(ctx)
		}()
	}
	wg.Wait()
	last := run.Shutdown(context.Background())
	for i, err := range results {
		if err == nil || err.Error() != last.Error() || errors.Is(err, ErrKilled) != errors.Is(last, ErrKilled) {
			t.Fatalf("call %d = %v, want identical to %v", i, err, last)
		}
	}
}

// cancelOnSecondErr reports the context as live once, then cancelled: the cancel lands after Start's first check.
type cancelOnSecondErr struct {
	context.Context
	calls atomic.Int32
}

func (c *cancelOnSecondErr) Err() error {
	if c.calls.Add(1) > 1 {
		return context.Canceled
	}
	return nil
}

func TestStart_CancelledBetweenChecksDoesNotSpawn(t *testing.T) {
	c := useFakeAfm(t, fakeCfg{}, ModeDashboard)
	c.binary = filepath.Join(t.TempDir(), "missing-afm") // spawning it would fail with a start error
	c.sem = make(chan struct{}, 1)
	ctx := &cancelOnSecondErr{Context: context.Background()}
	if _, err := c.Start(ctx, "flow.yaml", t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start = %v, want context.Canceled without a spawn attempt", err)
	}
	if len(c.sem) != 0 {
		t.Fatal("slot was not released")
	}
}

func TestNotOwnedRun(t *testing.T) {
	run := &Run{}
	for name, err := range map[string]error{
		"Shutdown": run.Shutdown(context.Background()),
		"Wait":     run.Wait(context.Background()),
		"Cleanup":  run.Cleanup(),
	} {
		if !errors.Is(err, ErrNotOwned) {
			t.Errorf("%s = %v, want ErrNotOwned", name, err)
		}
	}
}

func TestStart_FailureOutputIsBounded(t *testing.T) {
	c := useFakeAfm(t, fakeCfg{FloodBytes: 10 << 20}, ModeDashboard)
	_, err := c.Start(context.Background(), "flow.yaml", t.TempDir())
	if err == nil {
		t.Fatal("Start succeeded")
	}
	if len(err.Error()) > maxErrorBytes+512 {
		t.Fatalf("error is %d bytes, want a bounded tail", len(err.Error()))
	}
}

func TestStart_FailureFreesSlot(t *testing.T) {
	c := useFakeAfm(t, fakeCfg{ExitNow: 2}, ModeDashboard)
	c.sem = make(chan struct{}, 1)
	for i := 0; i < 2; i++ {
		if _, err := c.Start(context.Background(), "flow.yaml", t.TempDir()); err == nil || !strings.Contains(err.Error(), "refusing to start") {
			t.Fatalf("Start #%d = %v", i, err)
		}
	}
}

func TestStart_CancelledBeforeSpawn(t *testing.T) {
	c := useFakeAfm(t, fakeCfg{}, ModeDashboard)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dir := t.TempDir()
	if _, err := c.Start(ctx, "flow.yaml", dir); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start = %v, want context.Canceled", err)
	}
}

func TestTailBuffer_KeepsLastBytes(t *testing.T) {
	b := &tailBuffer{max: 8}
	_, _ = b.Write([]byte("0123456789"))
	_, _ = b.Write([]byte("abc"))
	if got := b.String(); got != "56789abc" {
		t.Fatalf("tail = %q", got)
	}
}

func TestStart_UsesBinaryResolvedByNew(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	if err := os.Symlink(exe, filepath.Join(binDir, "afm-fake")); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(fakeCfg{})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeEnv, string(raw))
	t.Setenv("PATH", binDir)
	c, err := New(Config{Binary: "afm-fake"})
	if err != nil {
		t.Fatal(err)
	}
	// Start must not look the name up again: that scan is a blocking step after the last cancellation check.
	t.Setenv("PATH", t.TempDir())
	run, err := c.Start(context.Background(), "flow.yaml", t.TempDir())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := run.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestNew_BareBinaryNotOnPATH(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := New(Config{Binary: "afm-no-such-binary"}); !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("New = %v, want exec.ErrNotFound", err)
	}
}
