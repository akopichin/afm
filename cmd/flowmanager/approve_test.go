package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/state"
)

// TestFindLatestRunDirCorrectFlow воспроизводит баг: findLatestRunDir
// должна возвращать run dir для нужного флоу, а не просто последний по алфавиту.
//
// Сценарий: flow-b запущен позже (newer dir), flow-a запущен раньше (older dir).
// approve/revise вызываются для stage из flow-a.
// Старый код возвращал state.json flow-b → stage не найден → ошибка "not found".
// Новый код ищет по stageID и возвращает правильный run dir.
func TestFindLatestRunDirCorrectFlow(t *testing.T) {
	chdirTemp(t)

	// flow-a — старее, содержит "a-stage"
	makeRunState(t, "flow-a-20260101-100000", "a-stage", state.StatusAwaitingApproval)

	// flow-b — новее (позже по алфавиту/времени), содержит "b-stage"
	makeRunState(t, "flow-b-20260101-120000", "b-stage", state.StatusAwaitingApproval)

	dir, _, err := findLatestRunDir("a-stage")
	if err != nil {
		t.Fatalf("findLatestRunDir: %v", err)
	}
	if !strings.Contains(dir, "flow-a-20260101-100000") {
		t.Errorf("должен вернуть run dir flow-a, получили: %s", dir)
	}
}

// TestFindLatestRunDirNewestWins проверяет что при одном и том же stageID
// возвращается последний (самый новый) run.
func TestFindLatestRunDirNewestWins(t *testing.T) {
	chdirTemp(t)

	// Два рана одного флоу с одинаковым stage, второй новее
	makeRunState(t, "flow-20260101-100000", "init", state.StatusDone)
	makeRunState(t, "flow-20260101-120000", "init", state.StatusAwaitingApproval)

	dir, _, err := findLatestRunDir("init")
	if err != nil {
		t.Fatalf("findLatestRunDir: %v", err)
	}
	if !strings.Contains(dir, "flow-20260101-120000") {
		t.Errorf("должен вернуть новейший run, получили: %s", dir)
	}
}

// TestFindLatestRunDirNotFound — stage нигде не существует.
func TestFindLatestRunDirNotFound(t *testing.T) {
	chdirTemp(t)

	makeRunState(t, "flow-20260101-120000", "other-stage", state.StatusAwaitingApproval)

	_, _, err := findLatestRunDir("nonexistent-stage")
	if err == nil {
		t.Fatal("ожидалась ошибка, но её нет")
	}
}

// TestApproveWrongFlowBug — интеграционный тест: approve для stage одного флоу
// не должен промахиваться по state.json другого флоу, который новее.
func TestApproveWrongFlowBug(t *testing.T) {
	chdirTemp(t)

	// flow-b запущен позже
	makeRunState(t, "flow-b-20260101-120000", "b-stage", state.StatusAwaitingApproval)

	// flow-a запущен раньше, содержит "a-stage" в awaiting_approval
	makeRunState(t, "flow-a-20260101-100000", "a-stage", state.StatusAwaitingApproval)

	cmd := newApproveCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"a-stage"})

	err := cmd.Execute()
	if err != nil {
		t.Fatalf("approve a-stage: %v", err)
	}

	// Проверяем что статус в flow-a обновился
	runDir := filepath.Join(".flowManager", "runs", "flow-a-20260101-100000")
	rs := loadRunState(t, runDir)
	if rs.Stages["a-stage"].Status != state.StatusReady {
		t.Errorf("ожидался статус ready, получили: %v", rs.Stages["a-stage"].Status)
	}
}
