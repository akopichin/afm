package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/docker"
)

var rootDir string
var debugEnabled bool

// version вшивается через -ldflags "-X main.version=…" при сборке
// (Makefile build/install, Dockerfile.runtime ARG AFM_VERSION). По умолчанию "dev".
var version = "dev"

// fmDir возвращает путь к служебному каталогу .afm относительно rootDir.
// rootDir задаётся флагом --dir, иначе переменной AFM_DIR, иначе "."
// (PersistentPreRunE в корневой команде).
func fmDir() string {
	return filepath.Join(rootDir, config.AfmDir)
}

// runsDir возвращает путь к каталогу с ранами внутри .afm.
func runsDir() string {
	return filepath.Join(fmDir(), "runs")
}

// flowsDir возвращает путь к каталогу с flow.yaml внутри .afm.
func flowsDir() string {
	return filepath.Join(fmDir(), "flows")
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

// resolveDebug: флаг --debug важнее env AFM_DEBUG (1/true/yes/on).
func resolveDebug(flag bool, env string) bool {
	if flag {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(env)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func main() {
	os.Exit(runMain(os.Args[1:]))
}

// runMain выполняет корневую команду и переводит любой ошибочный результат
// в код завершения процесса, сам не вызывая os.Exit — так main() остаётся
// тонкой обёрткой, а сам перевод error → exit code проверяем в тестах
// (включая настоящий процесс, см. exit_test.go) без падения тестового
// бинаря.
func runMain(args []string) int {
	root := newRootCmd()
	root.SetArgs(args)
	cmd, err := root.ExecuteC()
	if err == nil {
		return 0
	}

	var subprocessErr *docker.SubprocessExitError
	if errors.As(err, &subprocessErr) {
		// Код завершения docker-контейнера (в т.ч. 0 — успех) передаётся
		// через error-канал Cobra чисто как транспорт для os.Exit(code) в
		// main; это не настоящая ошибка пользователя, поэтому подавляем
		// её через SilenceErrors/SilenceUsage на root и не печатаем здесь.
		return subprocessErr.Code
	}

	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		if !exitErr.Silent {
			cmd.PrintErrln(cmd.ErrPrefix(), err.Error())
		}
		return exitErr.Code
	}

	cmd.PrintErrln(cmd.ErrPrefix(), err.Error())
	cmd.Println(cmd.UsageString())
	return 1
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "afm",
		Short:         "Orchestrate multi-stage AI flows",
		SilenceErrors: true,
		SilenceUsage:  true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			rootDir = resolveRootDir(rootDir, os.Getenv("AFM_DIR"))
			debugEnabled = resolveDebug(debugEnabled, os.Getenv("AFM_DEBUG"))
			if debugEnabled {
				// чтобы re-exec внутри Docker тоже логировал (launcher прокидывает AFM_DEBUG)
				_ = os.Setenv("AFM_DEBUG", "1")
			}
			return nil
		},
	}
	root.Version = version // cobra регистрирует флаг --version
	root.PersistentFlags().StringVar(&rootDir, "dir", "", "base directory for .afm (default: current dir, env: AFM_DIR)")
	root.PersistentFlags().BoolVar(&debugEnabled, "debug", false, "log exact agent input (prompt) to <run>/debug.log and per-stage <phase>.prompt.log (env: AFM_DEBUG)")
	root.AddCommand(
		newRunCmd(),
		newCheckCmd(),
		newReportCmd(),
		newApproveCmd(),
		newReviseCmd(),
		newRetryCmd(),
		newInitCmd(),
		newValidateCmd(),
		newListCmd(),
		newInstallSkillsCmd(),
		newMemoryCmd(),
	)
	return root
}
