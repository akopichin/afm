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
