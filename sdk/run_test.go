package afmsdk

import (
	"context"
	"testing"
)

func TestRun_PortAndPID(t *testing.T) {
	run := &Run{port: 4321, pid: 999}
	if got := run.Port(); got != 4321 {
		t.Errorf("Port(): got %d, want 4321", got)
	}
	if got := run.PID(); got != 999 {
		t.Errorf("PID(): got %d, want 999", got)
	}
}

func TestRun_WaitWithoutCmdReturnsError(t *testing.T) {
	run := &Run{}
	if err := run.Wait(context.Background()); err == nil {
		t.Fatal("Wait: expected an error for a Run with no cmd (as Attach produces)")
	}
}

func TestRun_CleanupWithoutCmdReturnsError(t *testing.T) {
	run := &Run{}
	if err := run.Cleanup(); err == nil {
		t.Fatal("Cleanup: expected an error for a Run with no cmd (as Attach produces)")
	}
}
