package main

import (
	"path/filepath"
	"strings"
	"testing"

	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/state"
)

// TestFindLatestStateFileCorrectFlow воспроизводит баг: findLatestStateFile
// должна возвращать state.json для нужного флоу, а не просто последний по алфавиту.
//
// Сценарий: flow-b запущен позже (newer dir), flow-a запущен раньше (older dir).
// approve/revise вызываются для stage из flow-a.
// Старый код возвращал state.json flow-b → stage не найден → ошибка "not found".
// Новый код ищет по stageID и возвращает правильный state.json.
func TestFindLatestStateFileCorrectFlow(t *testing.T) {
	chdirTemp(t)

	// flow-a — старее, содержит "a-stage"
	makeRunState(t, "flow-a-20260101-100000", "a-stage", state.StatusAwaitingApproval)

	// flow-b — новее (позже по алфавиту/времени), содержит "b-stage"
	makeRunState(t, "flow-b-20260101-120000", "b-stage", state.StatusAwaitingApproval)

	sf, err := findLatestStateFile("a-stage")
	if err != nil {
		t.Fatalf("findLatestStateFile: %v", err)
	}
	if !strings.Contains(sf, "flow-a-20260101-100000") {
		t.Errorf("должен вернуть state.json flow-a, получили: %s", sf)
	}
}

// TestFindLatestStateFileNewestWins проверяет что при одном и том же stageID
// возвращается последний (самый новый) run.
func TestFindLatestStateFileNewestWins(t *testing.T) {
	chdirTemp(t)

	// Два рана одного флоу с одинаковым stage, второй новее
	makeRunState(t, "flow-20260101-100000", "init", state.StatusDone)
	makeRunState(t, "flow-20260101-120000", "init", state.StatusAwaitingApproval)

	sf, err := findLatestStateFile("init")
	if err != nil {
		t.Fatalf("findLatestStateFile: %v", err)
	}
	if !strings.Contains(sf, "flow-20260101-120000") {
		t.Errorf("должен вернуть новейший run, получили: %s", sf)
	}
}

// TestFindLatestStateFileNotFound — stage нигде не существует.
func TestFindLatestStateFileNotFound(t *testing.T) {
	chdirTemp(t)

	makeRunState(t, "flow-20260101-120000", "other-stage", state.StatusAwaitingApproval)

	_, err := findLatestStateFile("nonexistent-stage")
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
	runDirA := makeRunState(t, "flow-a-20260101-100000", "a-stage", state.StatusAwaitingApproval)
	_ = runDirA

	cmd := newApproveCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"a-stage"})

	// Старый код вернул бы state.json flow-b → "stage a-stage not found"
	// Новый код находит правильный state → approve проходит
	err := cmd.Execute()
	if err != nil {
		t.Fatalf("approve a-stage: %v", err)
	}

	// Проверяем что статус в flow-a обновился
	sf := filepath.Join(".flowManager", "runs", "flow-a-20260101-100000", "state.json")
	rs, err := state.Load(sf)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if rs.Stages["a-stage"].Status != state.StatusReady {
		t.Errorf("ожидался статус ready, получили: %v", rs.Stages["a-stage"].Status)
	}
}
