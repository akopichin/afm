package assistants

import (
	"errors"
	"fmt"
)

// Сентинелы инвариантов дерева задач. Оборачиваются через fmt.Errorf("...: %w", ...)
// в ValidateTree, поэтому проверяй через errors.Is, а не сравнение строк.
var (
	// ErrCycle — цепочка родителей зацикливается (A -> B -> A).
	ErrCycle = errors.New("assistants: parent chain forms a cycle")
	// ErrMissingParent — parent_task_id ссылается на несуществующую задачу.
	ErrMissingParent = errors.New("assistants: parent task not found")
	// ErrCrossStageParent — родитель принадлежит другой корневой стадии.
	ErrCrossStageParent = errors.New("assistants: parent belongs to a different root stage")
	// ErrCrossRunParent — родитель принадлежит другому рану.
	ErrCrossRunParent = errors.New("assistants: parent belongs to a different run")
)

// ComputeDepth вычисляет глубину задачи по её родителю. Задача без родителя —
// первый уровень (Depth == 1).
func ComputeDepth(parent *Task) int {
	if parent == nil {
		return 1
	}
	return parent.Depth + 1
}

// ValidateParent проверяет одно ребро child -> parent. parent == nil при
// child.ParentTaskID != nil означает, что вызывающий уже не нашёл родителя —
// это тоже ErrMissingParent, чтобы у всех вызывающих был один источник этой
// ошибки.
func ValidateParent(child, parent *Task, runID string) error {
	if child == nil {
		return errors.New("assistants: nil child task")
	}
	if child.ParentTaskID == nil {
		return nil // первый уровень — родителя не требуется
	}
	if parent == nil {
		return ErrMissingParent
	}
	if parent.ID == child.ID {
		return ErrCycle
	}
	if parent.RunID != runID || child.RunID != runID {
		return ErrCrossRunParent
	}
	if parent.RootStageID != child.RootStageID {
		return ErrCrossStageParent
	}
	return nil
}

// ValidateTree проверяет инварианты целого набора задач одной стадии/рана:
// каждый parent_task_id существует, принадлежит тому же рану и той же
// корневой стадии, и цепочка родителей не зацикливается. Используется при
// восстановлении дерева из журнала, где данные уже не проходят через
// ValidateParent по одному ребру за раз.
func ValidateTree(tasks []Task, runID string) error {
	byID := make(map[TaskID]Task, len(tasks))
	for _, t := range tasks {
		byID[t.ID] = t
	}

	for _, t := range tasks {
		visited := map[TaskID]bool{t.ID: true}
		cur := t
		for cur.ParentTaskID != nil {
			parent, ok := byID[*cur.ParentTaskID]
			if !ok {
				return fmt.Errorf("task %s: %w", t.ID, ErrMissingParent)
			}
			if err := ValidateParent(&cur, &parent, runID); err != nil {
				return fmt.Errorf("task %s: %w", t.ID, err)
			}
			if visited[parent.ID] {
				return fmt.Errorf("task %s: %w", t.ID, ErrCycle)
			}
			visited[parent.ID] = true
			cur = parent
		}
	}
	return nil
}

// BlocksRootCompletion сообщает, блокирует ли задача t завершение корневой
// стадии. Поднимается по цепочке предков до прямого потомка стадии; true
// только если КАЖДОЕ ребро по пути — required. Обязательность не повышается
// при вложении (§4.3): одно advisory-звено где угодно в цепочке размыкает
// блокировку для всех задач ниже него.
func BlocksRootCompletion(t Task, lookup func(TaskID) (Task, bool)) bool {
	cur := t
	for {
		if cur.Mode != ModeRequired {
			return false
		}
		if cur.ParentTaskID == nil {
			return true
		}
		parent, ok := lookup(*cur.ParentTaskID)
		if !ok {
			// Защитно: оборванная ссылка на родителя не должна навечно
			// заблокировать стадию.
			return false
		}
		cur = parent
	}
}

// TaskGateClosed сообщает, закрыла ли задача свой собственный required-гейт: статус обязан быть
// StatusCompleted (failed/cancelled — никогда не закрывает, см. TestTaskGate_CancelIsNotPass), и последний
// зафиксированный вердикт обязан быть VerdictPass (hasVerdict=false — для Completed-задачи без
// зафиксированного результата — fail-closed, т.е. гейт остаётся открытым, а не считается выполненным по
// умолчанию).
func TaskGateClosed(status TaskStatus, verdict Verdict, hasVerdict bool) bool {
	if status != StatusCompleted {
		return false
	}
	return hasVerdict && verdict.ClosesGate()
}

// RequiredGateOpen обходит ТЕКУЩЕЕ поддерево required-цепочки, начиная с roots (прямые дети стадии текущего
// поколения — фильтрация по поколению остаётся на вызывающем, у pkg/assistants нет понятия "поколение"), и
// возвращает первую задачу, чей required-гейт ещё не закрыт. В advisory-ветку обход НЕ заходит (§4.3: одно
// advisory-звено размыкает блокировку для всего, что ниже) — advisory-узел просто пропускается без рекурсии
// в его детей. Обход внутри required-узла продолжается НЕЗАВИСИМО от того, закрыт ли сам узел: даже если он
// уже закрыт, его required-дети могли остаться открытыми и всё ещё блокируют стадию.
func RequiredGateOpen(roots []Task, children func(Task) []Task, closed func(Task) bool) (bool, *Task) {
	for _, t := range roots {
		if t.Mode != ModeRequired {
			continue
		}
		if !closed(t) {
			blocker := t
			return true, &blocker
		}
		if open, blocker := RequiredGateOpen(children(t), children, closed); open {
			return true, blocker
		}
	}
	return false, nil
}
