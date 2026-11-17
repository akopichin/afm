//go:build unix

package serve

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/mcpserver"
	"github.com/akopichin/afm/pkg/runregistry"
)

type launchEnv struct {
	*Launcher
	host    *Host
	out     string
	regDir  string
	project string
	cfg     LauncherConfig
}

func (e *launchEnv) env(behavior string) []string {
	return []string{
		"PATH=" + os.Getenv("PATH"), fakeEnv + "=1", "SERVE_FAKE_OUT=" + e.out, "SERVE_FAKE_REGISTRY=" + e.regDir,
		"SERVE_FAKE_RUNS_DIR=" + filepath.Join(e.project, ".afm", "runs"), "SERVE_FAKE_BEHAVIOR=" + behavior,
		// Everything below must not reach the child.
		"AFM_IN_DOCKER=1", "AFM_STAGE_DIR=/somewhere", "AFM_SECRET_TOKEN=hunter2",
	}
}

func newLaunchEnv(t *testing.T, behavior string, mutate func(*LauncherConfig)) *launchEnv {
	t.Helper()
	host, _ := acquire(t)
	e := &launchEnv{host: host, out: filepath.Join(t.TempDir(), "out.jsonl"), regDir: t.TempDir(), project: t.TempDir()}
	flows := filepath.Join(e.project, ".afm", "flows")
	if err := os.MkdirAll(flows, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(flows, "dev.yaml"), []byte("name: dev\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.cfg = LauncherConfig{
		Host: host,
		Config: Config{
			Projects:  []Project{{ID: "app", Path: e.project}},
			Pipelines: []Pipeline{{ID: "dev", Source: "/pipelines/dev"}},
			Limits:    Limits{MaxRunning: 4, MaxStarting: 2},
		},
		Discovery:      &Discovery{RegistryDir: e.regDir, Now: time.Now},
		Groups:         mcpserver.AllGroups().Names(),
		Binary:         os.Args[0],
		Env:            e.env(behavior),
		PollInterval:   20 * time.Millisecond,
		UncertainAfter: 300 * time.Millisecond,
	}
	if mutate != nil {
		mutate(&e.cfg)
	}
	e.Launcher = NewLauncher(e.cfg)
	t.Cleanup(func() {
		e.Close()
		receipts, _ := host.Receipts()
		for _, r := range receipts {
			if r.PID > 0 {
				_ = syscall.Kill(r.PID, syscall.SIGKILL)
			}
		}
		e.reapers.Wait()
	})
	return e
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (e *launchEnv) waitState(t *testing.T, launchID string, states ...string) Receipt {
	t.Helper()
	var last Receipt
	eventually(t, "receipt state "+strings.Join(states, "|"), func() bool {
		last, _ = e.host.Receipt(launchID)
		return slices.Contains(states, last.State)
	})
	return last
}

// resolve is what Start checks and builds for a new launch.
func (l *Launcher) resolve(req StartRequest) (LaunchParams, []string, error) {
	params, err := l.canonical(req)
	if err != nil {
		return LaunchParams{}, nil, err
	}
	argv, err := l.prepare(req, params)
	return params, argv, err
}

func devRequest(id string) StartRequest {
	return StartRequest{RequestID: id, Project: "app", Pipeline: "dev", Mode: ModeCore}
}

func TestStartSpawnsADetachedChildWithTypedArgv(t *testing.T) {
	e := newLaunchEnv(t, "hold", nil)
	req := devRequest("req-1")
	req.Task = "ship the feature\nwith care"
	req.Worktree = "feat-1"
	req.MCPAllow = []string{"reply", "stages"}

	receipt, err := e.Start(req)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State != StateStarting || receipt.PID == 0 || receipt.InstanceID != InstanceIDFor(receipt.LaunchID) {
		t.Fatalf("receipt right after start = %+v", receipt)
	}
	ready := e.waitState(t, receipt.LaunchID, StateReady)
	if ready.RunID != "fake-1" || ready.RunsDir != filepath.Join(e.project, ".afm", "runs") {
		t.Errorf("ready receipt = %+v", ready)
	}

	reports := readReports(t, e.out)
	if len(reports) != 1 {
		t.Fatalf("children started = %d, want 1", len(reports))
	}
	rep := reports[0]
	wantArgs := []string{
		"run", "--dir", e.project, "--mode", "core", "--listen-any-port",
		"--instance-id", receipt.InstanceID, "--launch-id", receipt.LaunchID,
		"--mcp-allow", "reply,stages",
		"--task", "@" + e.host.TaskPath(receipt.LaunchID), "--worktree", "feat-1",
		"--pipeline", "/pipelines/dev",
	}
	if !slices.Equal(rep.Args, wantArgs) {
		t.Errorf("argv =\n  %v\nwant\n  %v", rep.Args, wantArgs)
	}
	if strings.Contains(strings.Join(rep.Args, " "), "ship the feature") {
		t.Error("the task text must not appear in argv")
	}
	if rep.Task != req.Task {
		t.Errorf("the child read task %q from the task file", rep.Task)
	}
	if rep.Cwd != e.project {
		t.Errorf("cwd = %s, want the project", rep.Cwd)
	}
	for _, kv := range rep.Env {
		if strings.HasPrefix(kv, "AFM_") {
			t.Errorf("the child inherited %s", strings.SplitN(kv, "=", 2)[0])
		}
	}
	if !rep.StdinEOF || rep.Sid != rep.PID {
		t.Errorf("child must have stdin /dev/null and its own session: %+v", rep)
	}
	info, err := os.Stat(e.host.LogPath(receipt.LaunchID))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("launch log: %v %v", info, err)
	}
	// The prompt is only needed until the child is ready.
	eventually(t, "task file removal", func() bool {
		_, err := os.Stat(e.host.TaskPath(receipt.LaunchID))
		return errors.Is(err, os.ErrNotExist)
	})
}

func TestStartUsesFlowFile(t *testing.T) {
	e := newLaunchEnv(t, "hold", nil)
	req := devRequest("req-flow")
	req.Pipeline, req.Flow = "", "dev.yaml"
	receipt, err := e.Start(req)
	if err != nil {
		t.Fatal(err)
	}
	e.waitState(t, receipt.LaunchID, StateReady)
	rep := readReports(t, e.out)[0]
	if got := rep.Args[len(rep.Args)-1]; got != filepath.Join(e.project, ".afm", "flows", "dev.yaml") {
		t.Errorf("flow argument = %s", got)
	}
	if slices.Contains(rep.Args, "--task") || slices.Contains(rep.Args, "--worktree") {
		t.Errorf("optional flags leaked into %v", rep.Args)
	}
}

func TestStartIsIdempotentPerRequestID(t *testing.T) {
	e := newLaunchEnv(t, "hold", nil)
	first, err := e.Start(devRequest("same"))
	if err != nil {
		t.Fatal(err)
	}
	e.waitState(t, first.LaunchID, StateReady)
	again, err := e.Start(devRequest("same"))
	if err != nil || again.LaunchID != first.LaunchID || again.PID != first.PID {
		t.Fatalf("repeat = %+v, %v", again, err)
	}
	other := devRequest("same")
	other.Mode = ModeDashboard
	if _, err := e.Start(other); !errors.Is(err, ErrRequestConflict) {
		t.Errorf("same request_id with other parameters: %v", err)
	}
	if n := len(readReports(t, e.out)); n != 1 {
		t.Errorf("children started = %d, want 1", n)
	}
}

func TestStartRefusesInvalidRequestsWithoutAReceipt(t *testing.T) {
	e := newLaunchEnv(t, "hold", nil)
	bad := map[string]func(*StartRequest){
		"no request id":       func(r *StartRequest) { r.RequestID = "" },
		"bad request id":      func(r *StartRequest) { r.RequestID = "a b" },
		"unknown project":     func(r *StartRequest) { r.Project = "nope" },
		"no mode":             func(r *StartRequest) { r.Mode = "" },
		"unknown mode":        func(r *StartRequest) { r.Mode = "tui" },
		"unknown pipeline":    func(r *StartRequest) { r.Pipeline = "nope" },
		"pipeline and flow":   func(r *StartRequest) { r.Flow = "dev.yaml" },
		"neither":             func(r *StartRequest) { r.Pipeline = "" },
		"flow outside":        func(r *StartRequest) { r.Pipeline, r.Flow = "", "../../etc/passwd" },
		"flow not a yaml":     func(r *StartRequest) { r.Pipeline, r.Flow = "", "dev.txt" },
		"flow missing":        func(r *StartRequest) { r.Pipeline, r.Flow = "", "absent.yaml" },
		"worktree path":       func(r *StartRequest) { r.Worktree = "../x" },
		"blank task":          func(r *StartRequest) { r.Task = " \n" },
		"task not utf8":       func(r *StartRequest) { r.Task = "\xff\xfe" },
		"task too large":      func(r *StartRequest) { r.Task = strings.Repeat("x", maxTaskBytes+1) },
		"unknown group":       func(r *StartRequest) { r.MCPAllow = []string{"root"} },
		"read with a group":   func(r *StartRequest) { r.MCPAllow = []string{"read", "reply"} },
		"request id too long": func(r *StartRequest) { r.RequestID = strings.Repeat("a", maxRequestIDBytes+1) },
	}
	for name, mutate := range bad {
		req := devRequest("req-bad")
		mutate(&req)
		if _, err := e.Start(req); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%s: error = %v, want ErrInvalidRequest", name, err)
		}
	}
	if receipts, _ := e.host.Receipts(); len(receipts) != 0 {
		t.Errorf("refused requests left receipts: %+v", receipts)
	}
	if n := len(readReports(t, e.out)); n != 0 {
		t.Errorf("refused requests started %d children", n)
	}
}

func TestStartLimitsGroupsToWhatTheHostAllows(t *testing.T) {
	e := newLaunchEnv(t, "hold", func(c *LauncherConfig) { c.Groups = []mcpserver.Group{"reply"} })
	req := devRequest("g1")
	req.MCPAllow = []string{"reply", "stages"}
	if _, err := e.Start(req); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("group beyond the host's: %v", err)
	}
	req.RequestID, req.MCPAllow = "g2", []string{"read"}
	receipt, err := e.Start(req)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(receipt.Params.MCPAllow, []string{"read"}) {
		t.Errorf("params = %+v", receipt.Params)
	}
	// Without an explicit list the child gets what the host allows.
	req.RequestID, req.MCPAllow, req.Worktree = "g3", nil, "other"
	receipt, err = e.Start(req)
	if err != nil || !slices.Equal(receipt.Params.MCPAllow, []string{"reply"}) {
		t.Errorf("default groups: %+v, %v", receipt.Params, err)
	}
}

func TestChildCrashBeforeReadyFailsTheLaunch(t *testing.T) {
	e := newLaunchEnv(t, "crash", nil)
	req := devRequest("crash")
	req.Task = "do it"
	receipt, err := e.Start(req)
	if err != nil {
		t.Fatal(err)
	}
	failed := e.waitState(t, receipt.LaunchID, StateFailed)
	if failed.ExitCode == nil || *failed.ExitCode != 3 || !strings.Contains(failed.Error, "before it was ready") {
		t.Errorf("failed receipt = %+v", failed)
	}
	if _, err := os.Stat(e.host.TaskPath(receipt.LaunchID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("task file of a failed launch: %v", err)
	}
}

func TestChildExitAfterReadyEndsTheLaunchWithItsExitCode(t *testing.T) {
	e := newLaunchEnv(t, "finish", nil)
	receipt, err := e.Start(devRequest("fin"))
	if err != nil {
		t.Fatal(err)
	}
	done := e.waitState(t, receipt.LaunchID, StateExited)
	eventually(t, "exit code", func() bool {
		done, _ = e.host.Receipt(receipt.LaunchID)
		return done.ExitCode != nil
	})
	if *done.ExitCode != 0 {
		t.Errorf("exit code = %d", *done.ExitCode)
	}
}

func TestSpawnFailureIsRecorded(t *testing.T) {
	e := newLaunchEnv(t, "hold", func(c *LauncherConfig) { c.Binary = "/nonexistent/afm" })
	receipt, err := e.Start(devRequest("nospawn"))
	if err == nil {
		t.Fatal("want a spawn error")
	}
	if receipt.State != StateFailed || !strings.Contains(receipt.Error, "/nonexistent/afm") {
		t.Errorf("receipt = %+v", receipt)
	}
	// The request_id is spent: the same request returns the failed receipt instead of retrying blindly.
	again, err := e.Start(devRequest("nospawn"))
	if err != nil || again.State != StateFailed {
		t.Errorf("repeat = %+v, %v", again, err)
	}
}

func TestStartTimeoutFailsAndStopsTheChild(t *testing.T) {
	e := newLaunchEnv(t, "silent", func(c *LauncherConfig) { c.ReadyTimeout = 200 * time.Millisecond })
	receipt, err := e.Start(devRequest("slow"))
	if err != nil {
		t.Fatal(err)
	}
	failed := e.waitState(t, receipt.LaunchID, StateFailed)
	if !strings.Contains(failed.Error, "did not become ready") {
		t.Errorf("error = %q", failed.Error)
	}
	eventually(t, "the child to stop", func() bool {
		_, err := runregistry.ProcessStart(receipt.PID)
		return err != nil
	})
}

func TestAdmissionLimits(t *testing.T) {
	t.Run("max_starting", func(t *testing.T) {
		e := newLaunchEnv(t, "silent", func(c *LauncherConfig) { c.Config.Limits = Limits{MaxRunning: 4, MaxStarting: 1} })
		if _, err := e.Start(devRequest("a")); err != nil {
			t.Fatal(err)
		}
		second := devRequest("b")
		second.Worktree = "w2"
		var adm *AdmissionError
		if _, err := e.Start(second); !errors.As(err, &adm) || adm.Code != CodeLimitStarting {
			t.Fatalf("second start while one is starting: %v", err)
		}
		if _, err := e.host.Receipt(LaunchID("b")); err == nil {
			t.Error("a refused launch must not leave a receipt")
		}
	})
	t.Run("max_running", func(t *testing.T) {
		e := newLaunchEnv(t, "hold", func(c *LauncherConfig) { c.Config.Limits = Limits{MaxRunning: 1, MaxStarting: 2} })
		first, err := e.Start(devRequest("a"))
		if err != nil {
			t.Fatal(err)
		}
		e.waitState(t, first.LaunchID, StateReady)
		second := devRequest("b")
		second.Worktree = "w2"
		var adm *AdmissionError
		if _, err := e.Start(second); !errors.As(err, &adm) || adm.Code != CodeLimitRunning {
			t.Fatalf("second start at the running limit: %v", err)
		}
	})
	t.Run("conflict without a separate worktree", func(t *testing.T) {
		e := newLaunchEnv(t, "hold", nil)
		first, err := e.Start(devRequest("a"))
		if err != nil {
			t.Fatal(err)
		}
		e.waitState(t, first.LaunchID, StateReady)
		var adm *AdmissionError
		if _, err := e.Start(devRequest("b")); !errors.As(err, &adm) || adm.Code != CodeConflict {
			t.Fatalf("same flow in the same checkout: %v", err)
		}
		withTree := devRequest("c")
		withTree.Worktree = "w3"
		if _, err := e.Start(withTree); err != nil {
			t.Errorf("a distinct worktree must be admitted: %v", err)
		}
	})
	t.Run("a finished launch frees its slot", func(t *testing.T) {
		e := newLaunchEnv(t, "finish", func(c *LauncherConfig) { c.Config.Limits = Limits{MaxRunning: 1, MaxStarting: 1} })
		first, err := e.Start(devRequest("a"))
		if err != nil {
			t.Fatal(err)
		}
		e.waitState(t, first.LaunchID, StateExited)
		if _, err := e.Start(devRequest("b")); err != nil {
			t.Errorf("after the first exited: %v", err)
		}
	})
}

// Recovery: the host died in the middle of a start and a new one takes over. Nothing is ever respawned.
func TestRecoverUnknownLaunchIsLostNotRestarted(t *testing.T) {
	e := newLaunchEnv(t, "hold", nil)
	req := devRequest("window")
	params, _, err := e.resolve(req)
	if err != nil {
		t.Fatal(err)
	}
	// Crash window: the receipt exists, the spawn never happened (or its pid was never written).
	if _, _, err := e.host.CreateReceipt(req.RequestID, params, e.identityOf(params), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := e.Recover(); err != nil {
		t.Fatal(err)
	}
	lost := e.waitState(t, LaunchID(req.RequestID), StateLost)
	if !strings.Contains(lost.Error, "not restarted") || lost.PID != 0 {
		t.Errorf("receipt = %+v", lost)
	}
	if n := len(readReports(t, e.out)); n != 0 {
		t.Errorf("recovery spawned %d children", n)
	}
}

func TestRecoverAdoptsAnUncertainLaunchThroughItsEarlyRecord(t *testing.T) {
	e := newLaunchEnv(t, "hold", func(c *LauncherConfig) { c.UncertainAfter = time.Hour })
	req := devRequest("adopt")
	params, _, err := e.resolve(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.host.CreateReceipt(req.RequestID, params, e.identityOf(params), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := e.Recover(); err != nil {
		t.Fatal(err)
	}
	uncertain := e.waitState(t, LaunchID(req.RequestID), StateUncertain)
	if uncertain.PID != 0 {
		t.Fatalf("receipt = %+v", uncertain)
	}

	// The child had been spawned by the dead host after all and now reports itself.
	_, argv, _ := e.resolve(req)
	cmd := exec.Command(os.Args[0], argv...) //nolint:gosec // the test binary is the fake afm
	cmd.Env = e.env("hold")
	cmd.Dir = e.project
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	ready := e.waitState(t, LaunchID(req.RequestID), StateReady)
	if ready.PID != cmd.Process.Pid || ready.ProcessStart == "" || ready.RunID != "fake-1" {
		t.Errorf("adopted receipt = %+v (child pid %d)", ready, cmd.Process.Pid)
	}
	if n := len(readReports(t, e.out)); n != 1 {
		t.Errorf("children = %d: recovery must not start another", n)
	}
}

func TestRecoverFollowsRunningLaunchesAndEndsDeadOnes(t *testing.T) {
	e := newLaunchEnv(t, "hold", nil)
	live := devRequest("live")
	params, argv, _ := e.resolve(live)
	cmd := exec.Command(os.Args[0], argv...) //nolint:gosec // the test binary is the fake afm
	cmd.Env, cmd.Dir = e.env("hold"), e.project
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	marker, _ := runregistry.ProcessStart(cmd.Process.Pid)
	mustReceipt := func(id string, mutate func(*Receipt)) {
		t.Helper()
		p := params
		p.Worktree = id // distinct params per launch
		if _, _, err := e.host.CreateReceipt(id, p, e.identityOf(p), time.Now()); err != nil {
			t.Fatal(err)
		}
		if _, err := e.host.UpdateReceipt(LaunchID(id), time.Now(), mutate); err != nil {
			t.Fatal(err)
		}
	}
	mustReceipt("live", func(r *Receipt) {
		r.State, r.PID, r.ProcessStart, r.InstanceID = StateStarting, cmd.Process.Pid, marker, InstanceIDFor(r.LaunchID)
	})
	// A launch recorded as starting whose process is gone, and one recorded as ready whose process is gone.
	gone := exec.Command("true")
	_ = gone.Run()
	for id, state := range map[string]string{"dead-starting": StateStarting, "dead-ready": StateReady} {
		mustReceipt(id, func(r *Receipt) { r.State, r.PID, r.ProcessStart = state, gone.Process.Pid, "old-marker" })
	}
	if err := e.Recover(); err != nil {
		t.Fatal(err)
	}
	if got := e.waitState(t, LaunchID("dead-starting"), StateFailed); got.Error == "" {
		t.Error("a launch that died before ready needs an error")
	}
	e.waitState(t, LaunchID("dead-ready"), StateExited)
	// The live child reports itself under the instance id of its launch, so the launch is followed to ready.
	e.waitState(t, LaunchID("live"), StateReady)
}

func TestRecoverRemovesPromptsWithoutAReceipt(t *testing.T) {
	e := newLaunchEnv(t, "hold", nil)
	orphan := e.host.TaskPath(LaunchID("never-accepted"))
	if err := os.WriteFile(orphan, []byte("secret prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.Recover(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("orphan prompt still on disk: %v", err)
	}
}

func TestStartedChildSurvivesTheLauncher(t *testing.T) {
	e := newLaunchEnv(t, "hold", nil)
	receipt, err := e.Start(devRequest("outlive"))
	if err != nil {
		t.Fatal(err)
	}
	e.waitState(t, receipt.LaunchID, StateReady)
	e.Close()
	if err := syscall.Kill(receipt.PID, 0); err != nil {
		t.Errorf("the child died with the launcher: %v", err)
	}
	if got, _ := e.host.Receipt(receipt.LaunchID); got.State != StateReady {
		t.Errorf("closing the launcher changed the receipt to %s", got.State)
	}
}

// A host that released its lock still reaps its children, but writes nothing: the next host may have saved a
// cycle the retired one never saw.
func TestRetiredReaperLeavesTheNextHostsReceipt(t *testing.T) {
	var hold atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	e := newLaunchEnv(t, "hold", func(c *LauncherConfig) {
		c.Now = func() time.Time {
			// Once armed, the next reading of the clock is the reaper's, right before it writes.
			if hold.CompareAndSwap(true, false) {
				close(entered)
				<-release
			}
			return time.Now()
		}
	})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseOnce)
	receipt, err := e.Start(devRequest("retired"))
	if err != nil {
		t.Fatal(err)
	}
	e.waitState(t, receipt.LaunchID, StateReady)
	e.Close() // the watchers stop; the reaper keeps waiting for the child
	hold.Store(true)
	if err := syscall.Kill(receipt.PID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the reaper never got to its write")
	}
	if err := e.host.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := AcquireHost(e.host.dir, 7, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if _, err := next.UpdateReceipt(receipt.LaunchID, time.Now(), func(r *Receipt) {
		r.setBinding(mergeRunBinding(r.binding(), r.RunID, runBinding{RunID: "fake-2", PreviousRunIDs: []string{r.RunID}}))
	}); err != nil {
		t.Fatal(err)
	}
	saved, err := next.Receipt(receipt.LaunchID)
	if err != nil {
		t.Fatal(err)
	}
	releaseOnce()
	reaped := make(chan struct{})
	go func() { e.reapers.Wait(); close(reaped) }()
	select {
	case <-reaped:
	case <-time.After(10 * time.Second):
		t.Fatal("the reaper did not finish")
	}
	if got, _ := next.Receipt(receipt.LaunchID); !reflect.DeepEqual(got, saved) {
		t.Errorf("the retired host wrote after it released the lock:\n got %+v\nwant %+v", got, saved)
	}
}

func TestHostWithoutChildGroupsStartsReadOnlyRuns(t *testing.T) {
	e := newLaunchEnv(t, "hold", func(c *LauncherConfig) { c.Groups = nil })
	receipt, err := e.Start(devRequest("ro"))
	if err != nil {
		t.Fatal(err)
	}
	e.waitState(t, receipt.LaunchID, StateReady)
	if !slices.Equal(receipt.Params.MCPAllow, []string{"read"}) {
		t.Errorf("a host that allows no write group gave the child %v, want [read]", receipt.Params.MCPAllow)
	}
	var argv []string
	for _, r := range readReports(t, e.out) {
		argv = r.Args
	}
	if i := slices.Index(argv, "--mcp-allow"); i < 0 || argv[i+1] != "read" {
		t.Errorf("child argv = %v, want --mcp-allow read", argv)
	}
	req := devRequest("ro2")
	req.Worktree, req.MCPAllow = "w2", []string{"approve"}
	if _, err := e.Start(req); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("a group the host lacks: %v", err)
	}
}

func TestAdmissionComparesCheckoutAndRunName(t *testing.T) {
	t.Run("two project ids for one directory", func(t *testing.T) {
		e := newLaunchEnv(t, "hold", func(c *LauncherConfig) {
			c.Config.Projects = append(c.Config.Projects, Project{ID: "alias", Path: c.Config.Projects[0].Path})
		})
		first, err := e.Start(devRequest("a"))
		if err != nil {
			t.Fatal(err)
		}
		e.waitState(t, first.LaunchID, StateReady)
		via := devRequest("b")
		via.Project = "alias"
		var adm *AdmissionError
		if _, err := e.Start(via); !errors.As(err, &adm) || adm.Code != CodeConflict {
			t.Fatalf("the same flow through a project alias: %v", err)
		}
		if n := len(readReports(t, e.out)); n != 1 {
			t.Errorf("children = %d, want 1", n)
		}
	})
	t.Run("two flow files with one name", func(t *testing.T) {
		e := newLaunchEnv(t, "hold", nil)
		other := filepath.Join(e.project, ".afm", "flows", "other.yaml")
		if err := os.WriteFile(other, []byte("name: dev\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		first := StartRequest{RequestID: "a", Project: "app", Flow: "dev.yaml", Mode: ModeCore}
		receipt, err := e.Start(first)
		if err != nil {
			t.Fatal(err)
		}
		e.waitState(t, receipt.LaunchID, StateReady)
		second := StartRequest{RequestID: "b", Project: "app", Flow: "other.yaml", Mode: ModeCore}
		var adm *AdmissionError
		if _, err := e.Start(second); !errors.As(err, &adm) || adm.Code != CodeConflict {
			t.Fatalf("another file declaring the same run name: %v", err)
		}
	})
	t.Run("a worktree is a separate run name", func(t *testing.T) {
		e := newLaunchEnv(t, "hold", nil)
		receipt, err := e.Start(devRequest("a"))
		if err != nil {
			t.Fatal(err)
		}
		e.waitState(t, receipt.LaunchID, StateReady)
		req := devRequest("b")
		req.Worktree = "w2"
		if _, err := e.Start(req); err != nil {
			t.Errorf("a distinct worktree must be admitted: %v", err)
		}
	})
}

// The child may have been spawned before the host died while it still prepares its pipeline: its record has a
// live process but no endpoint. The launch must follow that process, not be given up as lost.
func TestRecoverAdoptsALiveEarlyRecordWithoutEndpoint(t *testing.T) {
	e := newLaunchEnv(t, "early", func(c *LauncherConfig) { c.UncertainAfter = 200 * time.Millisecond })
	req := devRequest("early")
	params, argv, err := e.resolve(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.host.CreateReceipt(req.RequestID, params, e.identityOf(params), time.Now()); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], argv...) //nolint:gosec // the test binary is the fake afm
	cmd.Env = e.env("early")
	cmd.Dir = e.project
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if err := e.Recover(); err != nil {
		t.Fatal(err)
	}
	launchID := LaunchID(req.RequestID)
	eventually(t, "the early record to be adopted", func() bool {
		r, _ := e.host.Receipt(launchID)
		return r.PID == cmd.Process.Pid
	})
	time.Sleep(3 * e.cfg.UncertainAfter) // past the point where an unknown launch would be written off
	got, _ := e.host.Receipt(launchID)
	if got.State != StateStarting || got.PID != cmd.Process.Pid {
		t.Errorf("receipt = %+v, want starting with the child's pid", got)
	}
}

// A reachable endpoint that never reports ready must not hold a max_starting slot forever.
func TestUnreadyEndpointFailsAtTheReadyDeadline(t *testing.T) {
	e := newLaunchEnv(t, "unready", func(c *LauncherConfig) {
		c.ReadyTimeout = 300 * time.Millisecond
		c.Config.Limits = Limits{MaxRunning: 4, MaxStarting: 1}
	})
	receipt, err := e.Start(devRequest("slowready"))
	if err != nil {
		t.Fatal(err)
	}
	failed := e.waitState(t, receipt.LaunchID, StateFailed)
	if !strings.Contains(failed.Error, "did not become ready") {
		t.Errorf("error = %q", failed.Error)
	}
	req := devRequest("next")
	req.Worktree = "w2"
	if _, err := e.Start(req); err != nil {
		t.Errorf("the starting slot must be free after the deadline: %v", err)
	}
}

// Earlier repeat cycles are kept on the receipt so their reports resolve after the process is gone.
func TestAdoptKeepsEarlierRunsOnTheReceipt(t *testing.T) {
	var current atomic.Value
	current.Store("run-1")
	e := newLaunchEnv(t, "hold", func(c *LauncherConfig) {
		c.Discovery.Probe = func(_ context.Context, _ string) (Runtime, error) {
			return Runtime{ProtocolVersion: 1, RunID: current.Load().(string), Ready: true, InstanceID: InstanceIDFor(LaunchID("hist"))}, nil
		}
	})
	req := devRequest("hist")
	params, _, _ := e.resolve(req)
	if _, _, err := e.host.CreateReceipt(req.RequestID, params, e.identityOf(params), time.Now()); err != nil {
		t.Fatal(err)
	}
	launchID := LaunchID(req.RequestID)
	sleeper := exec.Command("sleep", "60")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() })
	marker, _ := runregistry.ProcessStart(sleeper.Process.Pid)
	rec := runregistry.Record{
		InstanceID: InstanceIDFor(launchID), LaunchID: launchID, PID: sleeper.Process.Pid, ProcessStart: marker,
		APIURL: "http://127.0.0.1:1", RunID: "run-1", Ready: true, StartedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := runregistry.Publish(e.regDir, rec); err != nil {
		t.Fatal(err)
	}
	if err := e.Recover(); err != nil {
		t.Fatal(err)
	}
	e.waitState(t, launchID, StateReady)
	current.Store("run-2")
	eventually(t, "the receipt to follow run-2", func() bool {
		r, _ := e.host.Receipt(launchID)
		return r.RunID == "run-2"
	})
	got, _ := e.host.Receipt(launchID)
	if !slices.Equal(got.PreviousRunIDs, []string{"run-1"}) {
		t.Errorf("previous run ids = %v, want [run-1]", got.PreviousRunIDs)
	}
}

// A launch whose process went through cycles the host never saw and then exited: the watcher saves what the
// record knew before it ends the launch.
func TestExitedLaunchKeepsTheRunsItsRecordKnew(t *testing.T) {
	e := newLaunchEnv(t, "hold", nil)
	req := devRequest("cycles")
	params, _, _ := e.resolve(req)
	if _, _, err := e.host.CreateReceipt(req.RequestID, params, e.identityOf(params), time.Now()); err != nil {
		t.Fatal(err)
	}
	launchID := LaunchID(req.RequestID)
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.host.UpdateReceipt(launchID, time.Now(), func(r *Receipt) {
		r.State, r.RunID, r.PID, r.ProcessStart, r.InstanceID = StateReady, "run-1", gone.Process.Pid, "old-marker", InstanceIDFor(launchID)
	}); err != nil {
		t.Fatal(err)
	}
	if err := runregistry.Publish(e.regDir, runregistry.Record{
		InstanceID: InstanceIDFor(launchID), LaunchID: launchID, PID: gone.Process.Pid, ProcessStart: "old-marker",
		RunID: "run-3", PreviousRunIDs: []string{"run-1", "run-2"}, RunsDir: "/work/.afm/runs", StartedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.Recover(); err != nil {
		t.Fatal(err)
	}
	got := e.waitState(t, launchID, StateExited)
	if got.RunID != "run-3" || !slices.Equal(got.PreviousRunIDs, []string{"run-1", "run-2"}) || got.RunsDir != "/work/.afm/runs" || !got.ProcessGone {
		t.Errorf("exited receipt = %+v", got)
	}
}

// A repeated request is answered from its receipt: what changed since it was accepted (its flow file, the project
// list, the host's permissions) is not checked again, while other parameters are still a conflict and a new
// request is still validated.
func TestReplayIsAnsweredFromTheReceiptAfterItsInputsChanged(t *testing.T) {
	e := newLaunchEnv(t, "hold", nil)
	explicit := StartRequest{RequestID: "flow-1", Project: "app", Flow: "dev.yaml", Mode: ModeCore, Task: "the task", MCPAllow: []string{"reply"}}
	omitted := StartRequest{RequestID: "flow-2", Project: "app", Flow: "dev.yaml", Mode: ModeCore, Worktree: "w2"}
	accepted := map[string]string{}
	for _, req := range []StartRequest{explicit, omitted} {
		r, err := e.Start(req)
		if err != nil {
			t.Fatalf("start %s: %v", req.RequestID, err)
		}
		accepted[req.RequestID] = r.LaunchID
	}

	if err := os.Remove(filepath.Join(e.project, ".afm", "flows", "dev.yaml")); err != nil {
		t.Fatal(err)
	}
	e.Launcher.cfg.Config.Projects = nil
	e.Launcher.cfg.Groups = nil
	for _, req := range []StartRequest{explicit, omitted} {
		if r, err := e.Start(req); err != nil || r.LaunchID != accepted[req.RequestID] {
			t.Errorf("replay of %s = %s, %v; want its launch %s", req.RequestID, r.LaunchID, err, accepted[req.RequestID])
		}
	}

	changedTask, changedGroups := explicit, omitted
	changedTask.Task = "another task"
	changedGroups.MCPAllow = []string{"stages"}
	for _, req := range []StartRequest{changedTask, changedGroups} {
		if _, err := e.Start(req); !errors.Is(err, ErrRequestConflict) {
			t.Errorf("%s with other parameters: %v, want a conflict", req.RequestID, err)
		}
	}
	fresh := explicit
	fresh.RequestID = "flow-3"
	if _, err := e.Start(fresh); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("a new request for a project that is gone: %v, want invalid", err)
	}
	if receipts, _ := e.host.Receipts(); len(receipts) != 2 {
		t.Errorf("%d launches, want only the 2 accepted", len(receipts))
	}
}
