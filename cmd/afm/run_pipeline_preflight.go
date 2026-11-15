package main

import (
	"errors"
	"fmt"
	"maps"
	"os/exec"
	"slices"
	"strings"

	"github.com/akopichin/afm-engine/engine"
	"github.com/akopichin/afm/contracts/assistants"
	"github.com/akopichin/afm/contracts/browser"
	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/contracts/docker"
	"github.com/akopichin/afm/contracts/flow"
	"github.com/akopichin/afm/contracts/secrets"
	"github.com/akopichin/afm/pkg/pipeline"
)

// Requirement check outcomes.
const (
	checkOK         = "ok"
	checkMissing    = "missing"
	checkNotChecked = "not_checked"
)

// requirementCheck is one environment need of a pipeline and whether this machine meets it.
type requirementCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// executorReport is what a command runs as after layering: the recipe's client, model and secret reference, or a
// plain executable when no recipe defines the command.
type executorReport struct {
	Command      string `json:"command"`
	CommandLayer string `json:"command_layer,omitempty"`
	Client       string `json:"client,omitempty"`
	Model        string `json:"model,omitempty"`
	Reasoning    string `json:"reasoning,omitempty"`
	URL          string `json:"url,omitempty"`
	AuthFrom     string `json:"auth_from,omitempty"`
}

// roleReport is a manifest role with its effective settings after layering, shown by the banner and inspect.
// ProfileExecutor is what the helper profile runs when it names a command other than the role's;
// ProfileExecutorInheritance replaces it for a profile without a command, whose executor depends on where it starts.
type roleReport struct {
	ID          string `json:"id"`
	Description string `json:"description,omitempty"`
	executorReport
	Profile                    string          `json:"profile,omitempty"`
	ProfileTitle               string          `json:"profile_title,omitempty"`
	ProfileLayer               string          `json:"profile_layer,omitempty"`
	ProfileExecutor            *executorReport `json:"profile_executor,omitempty"`
	ProfileExecutorInheritance string          `json:"profile_executor_inheritance,omitempty"`
}

// inheritedHelperExecutor is the order assistants.ResolveExecution inherits an executor in.
const inheritedHelperExecutor = "the delegating agent's executor, else stage.command, else client.command"

// describeExecutor resolves command through the effective docker.agents recipes.
func describeExecutor(command string, cfg config.Config, prov pipeline.Provenance) executorReport {
	report := executorReport{Command: command}
	if recipe, ok := cfg.Docker.Agents[command]; ok {
		report.CommandLayer = string(prov.Agents[command])
		report.Client = recipeClient(recipe)
		report.Model, report.Reasoning, report.URL, report.AuthFrom = recipe.Model, recipe.Reasoning, recipe.URL, recipe.Auth.From
	}
	return report
}

// describeRoles lists the manifest roles in id order with their effective recipe and profile. Only references to
// secrets (auth.from) are shown, never their values.
func describeRoles(m pipeline.Manifest, cfg config.Config, prov pipeline.Provenance) []roleReport {
	ids := slices.Sorted(maps.Keys(m.Roles))
	reports := make([]roleReport, 0, len(ids))
	for _, id := range ids {
		role := m.Roles[id]
		report := roleReport{ID: id, Description: role.Description, executorReport: describeExecutor(role.Command, cfg, prov), Profile: role.Profile}
		if profile, ok := cfg.Collaboration.Profiles[role.Profile]; ok && role.Profile != "" {
			report.ProfileTitle = profile.Title
			report.ProfileLayer = string(prov.Profiles[role.Profile])
			// Without a run there is nothing to inherit from, so the shared resolver fixes an executor only for a
			// profile that names its own command.
			helper, err := assistants.ResolveExecution(assistants.Profile{Command: profile.Command}, assistants.ExecutionInputs{})
			switch {
			case errors.Is(err, assistants.ErrNoExecutor):
				report.ProfileExecutorInheritance = inheritedHelperExecutor
			case err == nil && helper.Command != role.Command:
				executor := describeExecutor(helper.Command, cfg, prov)
				report.ProfileExecutor = &executor
			default:
				// The profile runs the role's own command, which the report already shows.
			}
		}
		reports = append(reports, report)
	}
	return reports
}

// recipeClient names the client a recipe drives; an empty type is the default claude.
func recipeClient(r config.AgentRecipe) string {
	if r.Type == "" {
		return config.ClaudeCommand
	}
	return r.Type
}

// checkPipelineRequirements checks what the package declares plus what its recipes need to run here. It reads only
// configuration, the environment and PATH; it never starts an agent or probes a provider.
func checkPipelineRequirements(f *flow.Flow, cfg config.Config, m pipeline.Manifest) ([]requirementCheck, error) {
	dockerOn, autoShim := cfg.Docker.IsDockerEnabled(), cfg.Docker.IsAutoShim()
	var checks []requirementCheck
	add := func(name string, ok bool, detail string) {
		status := checkOK
		if !ok {
			status = checkMissing
		}
		checks = append(checks, requirementCheck{Name: name, Status: status, Detail: detail})
	}

	if m.Requirements.DockerAutoShim {
		switch {
		case !dockerOn:
			add("docker_auto_shim", true, "docker is not enabled; recipe commands must be executables on PATH")
		case !autoShim:
			add("docker_auto_shim", false, "docker is enabled but docker.autoShim is not: recipe commands cannot run in the container")
		default:
			add("docker_auto_shim", true, "docker.autoShim is on")
		}
	}
	if m.Requirements.Collaboration {
		add("collaboration", cfg.Collaboration.IsEnabled(), "collaboration is disabled (collaboration.enabled or AFM_COLLABORATION)")
	}
	if m.Requirements.Browser {
		mode := browser.BrowserModeNative
		if dockerOn {
			mode = browser.BrowserModeDocker
		}
		status, err := cfg.BrowserPreflight(mode)
		add("browser", err == nil && status.Available, status.Reason)
	}
	for _, tool := range m.Requirements.Tools {
		if dockerOn {
			checks = append(checks, requirementCheck{Name: "tool:" + tool, Status: checkNotChecked, Detail: "docker is enabled: tools come from the container image"})
			continue
		}
		_, err := exec.LookPath(tool)
		add("tool:"+tool, err == nil, "not found on PATH")
	}

	proj, err := engine.ProjectDocker(f, cfg, nil)
	if err != nil {
		return nil, err
	}
	for _, command := range proj.Commands {
		if _, isRecipe := cfg.Docker.Agents[command]; !isRecipe {
			continue
		}
		switch _, lookErr := exec.LookPath(command); {
		case dockerOn && autoShim:
			add("command:"+command, true, "recipe wrapper is generated in the container")
		case lookErr == nil:
			add("command:"+command, true, "executable on PATH")
		default:
			add("command:"+command, false, fmt.Sprintf("recipe command %q needs docker with autoShim, or an executable named %q on PATH", command, command))
		}
	}
	checks = append(checks, secretChecks(proj, dockerOn && autoShim, cfg)...)
	if err := checkMoneyBudgetRates(f, cfg); err != nil {
		add("budget_rates", false, err.Error())
	}
	return checks, nil
}

// secretChecks verifies that every secret a used recipe references can be resolved by the existing resolver
// (project secrets.env, global secrets.env, process environment). Values are never read into the report.
func secretChecks(proj engine.DockerProjection, transported bool, cfg config.Config) []requirementCheck {
	if !transported {
		return nil
	}
	loaded, err := docker.LoadSecretLayers(cfg.Docker.SecretsFile, absRootDir())
	if err != nil {
		return []requirementCheck{{Name: "secrets", Status: checkMissing, Detail: err.Error()}}
	}
	var checks []requirementCheck
	for _, command := range slices.Sorted(maps.Keys(proj.Recipes)) {
		recipe := proj.Recipes[command]
		if recipe.Auth.From == "" {
			continue
		}
		check := requirementCheck{Name: "secret:" + command, Status: checkOK, Detail: recipe.Auth.From}
		if _, err := secrets.ResolveRef(recipe.Auth.From, loaded); err != nil {
			check.Status, check.Detail = checkMissing, err.Error()
		}
		checks = append(checks, check)
	}
	return checks
}

// missingRequirements renders the failed checks for an error message.
func missingRequirements(checks []requirementCheck) []string {
	var missing []string
	for _, c := range checks {
		if c.Status == checkMissing {
			missing = append(missing, c.Name+": "+c.Detail)
		}
	}
	return missing
}

// pipelinePreflight is everything checked before a worktree or run exists: references that must resolve, then the
// environment the package asks for.
func pipelinePreflight(f *flow.Flow, cfg config.Config, m pipeline.Manifest, continuedRunDir string) error {
	if err := checkHelperProfiles(f, cfg, continuedRunDir); err != nil {
		return err
	}
	checks, err := checkPipelineRequirements(f, cfg, m)
	if err != nil {
		return err
	}
	if missing := missingRequirements(checks); len(missing) > 0 {
		return fmt.Errorf("pipeline %s cannot start in this environment:\n  - %s", m.Name, strings.Join(missing, "\n  - "))
	}
	return nil
}
