package executor_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/clock"
	"github.com/akopichin/afm/pkg/executor"
)

// TestIdleTimeout_FakeClockFiresAndKillsProcess asserts the idle timer kills a silent subprocess once the
// clock is advanced past IdleTimeout — using a prod-realistic timeout (30m, the actual default) that would
// be far too slow to test with a real sleep (T10, dev-plan.md).
func TestIdleTimeout_FakeClockFiresAndKillsProcess(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "reached-end")
	logFile := filepath.Join(dir, "idle.log")

	fc := clock.NewFake(time.Unix(0, 0))
	ex := executor.New(executor.Config{
		Command: testCmdShell,
		// The marker is only created after a REAL sleep far longer than the test should ever wait — if the
		// idle timeout did not fire (or fired without killing the process), the test would hang on <-done
		// instead, not silently pass.
		ExtraArgs:   []string{testFlagC, "sleep 300 && touch " + marker},
		IdleTimeout: 30 * time.Minute, // the actual production default (executor.go's New)
		Clock:       fc,
	})

	done := make(chan error, 1)
	go func() { done <- ex.RunAgent(context.Background(), "implementation", "test-stage", "do work", logFile) }()

	fc.BlockUntil(1)
	fc.Advance(30 * time.Minute)

	select {
	case err := <-done:
		if !errors.Is(err, executor.ErrIdleTimeout) {
			t.Fatalf("RunAgent error = %v, want errors.Is(err, ErrIdleTimeout)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunAgent did not return after the idle timeout should have fired")
	}

	// The process must actually be dead, not just abandoned: give it a brief real moment it would need to
	// reach the marker if it (wrongly) survived, then confirm it never did.
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("subprocess was not killed — it ran to completion after the idle timeout fired")
	}
}

// TestIdleTimeout_ResetDefersDeadlineOnOutput asserts that each line of output resets the idle deadline —
// a steady stream of output must never trip the timeout even though its cumulative wall-clock span exceeds
// IdleTimeout, as long as no single gap between lines does.
func TestIdleTimeout_ResetDefersDeadlineOnOutput(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "reset.log")

	fc := clock.NewFake(time.Unix(0, 0))
	lineCh := make(chan struct{}, 8)
	ex := executor.New(executor.Config{
		Command: testCmdShell,
		// A real gap between lines matters: it guarantees each line's idleTimer.Reset() lands strictly after
		// the PRECEDING Advance in the loop below, so the fake clock's "now" at each Reset keeps climbing
		// instead of all three resets racing in at now==0 before the test loop gets to advance at all.
		ExtraArgs:   []string{testFlagC, "for i in 1 2 3; do echo line$i; sleep 0.05; done; sleep 300"},
		IdleTimeout: time.Hour,
		Clock:       fc,
		// RunScript reports every raw stdout line via OnAction unconditionally — unlike RunAgent, which only
		// reports lines that parse as the JSON stream protocol.
		OnAction: func(stream, line string) { lineCh <- struct{}{} },
	})

	done := make(chan error, 1)
	go func() { done <- ex.RunScript(context.Background(), 0, logFile) }()

	fc.BlockUntil(1) // initial idle timer registered

	for i := 0; i < 3; i++ {
		select {
		case <-lineCh:
		case <-time.After(2 * time.Second):
			t.Fatalf("line %d did not arrive in time", i+1)
		}
		// idleTimer.Reset is called synchronously right after the line callback, on the same goroutine — a
		// brief real settle lets it land before the next Advance (scheduling slack, not a production duration).
		time.Sleep(10 * time.Millisecond)
		fc.Advance(30 * time.Minute) // half of IdleTimeout: must not fire if Reset just re-armed the full hour
	}

	select {
	case err := <-done:
		t.Fatalf("RunAgent returned early (%v) — the idle timer was not reset by incoming output", err)
	case <-time.After(50 * time.Millisecond):
	}

	// No more output is coming (the script just sleeps for real now) — advance well past the last reset's
	// deadline to let the idle timeout actually fire.
	fc.Advance(time.Hour)

	select {
	case err := <-done:
		if !errors.Is(err, executor.ErrIdleTimeout) {
			t.Fatalf("expected ErrIdleTimeout once output stopped, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunAgent did not return after the idle timeout should have fired")
	}
}

// TestInterruptGrace_SIGKILLAfterGraceElapsed asserts that a subprocess which ignores SIGINT is force-killed
// once the interrupt grace period elapses — using the clock instead of a real 15s wait.
func TestInterruptGrace_SIGKILLAfterGraceElapsed(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "sigint-ignored-but-ran-to-completion")
	trapped := filepath.Join(dir, "sigint-trap-installed")
	logFile := filepath.Join(dir, "grace.log")

	fc := clock.NewFake(time.Unix(0, 0))
	interruptCh := make(chan struct{}, 1)
	ex := executor.New(executor.Config{
		Command: testCmdShell,
		// trap '' TERM — ignore SIGINT's default disposition is already "terminate", so explicitly trap it
		// to nothing, forcing the executor down the SIGKILL grace-timeout path.
		ExtraArgs:   []string{testFlagC, "trap '' INT; touch " + trapped + "; sleep 300 && touch " + marker},
		IdleTimeout: time.Hour,
		Clock:       fc,
		InterruptCh: interruptCh,
	})

	done := make(chan error, 1)
	go func() { done <- ex.RunAgent(context.Background(), "implementation", "test-stage", "do work", logFile) }()

	fc.BlockUntil(1) // idle timer armed — the process has started
	// A SIGINT that arrives before the shell has installed its trap kills it, and the run would end without
	// ever reaching the grace timer.
	waitForFile(t, trapped)

	interruptCh <- struct{}{}

	// Taking the InterruptCh branch registers a SECOND waiter (the grace-period timer) via e.clk().After —
	// the original idle timer is only Stop()'d once run() returns, so both coexist here. BlockUntil(2) waits
	// for that second registration specifically; BlockUntil(1) would return immediately on the pre-existing
	// idle timer alone and could fire Advance before the grace timer ever registers.
	fc.BlockUntil(2)
	fc.Advance(interruptGraceForTest)

	select {
	case err := <-done:
		if !errors.Is(err, executor.ErrUserInterrupted) {
			t.Fatalf("expected ErrUserInterrupted, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunAgent did not return after the interrupt grace period should have elapsed")
	}

	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("subprocess ignored SIGINT and was not SIGKILLed after the grace period")
	}
}

// interruptGraceForTest mirrors executor.go's unexported interruptGracePeriod (15s) — duplicated here
// because the constant is not exported; advancing by exactly this much is what proves the grace period
// itself, not some unrelated larger duration.
const interruptGraceForTest = 15 * time.Second

// waitForFile fails the test unless path exists within a few seconds.
func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not appear", path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
