package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm/pkg/flow"
)

// validateFlowFile parses and validates a flow.yaml without running any
// agents. Returns a human-readable success message, or an error wrapping
// flow.ParseFile's validation failure.
func validateFlowFile(path string) (string, error) {
	f, err := flow.ParseFile(path)
	if err != nil {
		return "", fmt.Errorf("invalid flow.yaml: %w", err)
	}
	return fmt.Sprintf("OK: %q — %d stage(s), valid", f.Name, len(f.Stages)), nil
}

func newValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <flow.yaml>",
		Short: "Validate a flow.yaml without running it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			msg, err := validateFlowFile(args[0])
			if err != nil {
				return err
			}
			fmt.Println(msg)
			return nil
		},
	}
}
