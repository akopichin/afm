package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm-engine/engine"
)

func newRetryCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "retry [stage-id]",
		Short: "Retry a failed stage (transitions failed -> pending)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			stageID := args[0]
			if err := engine.RetryOffline(runsDir(), stageID); err != nil {
				return offlineCLIError(err, "retry")
			}
			fmt.Printf("stage %q retried: run 'afm run' to restart\n", stageID)
			return nil
		},
	}
}
