package stagefiles

import (
	"os"
	"path/filepath"
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
		if err := CheckPlanCompletion(dir); err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})

	t.Run("plan missing", func(t *testing.T) {
		dir := t.TempDir()
		if err := CheckPlanCompletion(dir); err == nil {
			t.Error("expected error for missing plan.md")
		}
	})

	t.Run("plan empty", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "plan.md"), []byte(""))
		if err := CheckPlanCompletion(dir); err == nil {
			t.Error("expected error for empty plan.md")
		}
	})
}

func TestCheckCompletion(t *testing.T) {
	t.Run("done exists no artifacts", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, ".done"), []byte("all done"))
		stage := flow.Stage{ID: testStageID}
		if err := CheckCompletion(dir, ".", stage); err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})

	t.Run("done missing", func(t *testing.T) {
		dir := t.TempDir()
		stage := flow.Stage{ID: testStageID}
		err := CheckCompletion(dir, ".", stage)
		if err == nil {
			t.Error("expected error for missing .done")
		}
		if !IsIncompleteWorkError(err) {
			t.Errorf("expected incomplete work error, got %v", err)
		}
	})

	t.Run("done empty", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, ".done"), []byte(""))
		stage := flow.Stage{ID: testStageID}
		err := CheckCompletion(dir, ".", stage)
		if err == nil {
			t.Error("expected error for empty .done")
		}
		if !IsIncompleteWorkError(err) {
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
		err := CheckCompletion(dir, t.TempDir(), stage)
		if err == nil {
			t.Error("expected error for missing artifact")
		}
		if IsIncompleteWorkError(err) {
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
		if err := CheckCompletion(stageDir, projectDir, stage); err != nil {
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
		if err := CheckCompletion(stageDir, ".", stage); err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})
}

// TestCheckCompletion_Verify — CheckCompletion теперь ЧИСТЫЙ file-probe
// (.done + артефакты) и не запускает verify вообще, даже если Verify задан.
// Раньше этот тест гонял shell-verify через CheckCompletion (V1); та
// exec/exit-code/вывод-в-ошибке семантика ПЕРЕЕХАЛА в движок
// (TestRunVerification_ShellRejectedCarriesLegacyTail/…, pkg/orchestrator/
// verify_test.go), а не удалена — см. отчёт задачи V4a. Здесь остаётся
// только проверка, что наличие Verify на стадии НИЧЕГО не меняет для
// file-probe: .done+артефакты решают всё, verify не запускается ни разу.
func TestCheckCompletion_Verify(t *testing.T) {
	t.Run("declared verify does not run from the probe", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, ".done"), []byte("done"))
		// Команда, которая упала бы, если бы CheckCompletion её выполнил —
		// сам факт nil-результата доказывает, что verify не выполняется здесь.
		stage := flow.Stage{ID: testStageID, Verify: flow.NewShellVerify("exit 1")}
		if err := CheckCompletion(dir, t.TempDir(), stage); err != nil {
			t.Errorf("probe must ignore Verify entirely, got %v", err)
		}
	})

	t.Run("missing done still incomplete regardless of verify", func(t *testing.T) {
		dir := t.TempDir()
		stage := flow.Stage{ID: testStageID, Verify: flow.NewShellVerify("true")}
		err := CheckCompletion(dir, t.TempDir(), stage)
		if err == nil || !IsIncompleteWorkError(err) {
			t.Errorf("missing .done should still be incomplete work, got %v", err)
		}
	})
}
