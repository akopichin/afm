package state_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/state"
)

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	s := state.NewRunState([]string{"backend", "frontend"})
	s.SetStageStatus("backend", state.StatusPlanning)

	if err := s.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := state.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Stages["backend"].Status != state.StatusPlanning {
		t.Errorf("status not persisted: got %v", loaded.Stages["backend"].Status)
	}
	if loaded.Stages["frontend"].Status != state.StatusPending {
		t.Errorf("other stage should be pending: got %v", loaded.Stages["frontend"].Status)
	}
}

func TestAtomicSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	s := state.NewRunState([]string{"a"})
	for i := 0; i < 10; i++ {
		if err := s.Save(path); err != nil {
			t.Fatalf("Save iteration %d: %v", i, err)
		}
	}
	if _, err := state.Load(path); err != nil {
		t.Fatalf("final Load: %v", err)
	}
}

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
