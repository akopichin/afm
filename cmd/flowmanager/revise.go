package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/state"
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
			if st.Status != state.StatusAwaitingApproval {
				return fmt.Errorf("stage %q is %v, not awaiting_approval", stageID, st.Status)
			}

			runDir := filepath.Dir(stateFile)
			stageDir := filepath.Join(runDir, stageID)

			// Version current plan
			if _, err := state.VersionPlan(stageDir); err != nil {
				return fmt.Errorf("version plan: %w", err)
			}

			// Save feedback
			if err := state.SaveFeedback(stageDir, feedback); err != nil {
				return fmt.Errorf("save feedback: %w", err)
			}

			// Update status
			rs.SetStageStatus(stageID, state.StatusRevising)
			if err := rs.Save(stateFile); err != nil {
				return fmt.Errorf("save state: %w", err)
			}

			fmt.Printf("feedback saved for stage %q -- orchestrator will re-plan\n", stageID)
			return nil
		},
	}
	cmd.Flags().StringVar(&feedback, "feedback", "", "feedback text for plan revision")
	return cmd
}
