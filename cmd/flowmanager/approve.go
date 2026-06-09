package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm/pkg/state"
)

func newApproveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "approve [stage-id]",
		Short: "Approve a stage plan (transitions awaiting_approval → ready)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			stageID := args[0]
			stateFile, err := findLatestStateFile(stageID)
			if err != nil {
				return err
			}
			rs, err := state.Load(stateFile)
			if err != nil {
				return fmt.Errorf("load state: %w", err)
			}
			st, ok := rs.Stages[stageID]
			if !ok {
				return fmt.Errorf("stage %q not found", stageID)
			}
			if st.Status != state.StatusAwaitingApproval {
				return fmt.Errorf("stage %q is %v, not awaiting_approval", stageID, st.Status)
			}
			rs.SetStageStatus(stageID, state.StatusReady)
			if err := rs.Save(stateFile); err != nil {
				return fmt.Errorf("save state: %w", err)
			}
			fmt.Printf("approved stage %q: ready to run\n", stageID)
			return nil
		},
	}
}

// findLatestStateFile returns the state.json of the most recent run that
// contains stageID. This prevents operating on the wrong flow when multiple
// flows have runs in .flowManager/runs/.
func findLatestStateFile(stageID string) (string, error) {
	base := filepath.Join(".flowManager", "runs")
	entries, err := os.ReadDir(base)
	if err != nil {
		return "", fmt.Errorf("no runs dir: %w", err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(base, e.Name()))
		}
	}
	if len(dirs) == 0 {
		return "", errors.New("no runs found")
	}
	slices.Sort(dirs)
	slices.Reverse(dirs) // newest first
	for _, dir := range dirs {
		sf := filepath.Join(dir, "state.json")
		rs, err := state.Load(sf)
		if err != nil {
			continue
		}
		if _, ok := rs.Stages[stageID]; ok {
			return sf, nil
		}
	}
	return "", fmt.Errorf("no active run found for stage %q", stageID)
}
