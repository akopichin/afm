package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm-engine/engine"
)

func newReviseCmd() *cobra.Command {
	var feedback string

	cmd := &cobra.Command{
		Use:   "revise <stage-id>",
		Short: "Send feedback for plan revision",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			stageID := args[0]

			if feedback == "" {
				data, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
				if err != nil {
					return fmt.Errorf("read stdin: %w", err)
				}
				feedback = strings.TrimSpace(string(data))
			}
			if feedback == "" {
				return errors.New("feedback is required (use --feedback or stdin)")
			}

			if err := engine.ReviseOffline(runsDir(), stageID, feedback); err != nil {
				return offlineCLIError(err, "revise")
			}

			fmt.Printf("feedback saved for stage %q -- orchestrator will re-plan\n", stageID)
			return nil
		},
	}
	cmd.Flags().StringVar(&feedback, "feedback", "", "feedback text for plan revision")
	return cmd
}
