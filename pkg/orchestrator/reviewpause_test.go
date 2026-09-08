package orchestrator

import "testing"

// TestRunnerKind_SetGetClear проверяет базовый контракт реестра runnerKind
// (Task 6 фичи "review notes"): пусто по умолчанию, set/get, clear возвращает
// в пустое состояние. o := &Orchestrator{} без New() — runnerKind это голое
// sync.Map с нулевым значением, отдельной инициализации не требует.
func TestRunnerKind_SetGetClear(t *testing.T) {
	o := &Orchestrator{}
	if o.runnerKindOf("s1") != "" {
		t.Fatal("empty default")
	}
	o.setRunnerKind("s1", kindImplementation)
	if o.runnerKindOf("s1") != kindImplementation {
		t.Fatal("set/get")
	}
	o.clearRunnerKind("s1")
	if o.runnerKindOf("s1") != "" {
		t.Fatal("clear")
	}
}
