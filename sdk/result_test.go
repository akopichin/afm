package afmsdk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"
)

func fastPolling(t *testing.T) {
	t.Helper()
	oldPoll, oldWindow := pollInterval, connectOutageWindow
	pollInterval, connectOutageWindow = 20*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() { pollInterval, connectOutageWindow = oldPoll, oldWindow })
}

func statusJSON(runID, state string, ended bool, stageStatus string) string {
	endedAt := ""
	if ended {
		endedAt = `,"ended_at":"2026-11-07T10:00:00Z"`
	}
	return fmt.Sprintf(`{"flow_name":"demo","run_id":%q,"started_at":"2026-11-07T09:00:00Z","run_status":%q%s,"stages":[{"id":"s","status":%q}]}`, runID, state, endedAt, stageStatus)
}

// scriptedRun serves the next /api/status body per request, repeating the last one; /api/runtime is absent (legacy afm).
func scriptedRun(t *testing.T, statusBodies []string) *Run {
	t.Helper()
	var st atomic.Int32
	next := func(counter *atomic.Int32, bodies []string) string {
		i := int(counter.Add(1)) - 1
		return bodies[min(i, len(bodies)-1)]
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/status":
			_, _ = fmt.Fprint(w, next(&st, statusBodies))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return &Run{baseURL: srv.URL, httpClient: srv.Client()}
}

func TestWaitResult_TerminalStates(t *testing.T) {
	fastPolling(t)
	for _, state := range []string{RunStateFinished, RunStateFailed, RunStateInterrupted} {
		run := scriptedRun(t, []string{statusJSON("r1", RunStateRunning, false, "running"), statusJSON("r1", state, true, "done")})
		got, err := run.WaitResult(context.Background())
		if err != nil || got.RunState != state || got.RunID != "r1" {
			t.Fatalf("%s: got %+v, %v", state, got, err)
		}
	}
}

func TestWaitResult_FailedStageIsNotTheResult(t *testing.T) {
	fastPolling(t)
	run := scriptedRun(t, []string{
		statusJSON("r1", RunStateRunning, false, "failed"),
		statusJSON("r1", RunStateRunning, false, "running"),
		statusJSON("r1", RunStateFinished, true, "done"),
	})
	got, err := run.WaitResult(context.Background())
	if err != nil || got.RunState != RunStateFinished {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestWaitResult_EndedWithoutTerminalStateKeepsWaiting(t *testing.T) {
	fastPolling(t)
	run := scriptedRun(t, []string{statusJSON("r1", RunStateRunning, true, "done")})
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := run.WaitResult(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
}

func TestWaitResult_RunChanged(t *testing.T) {
	fastPolling(t)
	run := scriptedRun(t, []string{statusJSON("a", RunStateRunning, false, "running"), statusJSON("b", RunStateFinished, true, "done")})
	_, err := run.WaitResult(context.Background())
	var changed *RunChangedError
	if !errors.As(err, &changed) || changed.Expected != "a" || changed.Current != "b" {
		t.Fatalf("err = %v, want RunChangedError a→b", err)
	}
}

func TestWaitResult_IgnoresStaleResultUntilReady(t *testing.T) {
	fastPolling(t)
	var runtimePolls, statusPolls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/runtime":
			ready := runtimePolls.Add(1) > 2
			_, _ = fmt.Fprintf(w, `{"protocol_version":1,"mode":"core","run_id":"new","ready":%t}`, ready)
		case "/api/status":
			// Until the new process is ready the journal still holds the previous process's final status.
			if runtimePolls.Load() <= 2 {
				_, _ = fmt.Fprint(w, statusJSON("old", RunStateFinished, true, "done"))
				return
			}
			if statusPolls.Add(1) == 1 {
				_, _ = fmt.Fprint(w, statusJSON("new", RunStateRunning, false, "running"))
				return
			}
			_, _ = fmt.Fprint(w, statusJSON("new", RunStateFinished, true, "done"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	run := &Run{baseURL: srv.URL, httpClient: srv.Client()}
	got, err := run.WaitResult(context.Background())
	if err != nil || got.RunID != "new" || got.RunState != RunStateFinished {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestWaitResult_EmptyRunIDIsIncompatible(t *testing.T) {
	fastPolling(t)
	run := scriptedRun(t, []string{statusJSON("", RunStateFinished, true, "done")})
	if _, err := run.WaitResult(context.Background()); !errors.Is(err, ErrIncompatibleRuntime) {
		t.Fatalf("err = %v, want ErrIncompatibleRuntime", err)
	}
}

func TestWaitResult_HTTPErrorIsNotRetried(t *testing.T) {
	fastPolling(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/status" {
			calls.Add(1)
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	run := &Run{baseURL: srv.URL, httpClient: srv.Client()}
	_, err := run.WaitResult(context.Background())
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != 500 || calls.Load() != 1 {
		t.Fatalf("err = %v after %d calls", err, calls.Load())
	}
}

func TestWaitResult_CancelDoesNotStopTheRun(t *testing.T) {
	fastPolling(t)
	run := startFake(t, fakeCfg{}, ModeCore)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := run.WaitResult(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if _, err := run.Status(context.Background()); err != nil {
		t.Fatalf("run no longer answers after WaitResult was cancelled: %v", err)
	}
}

func TestWaitResult_ProcessExitWithoutResult(t *testing.T) {
	fastPolling(t)
	for _, code := range []int{1, 130} {
		run := startFake(t, fakeCfg{ExitCode: code}, ModeCore)
		errCh := make(chan error, 1)
		go func() { _, err := run.WaitResult(context.Background()); errCh <- err }()
		time.Sleep(100 * time.Millisecond)
		_ = run.Shutdown(context.Background())
		err := <-errCh
		var exitErr *exec.ExitError
		if !errors.Is(err, ErrResultUnavailable) || !errors.As(err, &exitErr) || exitErr.ExitCode() != code {
			t.Fatalf("exit %d: err = %v", code, err)
		}
	}
}

func TestWaitResult_ConnectGivesUpAfterOutageWindow(t *testing.T) {
	fastPolling(t)
	srv := httptest.NewServer(http.NotFoundHandler())
	run := &Run{baseURL: srv.URL, httpClient: srv.Client()}
	srv.Close()
	if _, err := run.WaitResult(context.Background()); !errors.Is(err, ErrResultUnavailable) {
		t.Fatalf("err = %v, want ErrResultUnavailable", err)
	}
}

func TestWaitResult_AttachedProcessDied(t *testing.T) {
	fastPolling(t)
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.NotFoundHandler())
	run := &Run{baseURL: srv.URL, httpClient: srv.Client(), pid: cmd.Process.Pid}
	srv.Close()
	if _, err := run.WaitResult(context.Background()); !errors.Is(err, ErrResultUnavailable) || !errors.Is(err, ErrProcessDead) {
		t.Fatalf("err = %v", err)
	}
}

// stepServer answers each /api/status request with the next step; the last step repeats.
func stepServer(t *testing.T, steps ...func(w http.ResponseWriter)) *Run {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/status" {
			http.NotFound(w, r)
			return
		}
		i := int(calls.Add(1)) - 1
		steps[min(i, len(steps)-1)](w)
	}))
	t.Cleanup(srv.Close)
	return &Run{baseURL: srv.URL, httpClient: srv.Client()}
}

func serveStatus(body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) { _, _ = fmt.Fprint(w, body) }
}

// dropMidBody promises more bytes than it sends and closes the connection.
func dropMidBody(w http.ResponseWriter) {
	conn, buf, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return
	}
	_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 500\r\n\r\n{\"flow")
	_ = buf.Flush()
	_ = conn.Close()
}

func TestWaitResult_TruncatedBodyIsRetried(t *testing.T) {
	fastPolling(t)
	run := stepServer(t,
		serveStatus(statusJSON("a", RunStateRunning, false, "running")),
		dropMidBody,
		serveStatus(statusJSON("a", RunStateFinished, true, "done")),
	)
	got, err := run.WaitResult(context.Background())
	if err != nil || got.RunState != RunStateFinished {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestWaitResult_RunChangedAfterConnectionDrop(t *testing.T) {
	fastPolling(t)
	run := stepServer(t,
		serveStatus(statusJSON("a", RunStateRunning, false, "running")),
		dropMidBody, // an error the client sees, unlike a dropped keep-alive request the transport silently repeats
		serveStatus(statusJSON("b", RunStateFinished, true, "done")),
	)
	_, err := run.WaitResult(context.Background())
	var changed *RunChangedError
	if !errors.As(err, &changed) || changed.Expected != "a" || changed.Current != "b" {
		t.Fatalf("err = %v, want RunChangedError a→b", err)
	}
}

func TestIsNetworkError(t *testing.T) {
	if isNetworkError(&HTTPError{StatusCode: 500}) || isNetworkError(ErrResponseTooLarge) {
		t.Error("a reachable API that answered badly must not count as a network error")
	}
	if !isNetworkError(fmt.Errorf("read: %w", io.ErrUnexpectedEOF)) || !isNetworkError(&net.OpError{Op: "read"}) {
		t.Error("a broken connection must count as a network error")
	}
}
