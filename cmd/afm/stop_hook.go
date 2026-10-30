package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm/pkg/executor"
)

// newStopHookCmd registers the internal `afm stop-hook`: the command a CLI's Stop hook runs. It asks the live
// orchestrator (through the bridge its descriptor names) whether the session may end.
func newStopHookCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "stop-hook",
		Short:  "Answer an agent CLI's Stop hook from the live orchestrator (internal)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			output, err := executor.RunStopHook(cmd.Context(), os.Getenv(executor.StopHookDescriptorEnv), cmd.InOrStdin())
			if err != nil {
				// Exit 0 with an empty answer keeps a broken hook from blocking the CLI; stderr carries the cause.
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "afm stop-hook:", err)
			}
			_, werr := cmd.OutOrStdout().Write(output)
			return werr
		},
	}
}
