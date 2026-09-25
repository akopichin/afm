package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/docker"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/lifecyclehooks"
)

type runEnvironment struct {
	RootDir         string
	MemoryDir       string
	WrapperDir      string
	GeneratedAgents map[string]bool
}

func (e runEnvironment) Close() {
	if e.WrapperDir != "" {
		_ = os.RemoveAll(e.WrapperDir)
	}
}

func prepareRunEnvironment(afmRoot string, f *flow.Flow, cfg config.Config) (runEnvironment, error) {
	var env runEnvironment
	var err error
	env.RootDir, err = resolveAgentRoot(afmRoot, f)
	if err != nil {
		return env, err
	}
	env.MemoryDir, err = resolveMemoryDir(afmRoot, env.RootDir, f)
	if err != nil {
		return env, err
	}

	// Generated wrappers require secrets transported by our own Docker re-exec.
	if !config.ReExecedIntoContainer() || !cfg.Docker.IsAutoShim() {
		return env, nil
	}
	if err := cfg.Docker.ValidateAgents(); err != nil {
		return env, err
	}
	env.GeneratedAgents = docker.UsedRecipeCommands(f, cfg.Client.Command, cfg.Docker.Agents)
	specs := make([]docker.WrapperSpec, 0, len(env.GeneratedAgents))
	for cmd := range env.GeneratedAgents {
		specs = append(specs, buildWrapperSpec(cmd, cfg.Docker.Agents[cmd], cfg.Client.IsClaudeBare()))
	}
	if len(specs) > 0 {
		env.WrapperDir, err = docker.CreateWrappers(specs)
		if err != nil {
			return env, fmt.Errorf("create wrappers: %w", err)
		}
	}
	return env, nil
}

// Normalize before constructing consumers, without mutating the parsed flow.
func normalizeInteractiveStages(stages []flow.Stage, dashboardEnabled bool) []flow.Stage {
	stages = slices.Clone(stages)
	if !dashboardEnabled {
		for i := range stages {
			if stages[i].Interactive {
				stages[i].Interactive = false
				fmt.Fprintf(os.Stderr, "warning: stage %q: interactive requires dashboard (server port > 0); running as non-interactive\n", stages[i].ID)
			}
		}
	}
	return stages
}

func reexecFlow(afmRoot string, f *flow.Flow, cfg config.Config, hooks []lifecyclehooks.RegisteredHook) error {
	absDir, err := filepath.Abs(afmRoot)
	if err != nil {
		return fmt.Errorf("resolve project dir: %w", err)
	}
	if err := docker.CheckClaudeDockerAuth(cfg.Client.Command); err != nil {
		return err
	}
	var recipes map[string]config.AgentRecipe
	var generatedForMount map[string]bool
	if cfg.Docker.IsAutoShim() {
		if err := cfg.Docker.ValidateAgents(); err != nil {
			return err
		}
		// Mount discovery and secret resolution use the same set of used recipes.
		recipes = docker.UsedRecipes(f, cfg.Client.Command, cfg.Docker.Agents)
		generatedForMount = make(map[string]bool, len(recipes))
		for cmd := range recipes {
			generatedForMount[cmd] = true
		}
	}
	browserEnabled := cfg.Docker.FileBrowser.IsEnabled()
	var fileRoots docker.FileRootManifest
	if browserEnabled {
		fileRoots, err = docker.BuildFileRootManifest(absDir, cfg.Docker.ExtraMounts)
		if err != nil {
			return fmt.Errorf("build file root manifest: %w", err)
		}
	}
	port := cfg.Server.GetPort()
	// A separate host process survives re-exec and opens the container dashboard.
	if port > 0 && cfg.Server.IsOpenBrowser() {
		launchHostBrowserOpener(port)
	}
	return docker.ReExec(docker.ReExecConfig{
		Image:         cfg.Docker.GetImage(),
		ProjectDir:    absDir,
		Commands:      docker.ScanCommands(f, cfg.Client.Command, generatedForMount),
		DashboardPort: port,
		ExtraMounts:   cfg.Docker.ExtraMounts,
		// The absolute, final --dir wins over a user's relative flag inside Docker.
		ExtraArgs:          append(os.Args[1:], "--dir="+absDir),
		ClientCommand:      cfg.Client.Command,
		Recipes:            recipes,
		SecretsFile:        cfg.Docker.SecretsFile,
		Hooks:              hooks,
		MountCodexState:    docker.UsesCodex(f, cfg.Client.Command, recipes),
		FileBrowserEnabled: browserEnabled,
		FileRoots:          fileRoots,
	})
}

// buildWrapperSpec translates a used agent recipe into its generated wrapper.
func buildWrapperSpec(cmd string, recipe config.AgentRecipe, bare bool) docker.WrapperSpec {
	return docker.WrapperSpec{
		Type:         recipe.Type,
		Command:      cmd,
		AuthTo:       recipe.Auth.EnvVarName(),
		BaseURL:      recipe.URL,
		Model:        recipe.Model,
		HasSysPrompt: recipe.SystemPrompt != "",
		Bare:         bare,
		MaxTurns:     recipe.MaxTurns,
	}
}
