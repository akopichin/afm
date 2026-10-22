package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/skills"
)

func newInstallSkillsCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "install-skills",
		Short: "Install afm skills for Claude Code and Codex",
		Long: `Install afm skills into ~/.afm/skills and link them from ~/.claude/skills and
$CODEX_HOME/skills (default ~/.codex/skills).`,
		SilenceUsage: true, SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("home dir: %w", err)
			}
			report, err := skills.Sync(home, force)
			printSkillsReport(cmd.OutOrStdout(), report)
			if err == nil && len(report.Installed) == 0 && len(report.Warnings) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "all skills up to date")
			}
			if err != nil {
				// Ошибка — не неверный вызов: печатаем её без справки по команде, которую добавил бы runMain.
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", err)
				return &ExitError{Code: 1, Silent: true}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "replace real skill directories with links (the directories are moved to a backup)")
	return cmd
}

func printSkillsReport(w io.Writer, report skills.Report) {
	if report.Store != "" {
		_, _ = fmt.Fprintf(w, "afm skills: %s -> %s\n", report.Store, strings.Join(report.AgentDirs, ", "))
	}
	for _, line := range report.Installed {
		_, _ = fmt.Fprintf(w, "  + %s\n", line)
	}
	for _, line := range report.Warnings {
		_, _ = fmt.Fprintf(w, "  ! %s\n", line)
	}
	for _, path := range report.Backups {
		_, _ = fmt.Fprintf(w, "  backup: %s\n", path)
	}
}

// syncSkillsForRun обновляет скиллы afm перед раном. Только на хосте: в контейнере store смонтирован
// с хоста и принадлежит хостовой версии afm. Проблемы не прерывают ран: печатается не больше одной
// строки-сводки предупреждений и строка ошибки.
func syncSkillsForRun(home string, w io.Writer) {
	if config.InContainer() {
		return
	}
	report, err := skills.Sync(home, false)
	if n := len(report.Warnings); n > 0 {
		_, _ = fmt.Fprintf(w, "warning: afm skills: %d warning(s), run \"afm install-skills\" for details\n", n)
	}
	if err != nil {
		_, _ = fmt.Fprintf(w, "warning: afm skills: %s\n", strings.ReplaceAll(err.Error(), "\n", "; "))
	}
}
