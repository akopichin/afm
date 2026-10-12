package worktree

import (
	"errors"
	"testing"
)

func TestTryLockFlow_ExclusiveAcrossFlows(t *testing.T) {
	dir := t.TempDir()

	a, err := TryLockFlow(dir, "flow-a")
	if err != nil {
		t.Fatalf("TryLockFlow(flow-a): %v", err)
	}
	defer a.Close()

	// A different flow name must not be blocked by flow-a's lock.
	b, err := TryLockFlow(dir, "flow-b")
	if err != nil {
		t.Fatalf("TryLockFlow(flow-b): %v", err)
	}
	defer b.Close()
}

func TestTryLockFlow_BusyWhileHeld(t *testing.T) {
	dir := t.TempDir()

	first, err := TryLockFlow(dir, "myflow")
	if err != nil {
		t.Fatalf("TryLockFlow (first): %v", err)
	}
	defer first.Close()

	_, err = TryLockFlow(dir, "myflow")
	if !errors.Is(err, ErrFlowLockBusy) {
		t.Fatalf("TryLockFlow (second, while held): got %v, want ErrFlowLockBusy", err)
	}
}

func TestTryLockFlow_AvailableAfterClose(t *testing.T) {
	dir := t.TempDir()

	first, err := TryLockFlow(dir, "myflow")
	if err != nil {
		t.Fatalf("TryLockFlow (first): %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := TryLockFlow(dir, "myflow")
	if err != nil {
		t.Fatalf("TryLockFlow (second, after release): %v", err)
	}
	defer second.Close()
}

func TestFlowLock_CloseOnNilIsNoop(t *testing.T) {
	var l *FlowLock
	if err := l.Close(); err != nil {
		t.Fatalf("Close on nil *FlowLock: %v", err)
	}
}
