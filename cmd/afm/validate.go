package main

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm-engine/engine"
	"github.com/akopichin/afm/contracts/accounting"
	"github.com/akopichin/afm/contracts/budget"
	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/contracts/flow"
)

// validateFlowFile parses and validates a flow.yaml without running any agents. Returns a human-readable
// success message, or an error wrapping flow.ParseFile's validation failure. A flow with stage assistants: or
// a team: is also checked against the collaboration profiles as afm run's preflight checks a fresh run: every
// helper needs its profile. A resumed run is more lenient (members it already added keep their saved
// profile), which validate does not look at — it reads no run journal.
func validateFlowFile(path string) (string, error) {
	f, err := flow.ParseFile(path)
	if err != nil {
		return "", fmt.Errorf("invalid flow.yaml: %w", err)
	}
	if err := validateHelperProfiles(f); err != nil {
		return "", fmt.Errorf("invalid flow.yaml: %w", err)
	}
	if err := validateMoneyBudgetRates(f); err != nil {
		return "", fmt.Errorf("invalid flow.yaml: %w", err)
	}
	return fmt.Sprintf("OK: %q — %d stage(s), valid", f.Name, len(f.Stages)), nil
}

// validateHelperProfiles checks the stage assistants and the team against the builtin default and the global
// and project config, with the registry and checks afm run uses. Config is loaded only for a flow that declares
// helpers. Whether a helper's executor exists on this machine is not checked: PATH differs in the container.
func validateHelperProfiles(f *flow.Flow) error {
	declaresAssistants := slices.ContainsFunc(f.Stages, func(s flow.Stage) bool { return len(s.Assistants) > 0 })
	if len(f.Team) == 0 && !declaresAssistants {
		return nil
	}
	home, _ := os.UserHomeDir()
	cfg, err := loadProductConfig(filepath.Join(home, config.AfmDir), fmDir())
	if err != nil {
		return err
	}
	if !cfg.Collaboration.IsEnabled() {
		engine.WarnAssistantsWithoutCollaboration(f.Stages)
		engine.WarnTeamWithoutCollaboration(f.Team)
		return nil
	}
	reg, err := engine.BuildProfileRegistry(cfg)
	if err != nil {
		return fmt.Errorf("collaboration.profiles: %w (afm run would turn collaboration off)", err)
	}
	if err := engine.CheckStageAssistants(f.Stages, reg); err != nil {
		return err
	}
	return engine.CheckTeamProfiles(f.Team, reg)
}

// validateMoneyBudgetRates refuses what engine Open refuses: a money budget over an agent command whose model has
// no explicit rates. The scope is Open's: agent stages and verify agent steps under a money limit.
func validateMoneyBudgetRates(f *flow.Flow) error {
	if !declaresMoneyBudget(f) {
		return nil
	}
	home, _ := os.UserHomeDir()
	cfg, err := loadProductConfig(filepath.Join(home, config.AfmDir), fmDir())
	if err != nil {
		return err
	}
	for _, stage := range f.Stages {
		scope := moneyBudgetScope(f, stage)
		if scope == "" {
			continue
		}
		for _, command := range stageAgentCommands(stage, cfg) {
			check := accounting.CheckExplicitRates(cfg.Pricing, cfg.UsageHint(command))
			if check.Status == accounting.RateCheckMissing {
				return fmt.Errorf("money budget of %s needs explicit rates: command %q, model %q, missing %s",
					scope, command, check.Key, strings.Join(check.Missing, ", "))
			}
		}
	}
	return nil
}

func declaresMoneyBudget(f *flow.Flow) bool {
	return hasMoneyLimit(f.Budget) || slices.ContainsFunc(f.Stages, func(s flow.Stage) bool { return hasMoneyLimit(s.Budget) })
}

func hasMoneyLimit(spec *budget.Spec) bool { return spec != nil && spec.MoneyUSD > 0 }

// moneyBudgetScope is the nearest scope above the stage with a money limit: the stage first, then the run.
func moneyBudgetScope(f *flow.Flow, stage flow.Stage) budget.ScopeID {
	switch {
	case hasMoneyLimit(stage.Budget):
		return budget.StageScope(stage.ID)
	case hasMoneyLimit(f.Budget):
		return budget.RunScope()
	}
	return ""
}

// stageAgentCommands lists the agent commands a stage runs: its own or the default client, and the commands of
// its verify agent steps. A script stage runs no agent.
func stageAgentCommands(stage flow.Stage, cfg config.Config) []string {
	var commands []string
	if !stage.IsScript() {
		commands = append(commands, cmp.Or(stage.Command, cfg.Client.Command))
	}
	for _, step := range stage.Verify.Steps {
		if step.Kind == flow.VerifyAgent && step.Command != "" {
			commands = append(commands, step.Command)
		}
	}
	return commands
}

func newValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <flow.yaml>",
		Short: "Validate a flow.yaml for a fresh run without running it",
		Long: "Validate a flow.yaml without running it: the flow itself and, for stage assistants: and a team:,\n" +
			"that every helper's profile exists — the checks a fresh afm run does at startup. It reads no run\n" +
			"journal, so it does not know which team agents an existing run already added.",
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
