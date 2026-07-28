package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorBlue   = "\033[34m"
	colorCyan   = "\033[36m"
	colorGray   = "\033[90m"
)

func statusColor(s state.StageStatus) string {
	switch s {
	case state.StatusDone:
		return colorGreen
	case state.StatusFailed:
		return colorRed
	case state.StatusAwaitingApproval:
		return colorYellow
	case state.StatusRunning, state.StatusPlanning:
		return colorBlue
	case state.StatusRevising:
		return colorCyan
	default:
		return colorGray
	}
}

func newCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Show status of the latest flow run",
		RunE: func(cmd *cobra.Command, args []string) error {
			base := filepath.Join(fmDir(), "runs")
			entries, err := os.ReadDir(base)
			if err != nil {
				return fmt.Errorf("no runs found in %s", base)
			}

			var dirs []string
			for _, e := range entries {
				if e.IsDir() {
					dirs = append(dirs, filepath.Join(base, e.Name()))
				}
			}
			if len(dirs) == 0 {
				return errors.New("no runs found")
			}
			slices.Sort(dirs)
			latest := dirs[len(dirs)-1]

			rs, err := state.LoadRunState(latest)
			if err != nil {
				return fmt.Errorf("load state: %w", err)
			}

			fmt.Printf("Run: %s\n\n", filepath.Base(latest))
			fmt.Printf("%-20s  %-22s  %-10s  %s\n", "STAGE", "STATUS", "UPDATED", "LAST ACTION")
			fmt.Printf("%-20s  %-22s  %-10s  %s\n", "-----", "------", "-------", "-----------")

			type row struct{ id, status, updated, lastAction string }
			var rows []row
			for id, s := range rs.Stages {
				action := lastLogAction(filepath.Join(latest, id))
				rows = append(rows, row{
					id:         id,
					status:     string(s.Status),
					updated:    s.UpdatedAt.Format("15:04:05"),
					lastAction: action,
				})
			}
			slices.SortFunc(rows, func(a, b row) int {
				if a.id < b.id {
					return -1
				}
				if a.id > b.id {
					return 1
				}
				return 0
			})
			for _, r := range rows {
				color := statusColor(state.StageStatus(r.status))
				fmt.Printf("%-20s  %s%-22s%s  %-10s  %s\n",
					r.id, color, r.status, colorReset, r.updated, r.lastAction)
			}
			return nil
		},
	}
}

func lastLogAction(stageDir string) string {
	var last string
	for _, p := range flow.Phases() {
		for _, name := range flow.PhaseLogFiles(p) {
			data, err := os.ReadFile(filepath.Join(stageDir, name))
			if err != nil || len(data) == 0 {
				continue
			}
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) > 0 && lines[len(lines)-1] != "" {
				last = lines[len(lines)-1]
			}
		}
	}
	if len(last) > 60 {
		last = last[:60] + "..."
	}
	return last
}
