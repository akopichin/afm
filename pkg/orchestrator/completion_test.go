package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
)

const testArtifactName = "output"
const testArtifactPath = "out.txt"
const testStageID = "s1"

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
		stage := flow.Stage{ID: testStageID}
		if err := checkCompletion(dir, ".", stage); err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})

	t.Run("done missing", func(t *testing.T) {
		dir := t.TempDir()
		stage := flow.Stage{ID: testStageID}
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
		stage := flow.Stage{ID: testStageID}
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
			ID: testStageID,
			Artifacts: []flow.Artifact{
				{Name: testArtifactName, Path: testArtifactPath, Description: "output file"},
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
		writeFile(t, filepath.Join(projectDir, testArtifactPath), []byte("data"))
		stage := flow.Stage{
			ID: testStageID,
			Artifacts: []flow.Artifact{
				{Name: testArtifactName, Path: testArtifactPath, Description: "output file"},
			},
		}
		if err := checkCompletion(stageDir, projectDir, stage); err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})

	t.Run("artifact with stage-relative path", func(t *testing.T) {
		runDir := t.TempDir()
		stageDir := filepath.Join(runDir, testStageID)
		if err := os.MkdirAll(stageDir, 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		writeFile(t, filepath.Join(stageDir, ".done"), []byte("done"))
		writeFile(t, filepath.Join(stageDir, "schema.sql"), []byte("CREATE TABLE"))
		stage := flow.Stage{
			ID: testStageID,
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
		{matchRateLimit + " exceeded", true},
		{matchTooManyRequests, true},
		{matchOverloaded, true},
		{matchAtCapacity, true},
		{"500 Internal Server Error", true},
		{matchInternalServerError, true},
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

func TestCheckCompletion_Verify(t *testing.T) {
	t.Run("verify passes", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, ".done"), []byte("done"))
		stage := flow.Stage{ID: testStageID, Verify: "true"}
		if err := checkCompletion(dir, t.TempDir(), stage); err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})

	t.Run("verify fails with output in reason", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, ".done"), []byte("done"))
		stage := flow.Stage{ID: testStageID, Verify: "echo '3 tests failed'; exit 1"}
		err := checkCompletion(dir, t.TempDir(), stage)
		if err == nil {
			t.Fatal("expected error for failing verify command")
		}
		if !isIncompleteWorkError(err) {
			t.Errorf("verify failure should be incomplete work (one retry), got %v", err)
		}
		if !strings.Contains(err.Error(), "3 tests failed") {
			t.Errorf("verify output should be in error, got %q", err.Error())
		}
	})

	t.Run("verify runs in project dir", func(t *testing.T) {
		dir := t.TempDir()
		projectDir := t.TempDir()
		writeFile(t, filepath.Join(dir, ".done"), []byte("done"))
		writeFile(t, filepath.Join(projectDir, "marker.txt"), []byte("x"))
		stage := flow.Stage{ID: testStageID, Verify: "test -f marker.txt"}
		if err := checkCompletion(dir, projectDir, stage); err != nil {
			t.Errorf("verify should run in project dir, got %v", err)
		}
	})

	t.Run("verify skipped when missing done", func(t *testing.T) {
		dir := t.TempDir()
		stage := flow.Stage{ID: testStageID, Verify: "true"}
		err := checkCompletion(dir, t.TempDir(), stage)
		if err == nil || !isIncompleteWorkError(err) {
			t.Errorf("missing .done should still be incomplete work, got %v", err)
		}
	})
}
