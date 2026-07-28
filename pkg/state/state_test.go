package state_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/state"
)

func TestAllDone(t *testing.T) {
	s := state.NewRunState([]string{"a", "b"})
	if s.AllDone() {
		t.Error("should not be done initially")
	}
	s.SetStageStatus("a", state.StatusDone)
	s.SetStageStatus("b", state.StatusDone)
	if !s.AllDone() {
		t.Error("should be done when all stages done")
	}
}

func TestSaveFeedback(t *testing.T) {
	dir := t.TempDir()
	stageDir := filepath.Join(dir, "s1")
	os.MkdirAll(stageDir, 0755)

	err := state.SaveFeedback(stageDir, "Добавь Redis")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(stageDir, "feedback.md"))
	if !strings.Contains(string(data), "Добавь Redis") {
		t.Errorf("feedback not saved: %q", string(data))
	}

	// Second feedback — appended
	err = state.SaveFeedback(stageDir, "Ещё TTL")
	if err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(stageDir, "feedback.md"))
	content := string(data)
	if !strings.Contains(content, "Добавь Redis") || !strings.Contains(content, "Ещё TTL") {
		t.Errorf("second feedback not appended: %q", content)
	}
	if !strings.Contains(content, "revision 2") {
		t.Errorf("missing revision separator: %q", content)
	}
}

func TestAwaitingUserInputStatus(t *testing.T) {
	s := state.NewRunState([]string{"a"})
	s.SetStageStatus("a", state.StatusAwaitingUserInput)
	if s.Stages["a"].Status != state.StatusAwaitingUserInput {
		t.Errorf("expected awaiting_user_input, got %q", s.Stages["a"].Status)
	}
	if s.AllDone() {
		t.Error("awaiting_user_input must not count as done")
	}
}

func TestFindLatestRunDir(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "runs")

	// run-старый
	old := filepath.Join(base, "myflow-20260101-100000")
	os.MkdirAll(old, 0755)

	// run-новый (алфавитно позже)
	newer := filepath.Join(base, "myflow-20260101-120000")
	os.MkdirAll(newer, 0755)

	got, err := state.FindLatestRunDir(base, "myflow")
	if err != nil {
		t.Fatal(err)
	}
	if got != newer {
		t.Errorf("got %q, want %q", got, newer)
	}
}

func TestFindLatestRunDir_NotFound(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "runs")
	os.MkdirAll(base, 0755)

	_, err := state.FindLatestRunDir(base, "noflow")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestVersionPlan(t *testing.T) {
	dir := t.TempDir()
	stageDir := filepath.Join(dir, "s1")
	os.MkdirAll(stageDir, 0755)
	planFile := filepath.Join(stageDir, "plan.md")
	if err := os.WriteFile(planFile, []byte("# Plan v1"), 0644); err != nil {
		t.Fatal(err)
	}

	n, err := state.VersionPlan(stageDir)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("version: got %d, want 1", n)
	}

	// plan.md renamed to plan.v1.md
	if _, err := os.Stat(filepath.Join(stageDir, "plan.v1.md")); err != nil {
		t.Error("plan.v1.md should exist")
	}
	// plan.md no longer exists
	if _, err := os.Stat(planFile); !os.IsNotExist(err) {
		t.Error("plan.md should be removed after versioning")
	}
}

func TestLatestPlanVersion(t *testing.T) {
	t.Run("empty directory", func(t *testing.T) {
		dir := t.TempDir()
		v, content, err := state.LatestPlanVersion(dir)
		if err != nil {
			t.Fatal(err)
		}
		if v != 0 || content != "" {
			t.Errorf("got (%d, %q), want (0, \"\")", v, content)
		}
	})

	t.Run("single version", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "plan.v1.md"), []byte("v1 content"), 0644); err != nil {
			t.Fatal(err)
		}
		v, content, err := state.LatestPlanVersion(dir)
		if err != nil {
			t.Fatal(err)
		}
		if v != 1 || content != "v1 content" {
			t.Errorf("got (%d, %q), want (1, \"v1 content\")", v, content)
		}
	})

	t.Run("multiple versions with a gap picks the max", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "plan.v1.md"), []byte("v1 content"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "plan.v3.md"), []byte("v3 content"), 0644); err != nil {
			t.Fatal(err)
		}
		v, content, err := state.LatestPlanVersion(dir)
		if err != nil {
			t.Fatal(err)
		}
		if v != 3 || content != "v3 content" {
			t.Errorf("got (%d, %q), want (3, \"v3 content\")", v, content)
		}
	})

	t.Run("garbage names are ignored", func(t *testing.T) {
		dir := t.TempDir()
		for _, name := range []string{"plan.vX.md", "plan.v1.txt", "plan.md", "plan.v.md"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("should not count"), 0644); err != nil {
				t.Fatal(err)
			}
		}
		v, content, err := state.LatestPlanVersion(dir)
		if err != nil {
			t.Fatal(err)
		}
		if v != 0 || content != "" {
			t.Errorf("got (%d, %q), want (0, \"\") — garbage names must not be counted as versions", v, content)
		}
	})
}
