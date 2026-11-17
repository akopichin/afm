//go:build unix

package serve

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/runregistry"
)

type fakeRuns struct{ view RunView }

func (f fakeRuns) Resolve(_ context.Context, runID string) (RunView, error) {
	if runID != f.view.RunID {
		return RunView{}, ErrRunNotFound
	}
	return f.view, nil
}

type fakeStatus struct {
	mu     sync.Mutex
	status string
	err    error
}

func (f *fakeStatus) set(s string) { f.mu.Lock(); f.status = s; f.mu.Unlock() }

func (f *fakeStatus) Status(context.Context, RunView) (ChildStatus, json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return ChildStatus{RunID: "run-1", RunStatus: f.status}, nil, f.err
}

// startProcess runs a real process; ignoreInt makes it survive SIGINT so the escalation path is exercised.
func startProcess(t *testing.T, ignoreInt bool) (*exec.Cmd, string) {
	t.Helper()
	// exec: the process that is signalled must be the sleeper itself, not a shell waiting for it.
	script := "exec sleep 60"
	if ignoreInt {
		script = `trap '' INT; echo ready; while :; do sleep 0.1; done`
	}
	cmd := exec.Command("sh", "-c", script)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if ignoreInt {
		// The interrupt must arrive after the trap is installed.
		if _, err := bufio.NewReader(out).ReadString('\n'); err != nil {
			t.Fatal(err)
		}
	}
	// One goroutine reaps the child as soon as it dies, so a signalled process does not linger as a zombie.
	reaped := make(chan struct{})
	go func() { _ = cmd.Wait(); close(reaped) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-reaped })
	marker, err := runregistry.ProcessStart(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	return cmd, marker
}

func controllerFor(t *testing.T, view RunView, status string) (*Controller, *fakeStatus) {
	t.Helper()
	st := &fakeStatus{status: status}
	return &Controller{
		Runs: fakeRuns{view}, Status: st, Grace: 300 * time.Millisecond,
		Probe: func(context.Context, string) (Runtime, error) {
			return Runtime{ProtocolVersion: 1, RunID: "run-1", InstanceID: testInstance}, nil
		},
	}, st
}

func managedView(pid int, marker string) RunView {
	return RunView{
		RunID: "run-1", InstanceID: testInstance, Endpoint: "http://127.0.0.1:9001", Ownership: OwnershipManaged,
		Liveness: LiveActive, PID: pid, ProcessStart: marker,
	}
}

func processGoneNow(pid int) bool { return syscall.Kill(pid, 0) != nil }

// waitReaped lets the test's own reaper collect a signalled child: a zombie still answers signal 0.
func waitReaped(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	eventually(t, "the process to be reaped", func() bool {
		_, err := runregistry.ProcessStart(cmd.Process.Pid)
		return err != nil
	})
}

func TestCancelInterruptsAnActiveRun(t *testing.T) {
	cmd, marker := startProcess(t, false)
	c, _ := controllerFor(t, managedView(cmd.Process.Pid, marker), "running")
	got, err := c.Cancel(context.Background(), "run-1")
	if err != nil || got.State != StopStopped || got.Action != ActionCancel {
		t.Fatalf("Cancel = %+v, %v", got, err)
	}
	waitReaped(t, cmd)
}

func TestReleaseInterruptsAFinishedHeldRun(t *testing.T) {
	cmd, marker := startProcess(t, false)
	c, _ := controllerFor(t, managedView(cmd.Process.Pid, marker), "finished")
	got, err := c.Release(context.Background(), "run-1")
	if err != nil || got.State != StopStopped {
		t.Fatalf("Release = %+v, %v", got, err)
	}
	waitReaped(t, cmd)
}

func TestPreconditions(t *testing.T) {
	cmd, marker := startProcess(t, false)
	view := managedView(cmd.Process.Pid, marker)
	for _, tt := range []struct {
		name, status string
		call         func(*Controller) error
	}{
		{"cancel a finished run", "finished", func(c *Controller) error { _, err := c.Cancel(context.Background(), "run-1"); return err }},
		{"cancel a failed run", "failed", func(c *Controller) error { _, err := c.Cancel(context.Background(), "run-1"); return err }},
		{"release a running run", "running", func(c *Controller) error { _, err := c.Release(context.Background(), "run-1"); return err }},
		{"release a run that has not started", "", func(c *Controller) error { _, err := c.Release(context.Background(), "run-1"); return err }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := controllerFor(t, view, tt.status)
			var pre *PreconditionError
			if err := tt.call(c); !errors.As(err, &pre) {
				t.Errorf("error = %v, want PreconditionError", err)
			}
			if processGoneNow(cmd.Process.Pid) {
				t.Error("a refused action signalled the process")
			}
		})
	}
}

func TestStartMarkerMismatchSendsNoSignal(t *testing.T) {
	cmd, marker := startProcess(t, false)
	c, _ := controllerFor(t, managedView(cmd.Process.Pid, marker+"x"), "running")
	if _, err := c.Cancel(context.Background(), "run-1"); !errors.Is(err, errIdentityChanged) {
		t.Fatalf("Cancel = %v, want an identity failure", err)
	}
	if processGoneNow(cmd.Process.Pid) {
		t.Error("a process with another start marker was signalled")
	}
}

func TestEndpointOfAnotherInstanceSendsNoSignal(t *testing.T) {
	cmd, marker := startProcess(t, false)
	c, _ := controllerFor(t, managedView(cmd.Process.Pid, marker), "running")
	c.Probe = func(context.Context, string) (Runtime, error) {
		return Runtime{ProtocolVersion: 1, InstanceID: otherInstance}, nil
	}
	if _, err := c.Cancel(context.Background(), "run-1"); !errors.Is(err, errIdentityChanged) {
		t.Fatalf("Cancel = %v", err)
	}
	if processGoneNow(cmd.Process.Pid) {
		t.Error("the process was signalled although its endpoint answered as another instance")
	}
}

// A repeat that moved on after the status was read must not be stopped under the old run id.
func TestRunThatAdvancedSinceTheStatusIsNotSignalled(t *testing.T) {
	cmd, marker := startProcess(t, false)
	for _, action := range []string{ActionCancel, ActionRelease} {
		status := "running"
		if action == ActionRelease {
			status = "finished"
		}
		c, _ := controllerFor(t, managedView(cmd.Process.Pid, marker), status)
		c.Probe = func(context.Context, string) (Runtime, error) {
			return Runtime{ProtocolVersion: 1, RunID: "run-2", InstanceID: testInstance}, nil
		}
		var err error
		if action == ActionCancel {
			_, err = c.Cancel(context.Background(), "run-1")
		} else {
			_, err = c.Release(context.Background(), "run-1")
		}
		var changed *RunChangedError
		if !errors.As(err, &changed) || changed.CurrentRunID != "run-2" {
			t.Errorf("%s = %v, want run_changed to run-2", action, err)
		}
		if processGoneNow(cmd.Process.Pid) {
			t.Errorf("%s signalled the process serving run-2", action)
		}
	}
}

func TestAttachedRunsAreNeverSignalled(t *testing.T) {
	cmd, marker := startProcess(t, false)
	view := managedView(cmd.Process.Pid, marker)
	view.Ownership = OwnershipAttached
	c, _ := controllerFor(t, view, "running")
	if _, err := c.Cancel(context.Background(), "run-1"); !errors.Is(err, ErrNotManaged) {
		t.Errorf("Cancel = %v, want ErrNotManaged", err)
	}
	if processGoneNow(cmd.Process.Pid) {
		t.Error("an attached run was signalled")
	}
}

func TestNativeRunThatIgnoresInterruptIsKilledAfterTheGrace(t *testing.T) {
	cmd, marker := startProcess(t, true)
	c, _ := controllerFor(t, managedView(cmd.Process.Pid, marker), "running")
	got, err := c.Cancel(context.Background(), "run-1")
	if err != nil || got.State != StopKilled {
		t.Fatalf("Cancel = %+v, %v", got, err)
	}
	waitReaped(t, cmd)
}

func TestDockerRunIsNeverKilled(t *testing.T) {
	cmd, marker := startProcess(t, true)
	view := managedView(cmd.Process.Pid, marker)
	view.Docker = true
	c, _ := controllerFor(t, view, "running")
	var sent []syscall.Signal
	c.Signal = func(pid int, m string, sig syscall.Signal) error {
		sent = append(sent, sig)
		return signalProcess(pid, m, sig)
	}
	got, err := c.Cancel(context.Background(), "run-1")
	if err != nil || got.State != StopStopPending {
		t.Fatalf("Cancel = %+v, %v", got, err)
	}
	if len(sent) != 1 || sent[0] != syscall.SIGINT {
		t.Errorf("signals = %v, want only SIGINT", sent)
	}
	if processGoneNow(cmd.Process.Pid) {
		t.Error("the Docker run's process died")
	}
}

func TestKillIsRecheckedAgainstTheMarker(t *testing.T) {
	cmd, marker := startProcess(t, true)
	c, _ := controllerFor(t, managedView(cmd.Process.Pid, marker), "running")
	var sent []syscall.Signal
	c.Signal = func(pid int, m string, sig syscall.Signal) error {
		sent = append(sent, sig)
		if sig == syscall.SIGKILL {
			return errIdentityChanged // the pid was reused between the interrupt and the kill
		}
		return signalProcess(pid, m, sig)
	}
	if _, err := c.Cancel(context.Background(), "run-1"); !errors.Is(err, errIdentityChanged) {
		t.Errorf("Cancel = %v", err)
	}
	if processGoneNow(cmd.Process.Pid) {
		t.Error("the process was killed although the identity check failed")
	}
}

func TestSweepReleasesAfterContinuousObservation(t *testing.T) {
	cmd, marker := startProcess(t, false)
	view := managedView(cmd.Process.Pid, marker)
	c, st := controllerFor(t, view, "finished")
	c.ReleaseAfter = time.Hour
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c.Now = func() time.Time { return clock }
	ctx := context.Background()
	views := []RunView{view}

	if got := c.Sweep(ctx, views); len(got) != 0 {
		t.Fatalf("first sweep released %v", got)
	}
	clock = clock.Add(59 * time.Minute)
	// The run was seen working again: the clock restarts, so a later finish must hold for the full hour.
	st.set("running")
	c.Sweep(ctx, views)
	st.set("finished")
	clock = clock.Add(2 * time.Minute)
	if got := c.Sweep(ctx, views); len(got) != 0 {
		t.Fatalf("released %v although the hold was interrupted", got)
	}
	clock = clock.Add(61 * time.Minute)
	got := c.Sweep(ctx, views)
	if len(got) != 1 || got[0] != "run-1" {
		t.Fatalf("sweep after the hold = %v", got)
	}
	waitReaped(t, cmd)
}

func TestSweepIsOffByDefault(t *testing.T) {
	cmd, marker := startProcess(t, false)
	view := managedView(cmd.Process.Pid, marker)
	c, _ := controllerFor(t, view, "finished")
	if got := c.Sweep(context.Background(), []RunView{view}); got != nil || processGoneNow(cmd.Process.Pid) {
		t.Errorf("sweep with retention off: %v", got)
	}
}
