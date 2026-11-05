package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/clock"
)

const (
	sinkTerminalErrorLine = `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"upstream failed"}`
	sinkSuccessLine       = `{"type":"result","subtype":"success","is_error":false,"result":"done"}`
	sinkAnswer            = `{"schema_version":1,"kind":"result","verdict":"pass","summary":"ok"}`
)

func sinkFinalLine() string {
	return `{"type":"afm_final","version":1,"text":` + strconv.Quote(sinkAnswer) + `}`
}

// shortFinishWait keeps the end-of-call wait short for a test whose sink never returns.
func shortFinishWait(t *testing.T) {
	t.Helper()
	old := activityFinishWait
	activityFinishWait = 200 * time.Millisecond
	t.Cleanup(func() { activityFinishWait = old })
}

// sinkOutcome is what a caller of RunHelperAgent can tell apart about one call.
type sinkOutcome struct {
	ok      bool
	errType string
	raw     string
}

func (o sinkOutcome) String() string {
	return fmt.Sprintf("ok=%v err=%s raw=%q", o.ok, o.errType, o.raw)
}

// runGatedHelper runs a helper that prints first, waits for a gate file, then prints second and exits 0. With
// blockOn set, the activity sink blocks forever on the first event of that kind and the gate opens only after the
// sink is stuck; without it the call has no sink at all, which is the reference outcome.
func runGatedHelper(t *testing.T, first, second []string, blockOn ActivityKind) (sinkOutcome, time.Duration) {
	t.Helper()
	dir := t.TempDir()
	gate := filepath.Join(dir, "go")
	script := "cat <<'EOF'\n" + strings.Join(first, "\n") + "\nEOF\n" +
		"until [ -e " + gate + " ]; do sleep 0.02; done\n" +
		"cat <<'EOF'\n" + strings.Join(second, "\n") + "\nEOF\n"
	cfg := Config{Command: "bash", ExtraArgs: []string{"-c", script}, IdleTimeout: time.Hour}
	stuck := make(chan struct{}, 1)
	if blockOn != "" {
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })
		cfg.OnActivity = func(ev ActivityEvent) {
			if ev.Kind != blockOn {
				return
			}
			select {
			case stuck <- struct{}{}:
			default:
			}
			<-release
		}
	}
	start := time.Now()
	done := make(chan sinkOutcome, 1)
	go func() {
		out, _ := New(cfg).RunHelperAgent(context.Background(), "t1", "reviewer", "p", filepath.Join(dir, "h.log"), filepath.Join(dir, "r.json"))
		done <- sinkOutcome{ok: out.ProcessOK, errType: fmt.Sprintf("%T", out.ProcessErr), raw: out.RawText}
	}()
	if blockOn != "" {
		select {
		case <-stuck:
		case <-time.After(10 * time.Second):
			t.Fatal("the sink never saw the event it blocks on")
		}
	}
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case o := <-done:
		return o, time.Since(start)
	case <-time.After(30 * time.Second):
		t.Fatal("RunHelperAgent did not return")
		return sinkOutcome{}, 0
	}
}

// A sink that never returns must not change what the caller learns about the call: the semantic lines that follow
// the stuck event are still read.
func TestActivity_BlockedSinkDoesNotChangeTheOutcome(t *testing.T) {
	shortFinishWait(t)
	tests := []struct {
		name          string
		first, second []string
		blockOn       ActivityKind
	}{
		{"terminal error after a blocked tool start", []string{announceLine, toolStartLine}, []string{sinkTerminalErrorLine}, KindToolStarted},
		{"terminal error after a blocked message", []string{announceLine, `{"type":"assistant","message":{"content":[{"type":"text","text":"working"}]}}`}, []string{sinkTerminalErrorLine}, KindMessage},
		{"success after a blocked tool start", []string{announceLine, toolStartLine}, []string{sinkFinalLine(), sinkSuccessLine}, KindToolStarted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want, _ := runGatedHelper(t, tt.first, tt.second, "")
			got, elapsed := runGatedHelper(t, tt.first, tt.second, tt.blockOn)
			if got != want {
				t.Errorf("outcome with a blocked sink = %v, want the no-sink outcome %v", got, want)
			}
			if elapsed > 5*time.Second {
				t.Errorf("call took %v with a blocked sink, want under 5s", elapsed)
			}
		})
	}
}

// The reviewer's case: the sink blocks on the snapshot flushed when the terminal line arrives.
func TestActivity_BlockedFlushKeepsTheTerminalError(t *testing.T) {
	shortFinishWait(t)
	dir := t.TempDir()
	gate := filepath.Join(dir, "go")
	fc := clock.NewFake(time.Unix(1000, 0))
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	ready := make(chan struct{}, 1)
	blocked := make(chan struct{}, 1)
	sink := func(ev ActivityEvent) {
		if ev.Kind == KindToolStarted {
			ready <- struct{}{}
		}
		if ev.Kind == KindMessage && ev.Detail == "one two" {
			select {
			case blocked <- struct{}{}:
			default:
			}
			<-release
		}
	}
	first := strings.Join([]string{announceLine, partialLine("one"), partialLine("one two"), sinkFinalLine(), toolStartLine}, "\n")
	script := "cat <<'EOF'\n" + first + "\nEOF\nuntil [ -e " + gate + " ]; do sleep 0.02; done\ncat <<'EOF'\n" + sinkTerminalErrorLine + "\nEOF\n"
	ex := New(Config{Command: "bash", ExtraArgs: []string{"-c", script}, IdleTimeout: time.Hour, Clock: fc, OnActivity: sink})
	done := make(chan HelperOutcome, 1)
	go func() {
		out, _ := ex.RunHelperAgent(context.Background(), "t1", "reviewer", "p", filepath.Join(dir, "h.log"), filepath.Join(dir, "r.json"))
		done <- out
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("initial batch not processed")
	}
	fc.Advance(time.Second)
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("the pending snapshot was never delivered")
	}
	select {
	case out := <-done:
		var reported *CLIReportedError
		if out.ProcessOK || !errors.As(out.ProcessErr, &reported) {
			t.Errorf("outcome = %+v, want the CLI's terminal error", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunHelperAgent did not return")
	}
}

// A helper whose stdout was not read to its end must not be reported as a success; other modes keep the old
// behavior of treating a truncated read as the end of the output.
func TestRunJob_StuckOutputConsumerFailsOnlyAHelper(t *testing.T) {
	old := pipeDrainLimit
	pipeDrainLimit = 200 * time.Millisecond
	t.Cleanup(func() { pipeDrainLimit = old })

	tests := []struct {
		name    string
		mode    RunMode
		wantErr bool
	}{
		{"helper", ModeHelper, true},
		{"author", ModeAuthor, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unblock := make(chan struct{})
			t.Cleanup(func() { close(unblock) })
			ex := New(Config{Command: "bash", ExtraArgs: []string{"-c", "echo first"}, IdleTimeout: time.Hour, runMode: tt.mode})
			err := ex.runJob(context.Background(), nil, "p", "phase", io.Discard, func(string) { <-unblock })
			if got := errors.Is(err, errAbandoned); got != tt.wantErr {
				t.Errorf("runJob err = %v, want errAbandoned=%v", err, tt.wantErr)
			}
		})
	}
}

// The helper's OnAction callback runs on the stdout reader too: when it is stuck, the call fails instead of
// reporting the unread rest of the output as a success.
func TestRunHelperAgent_StuckActionCallbackIsAProcessError(t *testing.T) {
	old := pipeDrainLimit
	pipeDrainLimit = 200 * time.Millisecond
	t.Cleanup(func() { pipeDrainLimit = old })

	unblock := make(chan struct{})
	t.Cleanup(func() { close(unblock) })
	dir := t.TempDir()
	script := "cat <<'EOF'\n" + toolStartLine + "\nEOF\n"
	ex := New(Config{Command: "bash", ExtraArgs: []string{"-c", script}, IdleTimeout: time.Hour, OnAction: func(string, string) { <-unblock }})
	out, _ := ex.RunHelperAgent(context.Background(), "t1", "reviewer", "p", filepath.Join(dir, "h.log"), filepath.Join(dir, "r.json"))
	if out.ProcessOK || !errors.Is(out.ProcessErr, errAbandoned) {
		t.Errorf("outcome = %+v, want ProcessErr errAbandoned", out)
	}
}

// deliveryWorkers counts the activity delivery goroutines that are alive.
func deliveryWorkers() int {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	return strings.Count(string(buf), "(*activityNormalizer).deliver")
}

// TestActivity_NoDeliveryWorkerLeftWhenTheProcessNeverStarts launches helpers that cannot start; none may leave
// a delivery goroutine behind or report an invocation for a process that never ran.
func TestActivity_NoDeliveryWorkerLeftWhenTheProcessNeverStarts(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	cases := []struct {
		name string
		cfg  Config
		ctx  context.Context
	}{
		{"missing executable", Config{Command: "/nonexistent/afm-no-such-cli", IdleTimeout: time.Hour}, context.Background()},
		{"cancelled context", Config{Command: "bash", ExtraArgs: []string{"-c", "true"}, IdleTimeout: time.Hour}, cancelled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := deliveryWorkers()
			var events atomic.Int32
			tc.cfg.OnActivity = func(ActivityEvent) { events.Add(1) }
			for range 5 {
				dir := t.TempDir()
				_, _ = New(tc.cfg).RunHelperAgent(tc.ctx, "t1", "reviewer", "p", filepath.Join(dir, "h.log"), filepath.Join(dir, "r.json"))
			}
			deadline := time.Now().Add(5 * time.Second)
			for deliveryWorkers() > before && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
			}
			if got := deliveryWorkers(); got > before {
				t.Fatalf("delivery workers: %d before, %d after 5 launches that never started", before, got)
			}
			if n := events.Load(); n != 0 {
				t.Fatalf("%d activity events for calls whose process never started", n)
			}
		})
	}
}
