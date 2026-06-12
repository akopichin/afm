package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm/pkg/state"
)

func newRetryCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "retry [stage-id]",
		Short: "Retry a failed stage (transitions failed -> pending)",
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
			if current != state.StatusFailed {
				return fmt.Errorf("stage %q is %v, not failed", stageID, current)
			}
			if err := store.Apply(state.Transition{
				StageID: stageID,
				From:    state.StatusFailed,
				To:      state.StatusPending,
				Event:   "cli_retry",
			}); err != nil {
				return fmt.Errorf("retry: %w", err)
			}
			fmt.Printf("stage %q retried: run 'flowmanager run' to restart\n", stageID)
			return nil
		},
	}
}
