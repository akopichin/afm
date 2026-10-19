//go:build !windows

package executor

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
	for !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
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
	g, err := newProcessGroup()
	if err != nil {
		t.Fatal(err)
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
