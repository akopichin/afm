package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/docker"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/lifecyclehooks"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/state"
)

func TestExitOnFailResolver(t *testing.T) {
	for _, tc := range []struct {
		env     string
		want    bool
		wantErr bool
	}{
		{env: ""}, {env: " \t\n"},
		{env: "1", want: true}, {env: "true", want: true},
		{env: "yes", want: true}, {env: "on", want: true},
		{env: " TrUe\n", want: true}, {env: " YES ", want: true},
		{env: " ON ", want: true}, {env: " 1 ", want: true},
		{env: "0"}, {env: "false"}, {env: "no"}, {env: "off"},
		{env: " FaLsE\n"}, {env: " NO "}, {env: " OFF "},
		{env: "tru", wantErr: true}, {env: "2", wantErr: true},
		{env: "invalid-private-value", wantErr: true},
	} {
		t.Run(fmt.Sprintf("env=%q", tc.env), func(t *testing.T) {
			got, err := resolveExitOnFail(false, false, tc.env)
			if got != tc.want || (err != nil) != tc.wantErr {
				t.Fatalf("resolve = %v, %v; want %v, error=%v", got, err, tc.want, tc.wantErr)
			}
			if tc.wantErr && strings.Contains(err.Error(), "invalid-private-value") {
				t.Fatalf("invalid env value leaked: %v", err)
			}
			for _, flag := range []bool{false, true} {
				got, err := resolveExitOnFail(flag, true, tc.env)
				if got != flag || err != nil {
					t.Fatalf("explicit %v with env %q = %v, %v", flag, tc.env, got, err)
				}
			}
		})
	}
	for _, arg := range []string{"--exit-on-fail", "--exit-on-fail=true", "--exit-on-fail=false"} {
		t.Run(arg, func(t *testing.T) {
			cmd := newRunCmd()
			if err := cmd.ParseFlags([]string{arg}); err != nil {
				t.Fatal(err)
			}
			got, err := cmd.Flags().GetBool("exit-on-fail")
			if err != nil || got != (arg != "--exit-on-fail=false") || !cmd.Flags().Changed("exit-on-fail") {
				t.Fatalf("flag %s: value=%v err=%v changed=%v", arg, got, err, cmd.Flags().Changed("exit-on-fail"))
			}
		})
	}
}

func newExitOnFailFixture(t *testing.T) *cycleFixture {
	t.Helper()
	fx := newCycleFixture(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AFM_WORKTREES", "0")
	t.Setenv("AFM_USE_DOCKER", "0")
	t.Setenv("AFM_BROWSER", "0")
	t.Setenv("AFM_EXIT_ON_FAIL", "")
	return fx
}

func TestExitOnFailDefault(t *testing.T) {
	for _, tc := range []struct {
		name, env string
		args      []string
	}{
		{name: "default"},
		{name: "explicit false overrides env", env: "1", args: []string{"--exit-on-fail=false"}},
		{name: "explicit false overrides invalid env", env: "invalid", args: []string{"--exit-on-fail=false"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newExitOnFailFixture(t)
			t.Setenv("AFM_EXIT_ON_FAIL", tc.env)
			fx.write("flow.yaml", fx.flowYAML(flowSpec{script: "exit 7"}))
			stdout, stderr := captureOutput(t, func() {
				args := append([]string{"run", fx.flowPath, "--dir", fx.root, "--port=0"}, tc.args...)
				if code := runMain(args); code != 0 {
					t.Errorf("disabled policy exit code = %d, want legacy 0", code)
				}
			})
			dirs := fx.runDirs()
			if len(dirs) != 1 || fx.runState(dirs[0]).Stages["tick"].Status != state.StatusFailed {
				t.Fatalf("expected one failed run, got dirs %v", dirs)
			}
			if !strings.Contains(stdout, `flow "rp" completed`) || strings.Contains(stderr, "Usage:") {
				t.Fatalf("legacy output changed: stdout=%q stderr=%q", stdout, stderr)
			}
		})
	}
}

func TestExitOnFailExitError(t *testing.T) {
	for _, tc := range []struct {
		name, env string
		args      []string
	}{
		{name: "flag", env: "0", args: []string{"--exit-on-fail"}},
		{name: "environment", env: "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newExitOnFailFixture(t)
			t.Setenv("AFM_EXIT_ON_FAIL", tc.env)
			fx.write("flow.yaml", fx.flowYAML(flowSpec{script: "exit 7"}))
			stdout, stderr := captureOutput(t, func() {
				args := append([]string{"run", fx.flowPath, "--dir", fx.root, "--port=0"}, tc.args...)
				if code := runMain(args); code != 1 {
					t.Errorf("failed run exit code = %d, want 1", code)
				}
			})
			dirs := fx.runDirs()
			if len(dirs) != 1 {
				t.Fatalf("run dirs = %v, want one", dirs)
			}
			if strings.Count(stderr, "afm: stage") != 1 || !strings.Contains(stderr, "tick") || !strings.Contains(stderr, "exit status 7") || !strings.Contains(stderr, filepath.Join(fx.root, ".afm", "runs", dirs[0])) {
				t.Fatalf("missing single contextual failure: %q", stderr)
			}
			if strings.Contains(stdout, "completed") || strings.Contains(stdout+stderr, "Usage:") || strings.Contains(stderr, "Error:") {
				t.Fatalf("unexpected success/usage output: stdout=%q stderr=%q", stdout, stderr)
			}
			if rs := fx.runState(dirs[0]); rs.RunStatus != state.RunStatusFailed || rs.Stages["tick"].Status != state.StatusFailed {
				t.Fatalf("failed journal not preserved: %+v", rs)
			}
			wantEvents(t, fx.eventTypes(), "flow_started", "flow_failed")
		})
	}
}

func TestExitOnFailSignal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name string
		err  error
		code int
	}{
		{name: "SIGINT", err: context.Canceled, code: 130},
		{name: "primary failure wins", err: errors.Join(context.Canceled, &orchestrator.StageFailedError{StageID: "build", Reason: "failed", Seq: 8}), code: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got error
			stderr := captureStderr(t, func() { got = runExitError(ctx, tc.err, "/run-dir") })
			var exitErr *ExitError
			if !errors.As(got, &exitErr) || exitErr.Code != tc.code || !exitErr.Silent {
				t.Fatalf("runExitError = %v, want silent exit %d", got, tc.code)
			}
			if strings.Count(stderr, "\n") != 1 || (tc.code == 1 && (!strings.Contains(stderr, "/run-dir") || strings.Contains(stderr, "interrupted"))) {
				t.Fatalf("unexpected output: %q", stderr)
			}
		})
	}
}

func TestExitOnFailJoinedError(t *testing.T) {
	failed := &orchestrator.StageFailedError{StageID: "build", Reason: "exit status 7", Seq: 8}
	storageErr := fmt.Errorf("persist run completion: %w", &state.StorageError{Inner: errors.New("disk full")})
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "joined", err: errors.Join(failed, storageErr)},
		{name: "wrapped", err: fmt.Errorf("run: %w", errors.Join(storageErr, failed))},
		{name: "nested with cancellation", err: errors.Join(context.Canceled, errors.Join(fmt.Errorf("stage: %w", failed), storageErr))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			var got error
			stderr := captureStderr(t, func() { got = runExitError(ctx, tc.err, "/run-dir") })
			var exitErr *ExitError
			if !errors.As(got, &exitErr) || exitErr.Code != 1 || !exitErr.Silent {
				t.Fatalf("runExitError = %v, want silent exit 1", got)
			}
			want := "afm: stage build failed: exit status 7 (run dir: /run-dir)\n" +
				"afm: persist run completion: storage failure: disk full\n"
			if stderr != want {
				t.Fatalf("stderr = %q, want %q", stderr, want)
			}
		})
	}
}

func TestExitOnFailSingleLineReason(t *testing.T) {
	reason := "exit status 7\nforged log\r\t\x1b[31mred\x1b[0m\x00\x7f\u0085\u2028\u2029"
	failed := &orchestrator.StageFailedError{StageID: "build", Reason: reason, Seq: 8}
	var got error
	stderr := captureStderr(t, func() { got = runExitError(context.Background(), failed, "/run-dir") })
	var exitErr *ExitError
	if !errors.As(got, &exitErr) || exitErr.Code != 1 || !exitErr.Silent {
		t.Fatalf("runExitError = %v, want silent exit 1", got)
	}
	want := "afm: stage build failed: exit status 7forged log[31mred[0m (run dir: /run-dir)\n"
	if stderr != want {
		t.Fatalf("stderr = %q, want a single line without controls: %q", stderr, want)
	}
	if failed.Reason != reason {
		t.Fatalf("display formatting changed the recorded failure reason: %q", failed.Reason)
	}
}

func TestExitOnFailDashboardDrain(t *testing.T) {
	fx := newExitOnFailFixture(t)
	fx.write("flow.yaml", fx.flowYAML(flowSpec{script: "exit 7"}))
	drains := countDrains(t)
	port, err := pickFreeLoopbackPort()
	if err != nil {
		t.Fatal(err)
	}
	cfg := testCycleConfig()
	cfg.Server.Port = &port
	noBrowser := false
	cfg.Server.OpenBrowser = &noBrowser
	previousTerminal := controllingTerminal
	controllingTerminal = filepath.Join(fx.root, "no-terminal")
	t.Cleanup(func() { controllingTerminal = previousTerminal })
	f, err := flow.ParseFile(fx.flowPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), loopTestTimeout)
	defer cancel()
	loop := fx.loop(cfg)
	loop.exitOnFail = true
	var runErr error
	stdout, _ := captureOutput(t, func() { _, runErr = loop.run(ctx, f, combineRunHooks(cfg, f)) })
	var exitErr *ExitError
	if !errors.As(runErr, &exitErr) || exitErr.Code != 1 {
		t.Fatalf("dashboard run = %v, want exit 1", runErr)
	}
	if drains.Load() != 0 || strings.Contains(stdout, "dashboard: holding") {
		t.Fatalf("failure waited on dashboard: drains=%d stdout=%q", drains.Load(), stdout)
	}
}

func TestExitOnFailRepeat(t *testing.T) {
	t.Run("failed first cycle stops", func(t *testing.T) {
		fx := newExitOnFailFixture(t)
		fx.write("flow.yaml", fx.flowYAML(flowSpec{script: "exit 7", repeat: "{max_cycles: 3, keep_runs: 1}"}))
		stdout, stderr := captureOutput(t, func() {
			if code := runMain([]string{"run", fx.flowPath, "--dir", fx.root, "--port=0", "--exit-on-fail"}); code != 1 {
				t.Errorf("repeat exit = %d, want 1", code)
			}
		})
		if len(fx.runDirs()) != 1 || strings.Contains(stdout, "starting cycle 2") || strings.Contains(stdout, "removed old run") || strings.Contains(stderr, "repeat cleanup") {
			t.Fatalf("repeat continued/cleaned up: stdout=%q stderr=%q dirs=%v", stdout, stderr, fx.runDirs())
		}
		wantEvents(t, fx.eventTypes(), "flow_started", "flow_failed")
	})
	t.Run("invocation policy stays fixed and errors skip retention", func(t *testing.T) {
		fx := newExitOnFailFixture(t)
		fx.write("flow.yaml", fx.flowYAML(flowSpec{repeat: "{max_cycles: 3, keep_runs: 1}"}))
		f, err := flow.ParseFile(fx.flowPath)
		if err != nil {
			t.Fatal(err)
		}
		previousCycle := executeCycle
		t.Cleanup(func() { executeCycle = previousCycle })
		calls := 0
		failure := &ExitError{Code: 1, Silent: true}
		executeCycle = func(_ context.Context, _ *flow.Flow, _ config.Config, _ []lifecyclehooks.RegisteredHook, opts runOptions) (cycleResult, error) {
			calls++
			if !opts.exitOnFail {
				t.Error("policy lost between invocation and cycle")
			}
			if calls == 1 {
				t.Setenv("AFM_EXIT_ON_FAIL", "0")
				opts.afterRun()
				return cycleResult{RunID: "first", Completed: true}, nil
			}
			return cycleResult{RunID: "second"}, failure
		}
		loop := fx.loop(testCycleConfig())
		loop.exitOnFail = true
		var created []string
		var runErr error
		_, stderr := captureOutput(t, func() { created, runErr = loop.run(context.Background(), f, combineRunHooks(loop.cfg, f)) })
		if runErr != failure || calls != 2 || !slices.Equal(created, []string{"first", "second"}) || strings.Contains(stderr, "repeat cleanup") {
			t.Fatalf("failed repeat: err=%v calls=%d created=%v stderr=%q", runErr, calls, created, stderr)
		}
	})
}

func TestExitOnFailNonTerminalBoundaries(t *testing.T) {
	for _, hook := range []string{"", "hooks:\n  - id: observer\n    events: [flow_started]\n    command: exit 9\n"} {
		t.Run(fmt.Sprintf("observer_failure=%v", hook != ""), func(t *testing.T) {
			fx := newExitOnFailFixture(t)
			fx.write("flow.yaml", "name: success\n"+hook+"stages:\n  - id: tick\n    script: exit 0\n")
			captureOutput(t, func() {
				if code := runMain([]string{"run", fx.flowPath, "--dir", fx.root, "--port=0", "--exit-on-fail"}); code != 0 {
					t.Errorf("successful run exit = %d, want 0", code)
				}
			})
			dirs := fx.runDirs()
			if len(dirs) != 1 {
				t.Fatalf("successful run not durable: dirs=%v", dirs)
			}
			if rs := fx.runState(dirs[0]); rs.RunStatus != state.RunStatusFinished || !rs.AllDone() {
				t.Fatalf("successful run not durable: %+v", rs)
			}
		})
	}
}

func TestExitOnFailInvalidEnvPreflight(t *testing.T) {
	fx := newCycleFixture(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AFM_EXIT_ON_FAIL", "invalid-private-value")
	t.Setenv("AFM_WORKTREES", "1")
	t.Setenv("AFM_USE_DOCKER", "1")
	fx.write("flow.yaml", fx.flowYAML(flowSpec{}))
	called := false
	docker.SetExecFunc(func(string, []string, []string) error {
		called = true
		return nil
	})
	t.Cleanup(docker.ResetExecFunc)
	cmd := newRunCmd()
	cmd.SetArgs([]string{fx.flowPath})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "AFM_EXIT_ON_FAIL") {
		t.Fatalf("invalid env error = %v, want AFM_EXIT_ON_FAIL preflight error", err)
	}
	if strings.Contains(err.Error(), "invalid-private-value") {
		t.Fatalf("invalid env value leaked: %v", err)
	}
	if called {
		t.Fatal("invalid env reached Docker re-exec")
	}
	for _, name := range []string{".afm", ".worktrees"} {
		if _, err := os.Stat(filepath.Join(fx.root, name)); !os.IsNotExist(err) {
			t.Errorf("invalid env created %s: stat = %v", name, err)
		}
	}
}
