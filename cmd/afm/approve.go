package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm-engine/engine"
)

func newApproveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "approve [stage-id]",
		Short: "Approve a stage plan (transitions awaiting_approval → ready)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			stageID := args[0]
			if err := engine.ApproveOffline(runsDir(), stageID); err != nil {
				return offlineCLIError(err, "approve")
			}
			fmt.Printf("approved stage %q: ready to run\n", stageID)
			return nil
		},
	}
}

// offlineCLIError words the active-run rejection for the CLI; other errors already read well.
func offlineCLIError(err error, verb string) error {
	if errors.Is(err, engine.ErrRunActive) {
		return fmt.Errorf("run is active — %s via the dashboard, or stop `afm run` first", verb)
	}
	return err
}
