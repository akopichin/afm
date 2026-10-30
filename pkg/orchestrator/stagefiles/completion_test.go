package stagefiles

import (
	"errors"
	"io/fs"
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

// asIncompleteWorkError fails the test if err is not a *IncompleteWorkError and
// otherwise returns it, to keep the assertions below to the point.
func asIncompleteWorkError(t *testing.T, err error) *IncompleteWorkError {
	t.Helper()
	var inc *IncompleteWorkError
	if !errors.As(err, &inc) {
		t.Fatalf("expected *IncompleteWorkError, got %v (%T)", err, err)
	}
	return inc
}

// TestCheckCompletion_MissingNamesPath гарантирует, что ошибка «нет .done» называет
// полный путь к файлу, а не только общую фразу — иначе причину приходится угадывать
// по содержимому stageDir руками.
func TestCheckCompletion_MissingNamesPath(t *testing.T) {
	dir := t.TempDir()
	donePath := filepath.Join(dir, ".done")
	stage := flow.Stage{ID: testStageID}

	inc := asIncompleteWorkError(t, CheckCompletion(dir, ".", stage))

	if !strings.Contains(inc.Error(), donePath) {
		t.Errorf("error %q does not mention path %q", inc.Error(), donePath)
	}
	if !strings.Contains(inc.Error(), "missing") {
		t.Errorf("error %q does not mention 'missing'", inc.Error())
	}
}

// TestCheckCompletion_EmptyNamesPath — то же самое, но для пустого (из пробелов)
// .done: текст должен отличать «пусто» от «нет файла».
func TestCheckCompletion_EmptyNamesPath(t *testing.T) {
	dir := t.TempDir()
	donePath := filepath.Join(dir, ".done")
	writeFile(t, donePath, []byte("   \n\t  "))
	stage := flow.Stage{ID: testStageID}

	inc := asIncompleteWorkError(t, CheckCompletion(dir, ".", stage))

	if !strings.Contains(inc.Error(), donePath) {
		t.Errorf("error %q does not mention path %q", inc.Error(), donePath)
	}
	if !strings.Contains(inc.Error(), "empty") {
		t.Errorf("error %q does not mention 'empty'", inc.Error())
	}
}

// TestCheckCompletion_UnreadableIsDistinct покрывает третий, отдельный от
// missing/empty случай: .done существует, но os.ReadFile не может его прочитать
// (здесь — потому что это каталог, портативный способ получить ошибку чтения без
// игр с правами доступа). Сообщение должно называть путь, отличаться от текста
// «missing», и сохранять причину, которую вернул os.ReadFile.
func TestCheckCompletion_UnreadableIsDistinct(t *testing.T) {
	dir := t.TempDir()
	donePath := filepath.Join(dir, ".done")
	if err := os.Mkdir(donePath, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", donePath, err)
	}
	stage := flow.Stage{ID: testStageID}

	inc := asIncompleteWorkError(t, CheckCompletion(dir, ".", stage))
	if !strings.Contains(inc.Error(), donePath) {
		t.Errorf("error %q does not mention path %q", inc.Error(), donePath)
	}

	missingInc := asIncompleteWorkError(t, CheckCompletion(dir+"-does-not-exist", ".", stage))
	if inc.Error() == missingInc.Error() {
		t.Errorf("unreadable .done message should differ from missing .done message, both are %q", inc.Error())
	}

	// Причина, которую вернул os.ReadFile на каталоге (обычно "is a directory"),
	// должна остаться видимой в тексте, а не потеряться при обёртывании.
	_, readErr := os.ReadFile(donePath)
	if readErr == nil {
		t.Fatalf("expected os.ReadFile(%s) to fail on a directory", donePath)
	}
	if !strings.Contains(inc.Error(), readErr.Error()) {
		t.Errorf("error %q does not preserve underlying reason %q", inc.Error(), readErr.Error())
	}
}

// TestCheckAutonomousCompletion_SameDiagnostics проверяет тот же трёхсторонний
// диагноз (missing/empty/unreadable), что и CheckCompletion, но для
// execution_summary.md — путь автономного трека не должен отставать от основного.
func TestCheckAutonomousCompletion_SameDiagnostics(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		dir := t.TempDir()
		summaryPath := filepath.Join(dir, "execution_summary.md")

		inc := asIncompleteWorkError(t, CheckAutonomousCompletion(dir))

		if !strings.Contains(inc.Error(), summaryPath) {
			t.Errorf("error %q does not mention path %q", inc.Error(), summaryPath)
		}
		if !strings.Contains(inc.Error(), "missing") {
			t.Errorf("error %q does not mention 'missing'", inc.Error())
		}
	})

	t.Run("empty", func(t *testing.T) {
		dir := t.TempDir()
		summaryPath := filepath.Join(dir, "execution_summary.md")
		writeFile(t, summaryPath, []byte("  \n  "))

		inc := asIncompleteWorkError(t, CheckAutonomousCompletion(dir))

		if !strings.Contains(inc.Error(), summaryPath) {
			t.Errorf("error %q does not mention path %q", inc.Error(), summaryPath)
		}
		if !strings.Contains(inc.Error(), "empty") {
			t.Errorf("error %q does not mention 'empty'", inc.Error())
		}
	})

	t.Run("unreadable is distinct", func(t *testing.T) {
		dir := t.TempDir()
		summaryPath := filepath.Join(dir, "execution_summary.md")
		if err := os.Mkdir(summaryPath, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", summaryPath, err)
		}

		inc := asIncompleteWorkError(t, CheckAutonomousCompletion(dir))
		if !strings.Contains(inc.Error(), summaryPath) {
			t.Errorf("error %q does not mention path %q", inc.Error(), summaryPath)
		}

		missingInc := asIncompleteWorkError(t, CheckAutonomousCompletion(dir+"-does-not-exist"))
		if inc.Error() == missingInc.Error() {
			t.Errorf("unreadable message should differ from missing message, both are %q", inc.Error())
		}

		_, readErr := os.ReadFile(summaryPath)
		if readErr == nil {
			t.Fatalf("expected os.ReadFile(%s) to fail on a directory", summaryPath)
		}
		if !strings.Contains(inc.Error(), readErr.Error()) {
			t.Errorf("error %q does not preserve underlying reason %q", inc.Error(), readErr.Error())
		}
	})
}

// TestCheckPlanCompletion_MissingNamesPath закрепляет, что уже корректно
// %w-оборачиваемая ошибка "missing plan.md" теперь ещё и называет полный путь.
func TestCheckPlanCompletion_MissingNamesPath(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.md")

	err := CheckPlanCompletion(dir)

	if err == nil {
		t.Fatal("expected error for missing plan.md")
	}
	if !strings.Contains(err.Error(), planPath) {
		t.Errorf("error %q does not mention path %q", err.Error(), planPath)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error %v does not wrap the underlying os.ErrNotExist", err)
	}
}

// Тот же типизированный диагноз у .done и execution_summary.md: исправимы только missing/empty, а текст
// ошибки .done остаётся прежним.
func TestMarkerFailure_TypesEveryMarkerProblem(t *testing.T) {
	dir := t.TempDir()
	stage := flow.Stage{ID: testStageID}
	cases := []struct {
		name  string
		setup func(path string)
		want  MarkerProblem
	}{
		{"missing", func(string) {}, MarkerMissing},
		{"empty", func(path string) { writeFile(t, path, []byte(" \n\t")) }, MarkerEmpty},
		{"directory", func(path string) {
			if err := os.Mkdir(path, 0755); err != nil {
				t.Fatal(err)
			}
		}, MarkerUnreadable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sub := filepath.Join(dir, tc.name)
			if err := os.Mkdir(sub, 0755); err != nil {
				t.Fatal(err)
			}
			summary := filepath.Join(sub, SummaryFile)
			tc.setup(summary)
			failure, ok := MarkerFailureOf(CheckAutonomousCompletion(sub))
			if !ok || failure.Problem != tc.want || failure.Path != summary {
				t.Fatalf("summary failure = %+v ok=%v, want %s at %s", failure, ok, tc.want, summary)
			}

			doneSub := filepath.Join(dir, tc.name+"-done")
			if err := os.Mkdir(doneSub, 0755); err != nil {
				t.Fatal(err)
			}
			done := filepath.Join(doneSub, ".done")
			tc.setup(done)
			failure, ok = MarkerFailureOf(CheckCompletion(doneSub, ".", stage))
			if !ok || failure.Problem != tc.want || failure.Path != done {
				t.Fatalf(".done failure = %+v ok=%v, want %s at %s", failure, ok, tc.want, done)
			}
		})
	}
}

// Не marker-проба — не marker-ошибка: недостающий declared artifact остаётся своим типом и не лечится как summary.
func TestMarkerFailureOf_IgnoresOtherErrors(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".done"), []byte("done"))
	stage := flow.Stage{ID: testStageID, Artifacts: []flow.Artifact{{Name: testArtifactName, Path: testArtifactPath}}}
	err := CheckCompletion(dir, dir, stage)
	if _, ok := MarkerFailureOf(err); ok {
		t.Fatalf("a missing declared artifact (%v) reads as a marker failure", err)
	}
	if _, ok := MarkerFailureOf(&IncompleteWorkError{Reason: "verify rejected"}); ok {
		t.Fatal("an incomplete-work error without a marker reads as a marker failure")
	}
	if _, ok := MarkerFailureOf(nil); ok {
		t.Fatal("nil reads as a marker failure")
	}
}
