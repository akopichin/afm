//go:build unix

package serve

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/runregistry"
)

func (e *launchEnv) writeFlow(t *testing.T, file, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.project, ".afm", "flows", file), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// killLaunched kills every child the host recorded, so a test can wait for their reapers.
func killLaunched(h *Host) {
	receipts, _ := h.Receipts()
	for _, r := range receipts {
		if r.PID > 0 {
			_ = syscall.Kill(r.PID, syscall.SIGKILL)
		}
	}
}

func flowRequest(id, file string) StartRequest {
	return StartRequest{RequestID: id, Project: "app", Flow: file, Mode: ModeCore}
}

func wantAdmission(t *testing.T, err error, code string) {
	t.Helper()
	var adm *AdmissionError
	if !errors.As(err, &adm) || adm.Code != code {
		t.Fatalf("admission error = %v, want %s", err, code)
	}
}

// What a launch reserves is decided when it is accepted: later edits of the flow or of serve.yaml must not let a
// second run of the same flow into the same checkout.
func TestAdmissionKeepsTheIdentityAcceptedAtLaunch(t *testing.T) {
	t.Run("flow renamed after acceptance", func(t *testing.T) {
		e := newLaunchEnv(t, "hold", nil)
		first, err := e.Start(flowRequest("a", "dev.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		e.waitState(t, first.LaunchID, StateReady)
		e.writeFlow(t, "dev.yaml", "name: renamed\n")
		e.writeFlow(t, "other.yaml", "name: dev\n")
		_, err = e.Start(flowRequest("b", "other.yaml"))
		wantAdmission(t, err, CodeConflict)
	})
	t.Run("flow deleted after acceptance", func(t *testing.T) {
		e := newLaunchEnv(t, "hold", nil)
		first, err := e.Start(flowRequest("a", "dev.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		e.waitState(t, first.LaunchID, StateReady)
		if err := os.Remove(filepath.Join(e.project, ".afm", "flows", "dev.yaml")); err != nil {
			t.Fatal(err)
		}
		e.writeFlow(t, "other.yaml", "name: dev\n")
		_, err = e.Start(flowRequest("b", "other.yaml"))
		wantAdmission(t, err, CodeConflict)
	})
	t.Run("project remapped across a host restart", func(t *testing.T) {
		e := newLaunchEnv(t, "hold", nil)
		first, err := e.Start(devRequest("a"))
		if err != nil {
			t.Fatal(err)
		}
		e.waitState(t, first.LaunchID, StateReady)
		e.Close()
		cfg := e.cfg
		cfg.Config.Projects = []Project{{ID: "app", Path: t.TempDir()}, {ID: "orig", Path: e.project}}
		next := NewLauncher(cfg)
		t.Cleanup(next.Close)
		req := devRequest("b")
		req.Project = "orig"
		_, err = next.Start(req)
		wantAdmission(t, err, CodeConflict)
	})
	t.Run("the identity is in the first durable receipt", func(t *testing.T) {
		e := newLaunchEnv(t, "hold", nil)
		var onDisk Receipt
		e.accepted = func(r Receipt) {
			if err := readJSON(e.host.receiptPath(r.LaunchID), &onDisk); err != nil {
				t.Error(err)
			}
		}
		if _, err := e.Start(flowRequest("a", "dev.yaml")); err != nil {
			t.Fatal(err)
		}
		want := LaunchIdentity{Checkout: canonicalPath(e.project), RunName: "dev"}
		if onDisk.State != StateAccepted || onDisk.Identity != want {
			t.Errorf("receipt published before spawn = %+v, want identity %+v", onDisk, want)
		}
	})
}

func TestAdmissionTellsCheckoutsApart(t *testing.T) {
	t.Run("an explicit worktree is not the base checkout", func(t *testing.T) {
		e := newLaunchEnv(t, "hold", nil)
		first, err := e.Start(flowRequest("a", "dev.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		e.waitState(t, first.LaunchID, StateReady)
		inTree := devRequest("b")
		inTree.Worktree = "dev"
		if _, err := e.Start(inTree); err != nil {
			t.Fatalf("worktree dev next to the base checkout's dev: %v", err)
		}
		again := flowRequest("c", "dev.yaml")
		again.Worktree = "dev"
		_, err = e.Start(again)
		wantAdmission(t, err, CodeConflict)
	})
	t.Run("two spellings of one worktree", func(t *testing.T) {
		e := newLaunchEnv(t, "hold", func(c *LauncherConfig) {
			link := filepath.Join(t.TempDir(), "link")
			if err := os.Symlink(c.Config.Projects[0].Path, link); err != nil {
				t.Fatal(err)
			}
			c.Config.Projects = append(c.Config.Projects, Project{ID: "link", Path: link})
		})
		req := devRequest("a")
		req.Worktree = "w1"
		if _, err := e.Start(req); err != nil {
			t.Fatal(err)
		}
		via := devRequest("b")
		via.Project, via.Worktree = "link", "w1"
		_, err := e.Start(via)
		wantAdmission(t, err, CodeConflict)
	})
	t.Run("root_dir moves the worktree base", func(t *testing.T) {
		e := newLaunchEnv(t, "hold", nil)
		e.writeFlow(t, "sub.yaml", "name: sub\nroot_dir: sub\n")
		inSub := flowRequest("a", "sub.yaml")
		inSub.Worktree = "w1"
		if _, err := e.Start(inSub); err != nil {
			t.Fatal(err)
		}
		got, _ := e.host.Receipt(LaunchID("a"))
		if want := filepath.Join(canonicalPath(e.project), "sub", ".worktrees", "w1"); got.Identity.Checkout != want {
			t.Errorf("checkout = %s, want %s", got.Identity.Checkout, want)
		}
		atRoot := devRequest("b")
		atRoot.Worktree = "w1"
		if _, err := e.Start(atRoot); err != nil {
			t.Errorf("worktree w1 of another base: %v", err)
		}
	})
	t.Run("an unknown identity conflicts until its process is gone", func(t *testing.T) {
		e := newLaunchEnv(t, "hold", nil)
		params, _, _ := e.resolve(devRequest("legacy"))
		if _, _, err := e.host.CreateReceipt("legacy", params, LaunchIdentity{}, time.Now()); err != nil {
			t.Fatal(err)
		}
		other := flowRequest("b", "dev.yaml")
		other.Worktree = "elsewhere"
		_, err := e.Start(other)
		wantAdmission(t, err, CodeConflict)
	})
}

// A start that timed out gives back its starting slot, but a process that ignores the interrupt still runs: it
// keeps its running slot and its identity until its exit is seen, also across a host restart.
func TestTimedOutLaunchHoldsItsRunningSlotUntilItsProcessExits(t *testing.T) {
	e := newLaunchEnv(t, "stubborn", func(c *LauncherConfig) {
		c.ReadyTimeout = 300 * time.Millisecond
		c.Config.Limits = Limits{MaxRunning: 2, MaxStarting: 1}
	})
	first, err := e.Start(devRequest("a"))
	if err != nil {
		t.Fatal(err)
	}
	failed := e.waitState(t, first.LaunchID, StateFailed)
	if !strings.Contains(failed.Error, "did not become ready") || failed.ProcessGone {
		t.Fatalf("timed-out receipt = %+v", failed)
	}
	if err := syscall.Kill(failed.PID, 0); err != nil {
		t.Fatalf("the stubborn child must still run: %v", err)
	}
	_, err = e.Start(devRequest("same"))
	wantAdmission(t, err, CodeConflict)
	second := devRequest("b")
	second.Worktree = "w2"
	if _, err := e.Start(second); err != nil {
		t.Fatalf("the starting slot must be free after the timeout: %v", err)
	}
	third := devRequest("c")
	third.Worktree = "w3"
	e.waitState(t, LaunchID("b"), StateFailed)
	_, err = e.Start(third)
	wantAdmission(t, err, CodeLimitRunning)

	// A new host still counts the live process of the failed launch.
	e.Close()
	next := NewLauncher(e.cfg)
	t.Cleanup(func() {
		next.Close()
		killLaunched(e.host)
		next.reapers.Wait()
	})
	if err := next.Recover(); err != nil {
		t.Fatal(err)
	}
	_, err = next.Start(third)
	wantAdmission(t, err, CodeLimitRunning)

	if err := syscall.Kill(failed.PID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the exit of the timed-out process", func() bool {
		r, _ := e.host.Receipt(first.LaunchID)
		return r.ProcessGone
	})
	ended, _ := e.host.Receipt(first.LaunchID)
	if ended.State != StateFailed || ended.Error != failed.Error {
		t.Errorf("the exit must keep the timeout diagnosis: %+v", ended)
	}
	if _, err := next.Start(third); err != nil {
		t.Errorf("after the process exited its slot must be free: %v", err)
	}
}

// The timeout is decided on the receipt as it is when it is written: a launch that became ready meanwhile is
// neither failed nor signalled.
func TestTimeoutLeavesALaunchThatBecameReadyAlone(t *testing.T) {
	e := newLaunchEnv(t, "hold", func(c *LauncherConfig) { c.ReadyTimeout = time.Millisecond })
	sleeper := exec.Command("sleep", "60")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() })
	marker, _ := runregistry.ProcessStart(sleeper.Process.Pid)
	params, _, _ := e.resolve(devRequest("raced"))
	if _, _, err := e.host.CreateReceipt("raced", params, e.identityOf(params), time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	launchID := LaunchID("raced")
	if _, err := e.host.UpdateReceipt(launchID, time.Now(), func(r *Receipt) {
		r.State, r.PID, r.ProcessStart = StateReady, sleeper.Process.Pid, marker
	}); err != nil {
		t.Fatal(err)
	}
	e.timeOut(launchID) // a watcher that read the receipt while it was still starting
	got, _ := e.host.Receipt(launchID)
	if got.State != StateReady || got.Error != "" {
		t.Errorf("receipt after a stale timeout = %+v", got)
	}
	if err := syscall.Kill(sleeper.Process.Pid, 0); err != nil {
		t.Errorf("the ready process was signalled: %v", err)
	}
}

// A Docker run whose host-side afm exited while its container still answers keeps its slot; it ends once the
// container is gone too.
func TestDockerOrphanHoldsItsSlotUntilTheContainerIsGone(t *testing.T) {
	var answering atomic.Bool
	answering.Store(true)
	e := newLaunchEnv(t, "docker-orphan", func(c *LauncherConfig) {
		c.Config.Limits = Limits{MaxRunning: 1, MaxStarting: 1}
		c.Discovery.Probe = func(context.Context, string) (Runtime, error) {
			if !answering.Load() {
				return Runtime{}, errors.New("connection refused")
			}
			return Runtime{ProtocolVersion: 1, RunID: "fake-1", Ready: true, InstanceID: InstanceIDFor(LaunchID("orphan"))}, nil
		}
	})
	first, err := e.Start(devRequest("orphan"))
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the host-side exit to be reaped", func() bool {
		r, _ := e.host.Receipt(first.LaunchID)
		return r.ExitCode != nil && r.State == StateReady
	})
	second := devRequest("b")
	second.Worktree = "w2"
	_, err = e.Start(second)
	wantAdmission(t, err, CodeLimitRunning)

	answering.Store(false)
	ended := e.waitState(t, first.LaunchID, StateExited)
	if !ended.ProcessGone {
		t.Errorf("receipt = %+v", ended)
	}
	if _, err := e.Start(second); err != nil {
		t.Errorf("after the container is gone: %v", err)
	}
}
