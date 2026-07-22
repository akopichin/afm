package orchestrator

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestDialogPhases_BaseWithoutAutonomousFlag(t *testing.T) {
	dir := t.TempDir()
	got := dialogPhases(dir)
	want := []string{phasePlanning, phaseImplementation, phaseReview}
	if !slices.Equal(got, want) {
		t.Fatalf("dialogPhases (no flag) = %v, want %v", got, want)
	}
}

func TestDialogPhases_IncludesAutonomousWhenFlagPresent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "autonomous.flag"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	got := dialogPhases(dir)
	want := []string{phasePlanning, phaseImplementation, phaseReview, phaseAutonomous}
	if !slices.Equal(got, want) {
		t.Fatalf("dialogPhases (with flag) = %v, want %v", got, want)
	}
}

func TestClearInteractiveSessions_ClearsAutonomousArtifacts(t *testing.T) {
	dir := t.TempDir()
	// Стадия в автономном треке.
	if err := os.WriteFile(filepath.Join(dir, "autonomous.flag"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	// Артефакты autonomous-фазы: session по имени фазы, лог — autonomous.jsonl.
	autoSession := filepath.Join(dir, phaseAutonomous+".session.json") // autonomous_execution.session.json
	autoJSONL := filepath.Join(dir, "autonomous.jsonl")
	if err := os.WriteFile(autoSession, []byte(`{"session":"x"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(autoJSONL, []byte("line\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Плюс обычная planning-сессия, чтобы проверить базовый путь.
	planSession := filepath.Join(dir, phasePlanning+".session.json")
	if err := os.WriteFile(planSession, []byte(`{"session":"p"}`), 0644); err != nil {
		t.Fatal(err)
	}

	clearInteractiveSessions(dir)

	if _, err := os.Stat(autoSession); !os.IsNotExist(err) {
		t.Errorf("autonomous session должен быть удалён, err=%v", err)
	}
	if _, err := os.Stat(planSession); !os.IsNotExist(err) {
		t.Errorf("planning session должен быть удалён, err=%v", err)
	}
	if fi, err := os.Stat(autoJSONL); err != nil || fi.Size() != 0 {
		t.Errorf("autonomous.jsonl должен быть усечён до 0, size/err = %v/%v", func() int64 {
			if fi != nil {
				return fi.Size()
			}
			return -1
		}(), err)
	}
}
