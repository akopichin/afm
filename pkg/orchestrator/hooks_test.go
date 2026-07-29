package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestRunScriptWithRetry_SucceedsOnThirdAttempt(t *testing.T) {
	attempts := 0
	err := runScriptWithRetry(context.Background(), func() error {
		attempts++
		if attempts < 3 {
			return errors.New("boom")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
}

func TestRunScriptWithRetry_ExhaustsAfterFourAttempts(t *testing.T) {
	attempts := 0
	err := runScriptWithRetry(context.Background(), func() error {
		attempts++
		return errors.New("always fails")
	})
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if attempts != hookMaxRetries+1 {
		t.Errorf("attempts = %d, want %d", attempts, hookMaxRetries+1)
	}
}

func TestRunScriptWithRetry_RespectsBackoffTiming(t *testing.T) {
	attempts := 0
	start := time.Now()
	_ = runScriptWithRetry(context.Background(), func() error {
		attempts++
		return errors.New("fail")
	})
	elapsed := time.Since(start)
	// 1s + 2s + 3s = 6s minimum between the 4 attempts.
	if elapsed < 6*time.Second {
		t.Errorf("elapsed = %v, want >= 6s (backoff not respected)", elapsed)
	}
}

func TestHookPending_WriteReadClear(t *testing.T) {
	dir := t.TempDir()
	p := hookPending{Hook: "before", Script: "echo hi", Timeout: 30 * time.Second}
	if err := writeHookPending(dir, p); err != nil {
		t.Fatalf("writeHookPending: %v", err)
	}
	got, ok := readHookPending(dir)
	if !ok {
		t.Fatal("readHookPending: not found")
	}
	if got != p {
		t.Errorf("readHookPending = %+v, want %+v", got, p)
	}
	clearHookPending(dir)
	if _, ok := readHookPending(dir); ok {
		t.Error("expected pending to be cleared")
	}
}

func TestWaitForHookDecision_ResolveDelivers(t *testing.T) {
	o := &Orchestrator{}
	done := make(chan hookDecision, 1)
	go func() {
		d, ok := o.waitForHookDecision(context.Background(), "s1")
		if !ok {
			t.Error("expected ok=true")
		}
		done <- d
	}()

	// Give the goroutine a moment to register the waiter.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if o.resolveHook("s1", hookDecisionSkip) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	select {
	case d := <-done:
		if d != hookDecisionSkip {
			t.Errorf("decision = %v, want skip", d)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for decision to be delivered")
	}
}

func TestWaitForHookDecision_CtxCancelReturnsFalse(t *testing.T) {
	o := &Orchestrator{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, ok := o.waitForHookDecision(ctx, "s1")
	if ok {
		t.Error("expected ok=false when ctx is already cancelled")
	}
}

func TestResolveHook_NoWaiterReturnsFalse(t *testing.T) {
	o := &Orchestrator{}
	if o.resolveHook("nonexistent", hookDecisionRetry) {
		t.Error("expected false when no stage is waiting")
	}
}

var _ = filepath.Join // silence unused import if not otherwise used
