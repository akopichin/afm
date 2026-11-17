package runregistry

import (
	"errors"
	"os"
	"os/exec"
	"testing"
)

func TestProcessStartIsStableAndDistinguishesProcesses(t *testing.T) {
	self, err := ProcessStart(os.Getpid())
	if errors.Is(err, ErrUnsupported) {
		t.Skip("process start marker is not supported on this platform")
	}
	if err != nil || self == "" {
		t.Fatalf("ProcessStart(self) = %q, %v", self, err)
	}
	if again, _ := ProcessStart(os.Getpid()); again != self {
		t.Errorf("marker changed between calls: %q then %q", self, again)
	}

	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	child, err := ProcessStart(cmd.Process.Pid)
	if err != nil || child == "" {
		t.Fatalf("ProcessStart(child) = %q, %v", child, err)
	}
	if child == self {
		t.Error("two processes have the same start marker")
	}
}

func TestProcessStartMissingProcess(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := ProcessStart(cmd.Process.Pid); err == nil || errors.Is(err, ErrUnsupported) {
		t.Errorf("ProcessStart(exited pid) error = %v, want a lookup failure", err)
	}
}
