//go:build !windows

package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// agentRun is the outcome of one executor.run call made by runTurnScript.
type agentRun struct {
	err    error
	lines  []string
	stderr string
}

// runTurnScript runs body as the agent with prompt on stdin, handing each stdout line to onLine, and fails the
// test unless the call returns within limit.
func runTurnScript(t *testing.T, body, prompt string, onLine func(string), limit time.Duration) agentRun {
	t.Helper()
	script := filepath.Join(t.TempDir(), "agent.sh")
	if err := os.WriteFile(script, []byte("#!/bin/bash\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	e := New(Config{Command: script, ExtraArgs: []string{"-"}, IdleTimeout: time.Minute})
	var stderr bytes.Buffer
	var lines []string
	done := make(chan error, 1)
	go func() {
		done <- e.run(context.Background(), prompt, "test", &stderr, func(line string) {
			lines = append(lines, line)
			if onLine != nil {
				onLine(line)
			}
		})
	}()
	select {
	case err := <-done:
		return agentRun{err: err, lines: lines, stderr: stderr.String()}
	case <-time.After(limit):
		t.Fatalf("the agent turn did not finish within %s", limit)
		return agentRun{}
	}
}

// pidFrom reads the pid a script wrote to path and kills that process when the test ends, whatever happens.
func pidFrom(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	return pid
}

// requireGone fails the test unless pid is gone shortly after the turn returned; a killed orphan may take a
// moment to be reaped by init.
func requireGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !ProcessDead(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("descendant %d still runs after the turn returned", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A prompt larger than the pipe buffer, with a background child that holds stdin without reading it: the turn
// ends when the agent exits, and the child is killed with the group.
func TestRun_ChildHoldingStdinDoesNotBlockTheTurn(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	body := "sleep 30 <&0 >/dev/null 2>&1 &\necho $! > " + pidFile + "\necho done\n"
	run := runTurnScript(t, body, strings.Repeat("x", 4<<20), nil, 5*time.Second)
	if run.err != nil {
		t.Fatalf("run: %v", run.err)
	}
	requireGone(t, pidFrom(t, pidFile))
}

// An agent that exits with its final output still in the pipe while the output callback is slower than the
// grace period: nothing is lost, because a busy consumer is not a silent pipe.
func TestRun_SlowOutputCallbackKeepsBufferedFinalOutput(t *testing.T) {
	slow := func(line string) {
		if line == "first" {
			time.Sleep(pipeGracePeriod + 500*time.Millisecond)
		}
	}
	run := runTurnScript(t, "printf 'first\\nsecond\\nfinal\\n'\n", "", slow, 10*time.Second)
	if run.err != nil {
		t.Fatalf("run: %v", run.err)
	}
	if got := strings.Join(run.lines, ","); got != "first,second,final" {
		t.Fatalf("lines = %q, want all three", got)
	}
	if strings.Contains(run.stderr, "truncated") {
		t.Fatalf("stderr reports truncation: %q", run.stderr)
	}
}

// A descendant that closed both output pipes but keeps running is killed when the turn ends.
func TestRun_KillsDescendantThatClosedItsPipes(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	body := "sleep 30 </dev/null >/dev/null 2>&1 &\necho $! > " + pidFile + "\necho done\n"
	run := runTurnScript(t, body, "", nil, 5*time.Second)
	if run.err != nil {
		t.Fatalf("run: %v", run.err)
	}
	requireGone(t, pidFrom(t, pidFile))
}

// A descendant that left the agent's group (set -m puts the job in its own group) and holds stdout open: afm
// cannot kill it through the group, so it stops reading, keeps the output written before, and says the output
// was truncated. The turn does not hang.
func TestRun_DescendantOutsideTheGroupHoldingStdoutIsReportedAsTruncation(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	body := "set -m\nsleep 30 2>/dev/null &\necho $! > " + pidFile + "\necho final\n"
	run := runTurnScript(t, body, "", nil, 4*pipeGracePeriod)
	pidFrom(t, pidFile)
	if run.err != nil {
		t.Fatalf("run: %v", run.err)
	}
	if got := strings.Join(run.lines, ","); got != "final" {
		t.Fatalf("lines = %q, want the agent's own output", got)
	}
	if !strings.Contains(run.stderr, "agent output truncated") {
		t.Fatalf("stderr = %q, want a truncation warning", run.stderr)
	}
}

// Every group signal is sent while the group's anchor is still unreaped — so the group id cannot have been
// reused — and none after release. The agent is not the group id: reaping it frees nothing.
func TestProcessGroup_SignalsOnlyWhileTheAnchorIsUnreaped(t *testing.T) {
	g := newProcessGroup(anchorExecutable, io.Discard)
	if g.anchor == nil {
		t.Fatal("no anchor started; TestMain should make the test binary one")
	}
	var mu sync.Mutex
	var sent []int
	g.kill = func(pid int, sig syscall.Signal) error {
		mu.Lock()
		defer mu.Unlock()
		if g.anchor.ProcessState != nil {
			t.Errorf("signal %v to %d after the anchor was reaped", sig, pid)
		}
		sent = append(sent, pid)
		return syscall.Kill(pid, sig)
	}

	agent := exec.Command("/bin/sh", "-c", "sleep 30 >/dev/null 2>&1 & echo $!")
	g.join(agent)
	out, err := agent.Output() // starts and reaps the agent
	if err != nil {
		t.Fatal(err)
	}
	if agent.Process.Pid == g.pgid {
		t.Fatal("the agent leads the group; reaping it could free the group id")
	}
	member, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(member, syscall.SIGKILL) })

	g.signal(syscall.SIGKILL) // the agent is reaped, the group is still ours
	requireGone(t, member)

	g.release()
	g.signal(syscall.SIGKILL)
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 2 { // the explicit signal and release's own kill
		t.Fatalf("signals sent = %v, want 2", sent)
	}
	for _, pid := range sent {
		if pid != -g.pgid {
			t.Fatalf("signal sent to %d, want the group -%d", pid, g.pgid)
		}
	}
}

// exitedAgent is an agent started as run starts one, whose process has already exited and been reaped.
type exitedAgent struct {
	pipes  *agentPipes
	group  *processGroup
	stderr *bytes.Buffer
	lines  *atomic.Int64
}

// startExitedAgent runs body as the agent the way run does and returns once the agent process has exited; its
// descendants may still run and hold the output pipes. The group is released when the test ends.
func startExitedAgent(t *testing.T, body string) exitedAgent {
	t.Helper()
	cmd := exec.Command("/bin/bash", "-c", body)
	a := exitedAgent{stderr: &bytes.Buffer{}, lines: &atomic.Int64{}}
	var err error
	if a.pipes, err = attachAgentPipes(cmd, a.stderr); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.pipes.close)
	a.group = newProcessGroup(anchorExecutable, io.Discard)
	t.Cleanup(a.group.release)
	a.group.join(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	a.pipes.start("", a.stderr, func(string) bool {
		a.lines.Add(1)
		return true
	})
	if err := cmd.Wait(); err != nil {
		t.Fatalf("agent: %v", err)
	}
	return a
}

// writeForever is an agent body whose background child prints a line every 10 ms, never pausing long enough
// for the pipes to go silent, and writes its pid to pidFile.
func writeForever(pidFile string) string {
	return "(while :; do echo tick; sleep 0.01; done) &\necho $! > " + pidFile + "\necho final\n"
}

// A descendant that writes without pause after the agent exits does not keep the turn open: drain kills the
// group once drainLimit has passed, reads what is left and returns.
func TestDrain_DescendantWritingForeverIsKilledAtTheDrainLimit(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	a := startExitedAgent(t, writeForever(pidFile))
	a.pipes.drainLimit = 500 * time.Millisecond

	start := time.Now()
	err := a.pipes.drain(context.Background(), nil, a.group, a.stderr)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if elapsed < a.pipes.drainLimit {
		t.Fatalf("drain returned after %s, before the drain limit: the pipes were never silent", elapsed)
	}
	if limit := a.pipes.drainLimit + pipeFlushPeriod + time.Second; elapsed > limit {
		t.Fatalf("drain took %s, want at most %s", elapsed, limit)
	}
	requireGone(t, pidFrom(t, pidFile))
	if a.lines.Load() < 2 {
		t.Fatalf("read %d lines, want the agent's and its child's output", a.lines.Load())
	}
	if strings.Contains(a.stderr.String(), "truncated") {
		t.Fatalf("the writer was in the group, so nothing was cut off; stderr = %q", a.stderr.String())
	}
}

// Cancellation and the idle timeout stop a drain that a busy descendant keeps going: drain kills the group at
// once and reports why it stopped.
func TestDrain_CancellationAndIdleTimeoutStopTheDrain(t *testing.T) {
	tests := []struct {
		name    string
		stop    func(cancel context.CancelFunc, idle chan time.Time)
		wantErr error
	}{
		{"cancellation", func(cancel context.CancelFunc, _ chan time.Time) { cancel() }, context.Canceled},
		{"idle timeout", func(_ context.CancelFunc, idle chan time.Time) { idle <- time.Now() }, ErrIdleTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "child.pid")
			a := startExitedAgent(t, writeForever(pidFile))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			idle := make(chan time.Time, 1)
			time.AfterFunc(200*time.Millisecond, func() { tt.stop(cancel, idle) })

			start := time.Now()
			err := a.pipes.drain(ctx, idle, a.group, a.stderr)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("drain = %v, want %v", err, tt.wantErr)
			}
			if elapsed := time.Since(start); elapsed > pipeFlushPeriod+time.Second {
				t.Fatalf("drain took %s after being stopped at 200ms", elapsed)
			}
			requireGone(t, pidFrom(t, pidFile))
		})
	}
}

// When the anchor cannot start, the agent still runs: it leads its own group, which afm signals through the
// agent's pid, and the warning goes to the agent's stderr log.
func TestProcessGroup_WithoutAnchorTheAgentLeadsItsGroup(t *testing.T) {
	var log bytes.Buffer
	g := newProcessGroup(filepath.Join(t.TempDir(), "missing-afm"), &log)
	if g.anchor != nil {
		t.Fatal("an anchor started from a missing binary")
	}
	if !strings.Contains(log.String(), "process group anchor did not start") {
		t.Fatalf("stderr log = %q, want the fallback warning", log.String())
	}
	agent := exec.Command("/bin/sh", "-c", "sleep 30 >/dev/null 2>&1 & echo $!; sleep 30")
	g.join(agent)
	out, err := agent.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = agent.Wait() })
	var member int
	if _, err := fmt.Fscan(out, &member); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(member, syscall.SIGKILL) })
	if pgid, _ := syscall.Getpgid(agent.Process.Pid); pgid != agent.Process.Pid {
		t.Fatalf("agent pgid = %d, want its own pid %d", pgid, agent.Process.Pid)
	}

	g.release() // the agent is unreaped, so its group id is still ours
	requireGone(t, member)
}

// A full turn with no anchor binary available: the agent starts, its output arrives, and the descendant it
// leaves behind is killed with the turn.
func TestRun_AgentStartsWhenTheAnchorIsUnavailable(t *testing.T) {
	saved := anchorExecutable
	anchorExecutable = filepath.Join(t.TempDir(), "missing-afm")
	t.Cleanup(func() { anchorExecutable = saved })

	pidFile := filepath.Join(t.TempDir(), "child.pid")
	body := "sleep 30 </dev/null >/dev/null 2>&1 &\necho $! > " + pidFile + "\necho done\n"
	run := runTurnScript(t, body, "", nil, 5*time.Second)
	if run.err != nil {
		t.Fatalf("run: %v", run.err)
	}
	if got := strings.Join(run.lines, ","); got != "done" {
		t.Fatalf("lines = %q, want the agent's output", got)
	}
	requireGone(t, pidFrom(t, pidFile))
}
