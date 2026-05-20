package main

import (
	"os"

	"github.com/spf13/cobra"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "flowmanager",
		Short: "Orchestrate multi-stage AI flows",
	}
	root.AddCommand(
		newRunCmd(),
		newCheckCmd(),
		newApproveCmd(),
		newReviseCmd(),
		newRetryCmd(),
		newInitCmd(),
		newListCmd(),
	)
	return root
}
