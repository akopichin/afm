package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	implexecutor "github.com/akopichin/afm-engine/testkit/executor"
	"github.com/akopichin/afm/contracts/state"
)

// afmExitTestChildEnv selects a fixture and runs check in a child test process, exiting with runMain's
// code.
const afmExitTestChildEnv = "AFM_EXIT_TEST_CHILD"

// afmRunTestChildArgsEnv carries a JSON array of runMain arguments: the child test process runs them and
// exits with their code, so a test can send it a real SIGINT and observe the exit code.
const afmRunTestChildArgsEnv = "AFM_RUN_TEST_CHILD_ARGS"

func TestMain(m *testing.M) {
	// Агенты запусков из тестов живут в группе-якоре; без него их потомки переживали бы тест.
	if implexecutor.ServeProcessGroupAnchor(os.Args) {
		return
	}
	if raw := os.Getenv(afmRunTestChildArgsEnv); raw != "" {
		var args []string
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(9) //nolint:revive // deliberate custom exit code, see comment above
		}
		_ = os.Unsetenv(afmRunTestChildArgsEnv) // the run's own children must not re-enter this branch
		os.Exit(runMain(args))                  //nolint:revive // deliberate custom exit code, see comment above
		return
	}
	if dir := os.Getenv(afmExitTestChildEnv); dir != "" {
		if err := os.Chdir(dir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(9) //nolint:revive // deliberate custom exit code, see comment above
		}
		os.Exit(runMain([]string{"check"})) //nolint:revive // deliberate custom exit code, see comment above
		return
	}
	// No explicit os.Exit(m.Run()) needed: the `go test` runner applies
	// m.Run()'s exit code automatically once TestMain returns (Go 1.15+).
	m.Run()
}

func TestExitError_ProcessExitCode(t *testing.T) {
	dir := t.TempDir()
	runDir := filepath.Join(dir, ".afm", "runs", "flow-20260101-120000")
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatal(err)
	}

	tr := state.Transition{
		Seq: 1, Time: time.Now(), StageID: cmdInit,
		From: state.StatusPending, To: state.StatusDone, Event: "test_setup",
	}
	data, err := json.Marshal(tr)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(runDir, "events.jsonl"), data, 0644); err != nil {
		t.Fatal(err)
	}
	// A complete (newline-terminated) line that isn't valid JSON: Load
	// returns ErrCorruptUsage for this, never a torn-tail false positive.
	if err := os.WriteFile(filepath.Join(runDir, "usage.jsonl"), []byte("not valid json\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// os.Args[0] is the path to this very test binary (compiled by `go test`), not user/network input — the
	// standard os/exec "helper process" re-exec pattern.
	cmd := exec.Command(os.Args[0]) //nolint:gosec // G702: os.Args[0] here is the test binary itself
	cmd.Env = append(os.Environ(), afmExitTestChildEnv+"="+dir)
	runErr := cmd.Run()

	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) {
		t.Fatalf("expected the subprocess to exit non-zero via *exec.ExitError, got err=%v", runErr)
	}
	if got := exitErr.ExitCode(); got != 3 {
		t.Fatalf("subprocess exit code = %d, want 3", got)
	}
}

func TestRunMain_ExitErrorSilentSuppressesPrinting(t *testing.T) {
	prev := rootDir
	t.Cleanup(func() { rootDir = prev })

	dir := t.TempDir()
	runDir := filepath.Join(dir, ".afm", "runs", "flow-20260101-120000")
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "usage.jsonl"), []byte("not valid json\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tr := state.Transition{
		Seq: 1, Time: time.Now(), StageID: cmdInit,
		From: state.StatusPending, To: state.StatusDone, Event: "test_setup",
	}
	data, err := json.Marshal(tr)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "events.jsonl"), append(data, '\n'), 0644); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if got := runMain([]string{"check", "--dir", dir}); got != 3 {
			t.Errorf("runMain([\"check\"]) with a corrupt ledger = %d, want 3", got)
		}
	})
	if got := out; got == "" {
		t.Error("expected check's own stdout table even on a corrupt ledger, got empty output")
	}
}

// TestRunExitError_InterruptIsNotAUsageError: Ctrl+C ends the run with a silent exit 130 (main prints no usage);
// any other run error stays an ordinary error.
func TestRunExitError_InterruptIsNotAUsageError(t *testing.T) {
	interrupted, cancel := context.WithCancel(context.Background())
	cancel()
	var exitErr *ExitError
	if err := runExitError(interrupted, context.Canceled, ""); !errors.As(err, &exitErr) || !exitErr.Silent || exitErr.Code != interruptedExitCode {
		t.Fatalf("interrupted run error = %v, want a silent ExitError with code %d", err, interruptedExitCode)
	}

	boom := errors.New("boom")
	if err := runExitError(context.Background(), boom, ""); !errors.Is(err, boom) || errors.As(err, &exitErr) {
		t.Fatalf("failed run error = %v, want the wrapped run error", err)
	}
}

// childOutput is the child's combined stdout and stderr, safe to read while the child still writes.
type childOutput struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (o *childOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.Write(p)
}

func (o *childOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.String()
}

// childShutdownGrace is how long cleanup waits for a SIGINTed child before killing its group.
const childShutdownGrace = 10 * time.Second

// afmChild is `afm <args>` running as a child of the test binary (see afmRunTestChildArgsEnv).
type afmChild struct {
	cmd  *exec.Cmd
	out  *childOutput
	done chan struct{}
	err  error // cmd.Wait's result, valid once done is closed
}

// startAfmChild starts runMain(args) in a child process whose environment holds only PATH, a throwaway HOME,
// the switches that keep the run local and LLM-free, and env (KEY=VALUE, later entries win). The child leads its
// own process group; a child still alive at cleanup gets SIGINT, then kill.
func startAfmChild(t *testing.T, dir string, env []string, args ...string) *afmChild {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	// os.Args[0] is the test binary itself, not user input — the standard os/exec "helper process" pattern.
	cmd := exec.Command(os.Args[0]) //nolint:gosec // G702: os.Args[0] here is the test binary itself
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = append([]string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"AFM_WORKTREES=0",
		"AFM_USE_DOCKER=0",
		"AFM_BROWSER=0",
		"AFM_SIDEAGENT=0",
		afmRunTestChildArgsEnv + "=" + string(raw),
	}, env...)
	c := &afmChild{cmd: cmd, out: &childOutput{}, done: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = c.out, c.out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		c.err = cmd.Wait()
		close(c.done)
	}()
	t.Cleanup(func() {
		select {
		case <-c.done:
			return
		default:
		}
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case <-c.done:
		case <-time.After(childShutdownGrace):
		}
		// The child is the group leader and is reaped only by the goroutine above, so its pid still names the
		// group until done closes; after that nothing is signalled.
		select {
		case <-c.done:
		default:
			c.kill(t)
		}
	})
	return c
}

// kill is the emergency stop of a child that ignored SIGINT. A dead afm reaps nothing, so the group of every
// agent it anchored would outlive it: the child is frozen first, its direct children's groups are killed
// while their ids still name them, and only then the child itself.
func (c *afmChild) kill(t *testing.T) {
	t.Helper()
	pid := c.cmd.Process.Pid
	// SIGSTOP on the group keeps the unreaped child's pid (and so its group id) from being reused.
	_ = syscall.Kill(-pid, syscall.SIGSTOP)
	killChildGroups(t, pid)
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	<-c.done
}

// killChildGroups SIGKILLs every process group that holds a direct child of parent, except parent's own
// group and the test process's.
func killChildGroups(t *testing.T, parent int) {
	t.Helper()
	out, err := exec.Command("ps", "-A", "-o", "pid=,ppid=,pgid=").Output()
	if err != nil {
		t.Errorf("list processes to clean up the groups of child %d: %v", parent, err)
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		ppid, _ := strconv.Atoi(fields[1])
		pgid, _ := strconv.Atoi(fields[2])
		if ppid == parent && pgid > 1 && pgid != parent && pgid != syscall.Getpgrp() {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
	}
}

// waitOutput blocks until the child has printed text.
func (c *afmChild) waitOutput(t *testing.T, text string) {
	t.Helper()
	waitFor(t, fmt.Sprintf("child output %q", text), func() bool {
		select {
		case <-c.done:
			t.Fatalf("child exited while waiting for %q: %v\n%s", text, c.err, c.out.String())
		default:
		}
		return strings.Contains(c.out.String(), text)
	})
}

// exitCode waits for the child to exit by itself and returns its code.
func (c *afmChild) exitCode(t *testing.T) int {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(loopTestTimeout):
		t.Fatalf("child did not exit; output:\n%s", c.out.String())
	}
	var exitErr *exec.ExitError
	switch {
	case c.err == nil:
		return 0
	case errors.As(c.err, &exitErr):
		return exitErr.ExitCode()
	default:
		t.Fatalf("child wait: %v", c.err)
		return -1
	}
}

// interrupt sends SIGINT and returns the child's exit code.
func (c *afmChild) interrupt(t *testing.T) int {
	t.Helper()
	if err := c.cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	return c.exitCode(t)
}

// A child that ignores SIGINT is killed together with the groups of its agents: afm gives every agent its own
// anchor group, so killing only afm's group would leave the agent running.
func TestAfmChildKill_StopsAnchoredAgent(t *testing.T) {
	fx := newCoreFixture(t)
	pidFile := filepath.Join(fx.root, "agent.pid")
	fx.write("flow.yaml", fx.flowYAML(flowSpec{script: "echo $$ > " + pidFile + "\nexec sleep 600"}))
	child := fx.startCore(t, nil)

	var agentPID int
	waitFor(t, "the agent's pid file", func() bool {
		raw, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		agentPID, err = strconv.Atoi(strings.TrimSpace(string(raw)))
		return err == nil
	})
	pgid, err := syscall.Getpgid(agentPID)
	if err != nil {
		t.Fatal(err)
	}
	// Safety net for a failing run: the agent must not outlive the test. A pid freed and reused by an unrelated
	// process belongs to another group, so the pair is checked before the signal.
	t.Cleanup(func() {
		if got, err := syscall.Getpgid(agentPID); err == nil && got == pgid {
			_ = syscall.Kill(agentPID, syscall.SIGKILL)
		}
	})
	if pgid == child.cmd.Process.Pid {
		t.Fatalf("agent %d shares afm's process group %d; the test would not cover anchored groups", agentPID, pgid)
	}

	// A stopped afm cannot handle SIGINT, like a hung one.
	if err := syscall.Kill(child.cmd.Process.Pid, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	child.kill(t)

	waitFor(t, "the agent to die", func() bool {
		gone, err := processGone(agentPID)
		if err != nil {
			t.Fatal(err)
		}
		return gone
	})
}

// A ps that cannot run says nothing about the process, so it must never read as the process being dead.
func TestAfmChildKill_ProcessGoneReportsPSFailure(t *testing.T) {
	if gone, err := processGone(os.Getpid()); err != nil || gone {
		t.Fatalf("processGone(own pid) = %v, %v; want false, nil", gone, err)
	}

	finished := exec.Command("true")
	if err := finished.Run(); err != nil {
		t.Fatal(err)
	}
	if gone, err := processGone(finished.Process.Pid); err != nil || !gone {
		t.Fatalf("processGone(reaped child) = %v, %v; want true, nil", gone, err)
	}

	t.Setenv("PATH", t.TempDir())
	if gone, err := processGone(os.Getpid()); err == nil || gone {
		t.Fatalf("processGone with no ps on PATH = %v, %v; want false and an error", gone, err)
	}
}

// processGone reports whether pid no longer runs: it is absent or an unreaped zombie. ps exits 1 without any
// output when no process matches; every other failure is returned, not read as a death.
func processGone(pid int) (bool, error) {
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && len(out) == 0 && len(exitErr.Stderr) == 0:
		return true, nil
	case err != nil:
		return false, fmt.Errorf("ps -p %d: %w", pid, err)
	default:
		return strings.HasPrefix(strings.TrimSpace(string(out)), "Z"), nil
	}
}
