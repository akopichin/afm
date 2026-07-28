package main

import (
	"errors"
	"fmt"

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
			runDir, stageIDs, err := state.FindLatestRunForStage(runsDir(), stageID)
			if err != nil {
				return err
			}
			store, err := state.Open(runDir, stageIDs)
			if err != nil {
				if errors.Is(err, state.ErrRunLocked) {
					return errors.New("run is active — approve via the dashboard, or stop `afm run` first")
				}
				return fmt.Errorf("open store: %w", err)
			}
			defer store.Close()

			current := store.Get(stageID)
			if current != state.StatusAwaitingApproval {
				return fmt.Errorf("stage %q is %v, not awaiting_approval", stageID, current)
			}
			if err := store.Apply(&state.Transition{
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
