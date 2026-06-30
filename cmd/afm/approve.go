package main

import (
	"encoding/json"
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
			runDir, stageIDs, err := findLatestRunDir(stageID)
			if err != nil {
				return err
			}
			store, err := state.Open(runDir, stageIDs)
			if err != nil {
				return fmt.Errorf("open store: %w", err)
			}
			defer store.Close()

			current := store.Get(stageID)
			if current != state.StatusAwaitingApproval {
				return fmt.Errorf("stage %q is %v, not awaiting_approval", stageID, current)
			}
			if err := store.Apply(state.Transition{
				StageID: stageID,
				From:    state.StatusAwaitingApproval,
				To:      state.StatusReady,
				Event:   "cli_approve",
			}); err != nil {
				return fmt.Errorf("approve: %w", err)
			}
			fmt.Printf("approved stage %q: ready to run\n", stageID)
			return nil
		},
	}
}

// findLatestRunDir returns the run directory and all stage IDs of the most
// recent run that contains stageID. This prevents operating on the wrong flow
// when multiple flows have runs in .afm/runs/.
func findLatestRunDir(stageID string) (runDir string, stageIDs []string, err error) {
	base := filepath.Join(fmDir(), "runs")
	entries, readErr := os.ReadDir(base)
	if readErr != nil {
		return "", nil, fmt.Errorf("no runs dir: %w", readErr)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(base, e.Name()))
		}
	}
	if len(dirs) == 0 {
		return "", nil, errors.New("no runs found")
	}
	slices.Sort(dirs)
	slices.Reverse(dirs) // newest first
	for _, dir := range dirs {
		sf := filepath.Join(dir, "state.json")
		sd, loadErr := os.ReadFile(sf)
		if loadErr != nil {
			continue
		}
		var rs state.RunState
		if jsonErr := json.Unmarshal(sd, &rs); jsonErr != nil {
			continue
		}
		if _, ok := rs.Stages[stageID]; ok {
			ids := make([]string, 0, len(rs.Stages))
			for id := range rs.Stages {
				ids = append(ids, id)
			}
			return dir, ids, nil
		}
	}
	return "", nil, fmt.Errorf("no active run found for stage %q", stageID)
}
