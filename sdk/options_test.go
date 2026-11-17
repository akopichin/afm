package afmsdk

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func startOptionsFake(t *testing.T, cfg fakeCfg, mode Mode, opts *StartOptions) *Run {
	t.Helper()
	c := useFakeAfm(t, cfg, mode)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	workDir := t.TempDir()
	var run *Run
	var err error
	if opts == nil {
		run, err = c.Start(ctx, "/flow.yaml", workDir)
	} else {
		run, err = c.StartWithOptions(ctx, "/flow.yaml", workDir, *opts)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = run.Shutdown(stopCtx)
	})
	return run
}

func TestStartWithOptions_TaskReachesAfmThroughStdin(t *testing.T) {
	run := startOptionsFake(t, fakeCfg{Record: true}, ModeCore, &StartOptions{Task: "do the thing\nsecond line"})

	raw, err := os.ReadFile(filepath.Join(run.Dir(), "fake-args.json"))
	if err != nil {
		t.Fatal(err)
	}
	var args []string
	if err := json.Unmarshal(raw, &args); err != nil {
		t.Fatal(err)
	}
	want := []string{"--task", "@-"}
	if !containsRun(args, want) {
		t.Errorf("argv = %v, want it to contain %v", args, want)
	}
	for _, a := range args {
		if a == "do the thing\nsecond line" {
			t.Errorf("task text leaked into argv: %v", args)
		}
	}
	task, err := os.ReadFile(filepath.Join(run.Dir(), "fake-task.txt"))
	if err != nil || string(task) != "do the thing\nsecond line" {
		t.Errorf("task read by afm = %q, %v", task, err)
	}
}

func TestStart_WithoutOptionsPassesNoTask(t *testing.T) {
	run := startOptionsFake(t, fakeCfg{Record: true}, ModeCore, nil)

	raw, err := os.ReadFile(filepath.Join(run.Dir(), "fake-args.json"))
	if err != nil {
		t.Fatal(err)
	}
	var args []string
	if err := json.Unmarshal(raw, &args); err != nil {
		t.Fatal(err)
	}
	for _, a := range args {
		if a == "--task" {
			t.Errorf("Start without options passed --task: %v", args)
		}
	}
	if _, err := os.Stat(filepath.Join(run.Dir(), "fake-task.txt")); !os.IsNotExist(err) {
		t.Errorf("a task was delivered without options (stat err = %v)", err)
	}
}

func TestRun_InstanceID(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	for _, mode := range []Mode{ModeCore, ModeDashboard} {
		t.Run(string(mode), func(t *testing.T) {
			run := startOptionsFake(t, fakeCfg{InstanceID: id}, mode, nil)
			if got := run.InstanceID(); got != id {
				t.Errorf("InstanceID = %q, want %q", got, id)
			}
			// A handle that attaches to the same process reads it from /api/runtime too.
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			conn, err := Connect(ctx, run.Port())
			if err != nil {
				t.Fatal(err)
			}
			if got := conn.InstanceID(); got != id {
				t.Errorf("connected InstanceID = %q, want %q", got, id)
			}
		})
	}
}

// An afm that predates the field simply has no instance id.
func TestRun_InstanceIDEmptyForOlderAfm(t *testing.T) {
	run := startOptionsFake(t, fakeCfg{}, ModeCore, nil)
	if got := run.InstanceID(); got != "" {
		t.Errorf("InstanceID = %q, want empty", got)
	}
}

func containsRun(args, seq []string) bool {
	for i := 0; i+len(seq) <= len(args); i++ {
		if reflect.DeepEqual(args[i:i+len(seq)], seq) {
			return true
		}
	}
	return false
}
