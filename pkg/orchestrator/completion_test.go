package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/flow"
)

// writeFile is a test helper that writes a file or fails the test.
func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestCheckPlanCompletion(t *testing.T) {
	t.Run("plan exists and not empty", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "plan.md"), []byte("# Plan\n- step 1"))
		if err := checkPlanCompletion(dir); err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})

	t.Run("plan missing", func(t *testing.T) {
		dir := t.TempDir()
		if err := checkPlanCompletion(dir); err == nil {
			t.Error("expected error for missing plan.md")
		}
	})

	t.Run("plan empty", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "plan.md"), []byte(""))
		if err := checkPlanCompletion(dir); err == nil {
			t.Error("expected error for empty plan.md")
		}
	})
}

func TestCheckCompletion(t *testing.T) {
	t.Run("done exists no artifacts", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, ".done"), []byte("all done"))
		stage := flow.Stage{ID: "s1"}
		if err := checkCompletion(dir, ".", stage); err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})

	t.Run("done missing", func(t *testing.T) {
		dir := t.TempDir()
		stage := flow.Stage{ID: "s1"}
		err := checkCompletion(dir, ".", stage)
		if err == nil {
			t.Error("expected error for missing .done")
		}
		if !isIncompleteWorkError(err) {
			t.Errorf("expected incomplete work error, got %v", err)
		}
	})

	t.Run("done empty", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, ".done"), []byte(""))
		stage := flow.Stage{ID: "s1"}
		err := checkCompletion(dir, ".", stage)
		if err == nil {
			t.Error("expected error for empty .done")
		}
		if !isIncompleteWorkError(err) {
			t.Errorf("expected incomplete work error, got %v", err)
		}
	})

	t.Run("done exists but artifact missing", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, ".done"), []byte("done"))
		stage := flow.Stage{
			ID: "s1",
			Artifacts: []flow.Artifact{
				{Name: "output", Path: "out.txt", Description: "output file"},
			},
		}
		err := checkCompletion(dir, t.TempDir(), stage)
		if err == nil {
			t.Error("expected error for missing artifact")
		}
		if isIncompleteWorkError(err) {
			t.Error("missing artifact should NOT be incomplete work (no retry)")
		}
	})

	t.Run("done exists and artifacts exist", func(t *testing.T) {
		projectDir := t.TempDir()
		stageDir := t.TempDir()
		writeFile(t, filepath.Join(stageDir, ".done"), []byte("done"))
		writeFile(t, filepath.Join(projectDir, "out.txt"), []byte("data"))
		stage := flow.Stage{
			ID: "s1",
			Artifacts: []flow.Artifact{
				{Name: "output", Path: "out.txt", Description: "output file"},
			},
		}
		if err := checkCompletion(stageDir, projectDir, stage); err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})

	t.Run("artifact with stage-relative path", func(t *testing.T) {
		runDir := t.TempDir()
		stageDir := filepath.Join(runDir, "s1")
		if err := os.MkdirAll(stageDir, 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		writeFile(t, filepath.Join(stageDir, ".done"), []byte("done"))
		writeFile(t, filepath.Join(stageDir, "schema.sql"), []byte("CREATE TABLE"))
		stage := flow.Stage{
			ID: "s1",
			Artifacts: []flow.Artifact{
				{Name: "db", Path: "./schema.sql", Description: "migration"},
			},
		}
		if err := checkCompletion(stageDir, ".", stage); err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})
}

func TestIsRetryableError(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"You've hit your limit", true},
		{"rate limit exceeded", true},
		{"too many requests", true},
		{"overloaded", true},
		{"capacity", true},
		{"500 Internal Server Error", true},
		{"internal server error", true},
		{"something went wrong", false},
		{"", false},
	}
	for _, c := range cases {
		var err error
		if c.msg != "" {
			err = fmt.Errorf("%s", c.msg)
		}
		if got := isRetryableError(err); got != c.want {
			t.Errorf("isRetryableError(%q) = %v, want %v", c.msg, got, c.want)
		}
	}

	if isRetryableError(nil) {
		t.Error("nil should not be retryable")
	}
}
