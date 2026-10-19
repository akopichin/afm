package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
)

// validateFlowFile parses and validates a flow.yaml without running any agents. Returns a human-readable
// success message, or an error wrapping flow.ParseFile's validation failure. A flow with a team: is also
// checked against the configured collaboration profiles as afm run's preflight checks a fresh run: every
// member needs its profile. A resumed run is more lenient (members it already added keep their saved
// profile), which validate does not look at — it reads no run journal.
func validateFlowFile(path string) (string, error) {
	f, err := flow.ParseFile(path)
	if err != nil {
		return "", fmt.Errorf("invalid flow.yaml: %w", err)
	}
	if err := validateTeamProfiles(f.Team); err != nil {
		return "", fmt.Errorf("invalid flow.yaml: %w", err)
	}
	return fmt.Sprintf("OK: %q — %d stage(s), valid", f.Name, len(f.Stages)), nil
}

// validateTeamProfiles checks the team's profiles against the global and project config, the same layers
// afm run reads. Config is loaded only for a flow that declares a team.
func validateTeamProfiles(team flow.Team) error {
	if len(team) == 0 {
		return nil
	}
	home, _ := os.UserHomeDir()
	cfg, err := config.LoadFrom(filepath.Join(home, config.AfmDir), fmDir())
	if err != nil {
		return err
	}
	if !cfg.Collaboration.IsEnabled() {
		warnTeamWithoutCollaboration(team)
		return nil
	}
	reg, err := buildProfileRegistry(cfg)
	if err != nil {
		return fmt.Errorf("collaboration.profiles: %w (afm run would turn collaboration off)", err)
	}
	return checkTeamProfiles(team, reg)
}

func newValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <flow.yaml>",
		Short: "Validate a flow.yaml for a fresh run without running it",
		Long: "Validate a flow.yaml without running it: the flow itself and, for a team:, that every member's\n" +
			"profile is configured — the checks a fresh afm run does at startup. It reads no run journal, so it\n" +
			"does not know which team agents an existing run already added.",
		Args: cobra.ExactArgs(1),
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
