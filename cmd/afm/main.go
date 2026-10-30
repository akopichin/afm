package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/docker"
	"github.com/akopichin/afm/pkg/executor"
)

var rootDir string
var debugEnabled bool

// version вшивается через -ldflags "-X main.version=…" при сборке
// (Makefile build/install, Dockerfile.runtime ARG AFM_VERSION). По умолчанию "dev".
var version = "dev"

// absRootDir — afm-root (rootDir: флаг --dir, иначе AFM_DIR, иначе ".") абсолютным путём на момент вызова.
// Пути, выведенные из afm-root (раны, worktree, root_dir, память), уходят агентам, чей CWD может быть
// worktree, поэтому они должны быть абсолютными.
func absRootDir() string {
	if abs, err := filepath.Abs(rootDir); err == nil {
		return abs
	}
	return rootDir
}

// fmDir возвращает путь к служебному каталогу .afm относительно rootDir. rootDir задаётся флагом --dir,
// иначе переменной AFM_DIR, иначе "." (PersistentPreRunE в корневой команде).
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

// resolveRootDir определяет базовую директорию для .afm по приоритету: явный флаг --dir важнее переменной
// окружения AFM_DIR, а та важнее текущей директории. dirFlag пуст, если флаг не задан.
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
	enabled, _ := parseEnvBool(env)
	return enabled
}

// parseEnvBool разбирает булево значение env: 1/true/yes/on и 0/false/no/off, пустое — выключено.
// known=false для любого другого значения.
func parseEnvBool(env string) (enabled, known bool) {
	switch strings.ToLower(strings.TrimSpace(env)) {
	case "1", "true", yesString, "on":
		return true, true
	case "", "0", "false", noString, "off":
		return false, true
	default:
		return false, false
	}
}

func main() {
	// A copy of afm started as an agent's process group anchor does nothing else (see pkg/executor).
	if executor.ServeProcessGroupAnchor(os.Args) {
		return
	}
	os.Exit(runMain(os.Args[1:]))
}

// runMain возвращает код завершения команды без os.Exit, чтобы его можно было проверить в тестах.
func runMain(args []string) int {
	root := newRootCmd()
	root.SetArgs(args)
	cmd, err := root.ExecuteC()
	if err == nil {
		return 0
	}

	var subprocessErr *docker.SubprocessExitError
	if errors.As(err, &subprocessErr) {
		// Возвращаем код завершения контейнера, включая сигнальный выход.
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
	// Send usage to stderr: stdout belongs to the browser MCP's JSON-RPC transport.
	cmd.PrintErrln(cmd.UsageString())
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
		newBrowserCmd(),
		newAgentCmd(),
	)
	return root
}
