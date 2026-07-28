package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm/pkg/state"
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

			runDir, stageIDs, err := state.FindLatestRunForStage(runsDir(), stageID)
			if err != nil {
				return err
			}

			store, err := state.Open(runDir, stageIDs)
			if err != nil {
				if errors.Is(err, state.ErrRunLocked) {
					return errors.New("run is active — revise via the dashboard, or stop `afm run` first")
				}
				return fmt.Errorf("open store: %w", err)
			}
			defer store.Close()

			current := store.Get(stageID)
			if current != state.StatusAwaitingApproval {
				return fmt.Errorf("stage %q is %v, not awaiting_approval", stageID, current)
			}

			stageDir := filepath.Join(runDir, stageID)

			// Version current plan
			if _, err := state.VersionPlan(stageDir); err != nil {
				return fmt.Errorf("version plan: %w", err)
			}

			// Save feedback
			if err := state.SaveFeedback(stageDir, feedback); err != nil {
				return fmt.Errorf("save feedback: %w", err)
			}

			// Transition to revising
			if err := store.Apply(&state.Transition{
				StageID: stageID,
				From:    state.StatusAwaitingApproval,
				To:      state.StatusRevising,
				Event:   "cli_revise",
				Reason:  feedback,
			}); err != nil {
				return fmt.Errorf("transition to revising: %w", err)
			}

			fmt.Printf("feedback saved for stage %q -- orchestrator will re-plan\n", stageID)
			return nil
		},
	}
	cmd.Flags().StringVar(&feedback, "feedback", "", "feedback text for plan revision")
	return cmd
}
