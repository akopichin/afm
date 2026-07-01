package main

import (
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var rootDir string

// fmDir возвращает путь к служебному каталогу .afm относительно rootDir.
// rootDir задаётся флагом --dir, иначе переменной AFM_DIR, иначе "."
// (PersistentPreRunE в корневой команде).
func fmDir() string {
	return filepath.Join(rootDir, ".afm")
}

// resolveRootDir определяет базовую директорию для .afm по приоритету:
// явный флаг --dir важнее переменной окружения AFM_DIR, а та важнее
// текущей директории. dirFlag пуст, если флаг не задан.
func resolveRootDir(dirFlag, envDir string) string {
	switch {
	case dirFlag != "":
		return dirFlag
	case envDir != "":
		return envDir
	default:
		return "."
	}
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "afm",
		Short: "Orchestrate multi-stage AI flows",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			rootDir = resolveRootDir(rootDir, os.Getenv("AFM_DIR"))
			return nil
		},
	}
	root.PersistentFlags().StringVar(&rootDir, "dir", "", "base directory for .afm (default: current dir, env: AFM_DIR)")
	root.AddCommand(
		newRunCmd(),
		newCheckCmd(),
		newApproveCmd(),
		newReviseCmd(),
		newRetryCmd(),
		newInitCmd(),
		newListCmd(),
		newInstallSkillsCmd(),
	)
	return root
}
