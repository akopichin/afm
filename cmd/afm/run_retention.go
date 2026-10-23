package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/akopichin/afm/pkg/state"
)

// pruneRuns удаляет из base самые старые успешные раны сверх keep. created — ID ранов, созданных этим
// процессом, в порядке создания: чужие, failed, interrupted и продолжающиеся раны не входят ни в удаление, ни в
// счёт keep. keep <= 0 ничего не удаляет. Ран с нечитаемым журналом не удаляется; ошибки называют run ID.
func pruneRuns(base string, created []string, keep int) (removed []string, err error) {
	if keep <= 0 {
		return nil, nil
	}
	var errs []error
	var successful []string
	for _, id := range created {
		ok, checkErr := isSuccessfulRun(base, id)
		if checkErr != nil {
			errs = append(errs, fmt.Errorf("run %s: %w", id, checkErr))
		} else if ok {
			successful = append(successful, id)
		}
	}
	for _, id := range successful[:max(len(successful)-keep, 0)] {
		if rmErr := os.RemoveAll(filepath.Join(base, id)); rmErr != nil {
			errs = append(errs, fmt.Errorf("run %s: remove: %w", id, rmErr))
			continue
		}
		removed = append(removed, id)
	}
	return removed, errors.Join(errs...)
}

// isSuccessfulRun читает решение из events.jsonl: ран записал run_finished и все его стадии done.
func isSuccessfulRun(base, id string) (bool, error) {
	if id == "" || id == "." || id == ".." || filepath.Base(id) != id {
		return false, errors.New("not a run directory name")
	}
	rs, err := state.LoadRunState(filepath.Join(base, id))
	if err != nil {
		return false, fmt.Errorf("read journal: %w", err)
	}
	return rs.RunStatus == state.RunStatusFinished && len(rs.Stages) > 0 && rs.AllDone(), nil
}

// prune применяет pruneRuns к ранам этого процесса и возвращает created без удалённых: в allowlist остаются
// только существующие раны, иначе следующая очистка предупреждала бы о «нечитаемом журнале» намеренно
// удалённого рана. Сбой очистки только печатается: он не должен ни останавливать повторяющийся флоу, ни
// подменять результат рана.
func prune(created []string, keep int) []string {
	base, err := filepath.Abs(runsDir())
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: repeat cleanup: %v\n", err)
		return created
	}
	removed, err := pruneRuns(base, created, keep)
	for _, id := range removed {
		fmt.Printf("afm: removed old run %s\n", id)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: repeat cleanup: %v\n", err)
	}
	return slices.DeleteFunc(slices.Clone(created), func(id string) bool { return slices.Contains(removed, id) })
}
