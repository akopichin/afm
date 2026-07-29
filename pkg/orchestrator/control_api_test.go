package orchestrator

import (
	"context"
	"testing"
	"time"
)

func TestRetryHook_NoWaiterReturnsError(t *testing.T) {
	o := &Orchestrator{}
	if err := o.RetryHook("nonexistent"); err == nil {
		t.Error("expected error when no stage is waiting on a hook decision")
	}
}

func TestSkipHook_DeliversDecision(t *testing.T) {
	o := &Orchestrator{}
	done := make(chan hookDecision, 1)
	go func() {
		d, _ := o.waitForHookDecision(context.Background(), "s1")
		done <- d
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := o.SkipHook("s1"); err == nil {
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
		t.Fatal("timed out")
	}
}
