package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/state"
)

func newRetryCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "retry [stage-id]",
		Short: "Retry a failed stage (transitions failed -> pending)",
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
			if st.Status != state.StatusFailed {
				return fmt.Errorf("stage %q is %v, not failed", stageID, st.Status)
			}
			rs.SetStageStatus(stageID, state.StatusPending)
			if err := rs.Save(stateFile); err != nil {
				return fmt.Errorf("save state: %w", err)
			}
			fmt.Printf("stage %q retried: run 'flowmanager run' to restart\n", stageID)
			return nil
		},
	}
}
