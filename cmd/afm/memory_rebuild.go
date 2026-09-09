package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// rebuildOptions — параметры `afm memory rebuild`, собранные из флагов
// команды. CommitSet отличает "флаг --commit/--no-commit реально передан" от
// "не передан вовсе" — Commit сам по себе (bool) этого не умеет: значение
// по умолчанию false неотличимо от явного --no-commit (review #12).
type rebuildOptions struct {
	FlowArg, RunID       string
	DryRun, ForceReflect bool
	CommitSet, Commit    bool
}

// rebuildHandler — единственная точка входа в реализацию команды; заглушка
// на Task 1, реализуется в последующих задачах плана. Var, а не func —
// тесты подменяют её (seam).
var rebuildHandler = func(ctx context.Context, o rebuildOptions) error {
	return errors.New("memory rebuild: not implemented")
}

func newMemoryRebuildCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rebuild [flow.yaml]",
		Short: "Rebuild agent memory from a completed run's session logs",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commit := cmd.Flags().Changed("commit") // review #12: Changed(), не значение
			noCommit := cmd.Flags().Changed("no-commit")
			if commit && noCommit {
				return errors.New("--commit and --no-commit are mutually exclusive")
			}
			o := rebuildOptions{CommitSet: commit || noCommit}
			if commit {
				v, _ := cmd.Flags().GetBool("commit")
				o.Commit = v
			}
			if noCommit {
				o.Commit = false
			}
			o.DryRun, _ = cmd.Flags().GetBool("dry-run")
			o.ForceReflect, _ = cmd.Flags().GetBool("force-reflect")
			o.RunID, _ = cmd.Flags().GetString("run")
			if len(args) == 1 {
				o.FlowArg = args[0]
			}
			return rebuildHandler(cmd.Context(), o)
		},
	}
	cmd.Flags().String("run", "", "explicit run id (default: latest completed run of the flow)")
	cmd.Flags().Bool("dry-run", false, "show diffs without writing memory or committing")
	cmd.Flags().Bool("force-reflect", false, "regenerate reflect datasets from logs")
	cmd.Flags().Bool("commit", false, "git-commit changed memory files")
	cmd.Flags().Bool("no-commit", false, "do not git-commit even if the flow enables it")
	return cmd
}

// resolveExplicitRunDir проверяет id, явно переданный через --run, и
// возвращает путь к его run-директории под runsBase. id обязан быть голым
// именем директории (без разделителей пути, без ".."/"." — Lstat вместо Stat,
// чтобы отклонить симлинк на run-директорию (review #7): подмена через
// симлинк не должна давать доступ за пределы runsBase.
func resolveExplicitRunDir(runsBase, flowName, id string) (string, error) {
	if id == "" || filepath.Base(id) != id || id == "." || id == ".." ||
		strings.ContainsAny(id, `/\`) {
		return "", fmt.Errorf("invalid run id %q: must be a bare run directory name", id)
	}
	prefix := flowName + "-"
	if len(id) <= len(prefix) || id[:len(prefix)] != prefix || id[len(prefix)] < '0' || id[len(prefix)] > '9' {
		return "", fmt.Errorf("run id %q does not belong to flow %q", id, flowName)
	}
	dir := filepath.Join(runsBase, id)
	info, err := os.Lstat(dir) // Lstat: отклонить симлинкованную run-директорию (review #7)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("run %q not found (or not a real directory) under %s", id, runsBase)
	}
	return dir, nil
}
