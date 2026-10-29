package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/akopichin/afm/pkg/browser"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/docker"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/lifecyclehooks"
)

type runEnvironment struct {
	RootDir string
	// SkillsBase is where a relative skills_dir is resolved: the effective project root, else the afm root.
	// Unlike RootDir it is never empty, because it does not decide where agents run.
	SkillsBase      string
	MemoryDir       string
	WrapperDir      string
	GeneratedAgents map[string]bool
}

func (e runEnvironment) Close() {
	if e.WrapperDir != "" {
		_ = os.RemoveAll(e.WrapperDir)
	}
}

func prepareRunEnvironment(afmRoot string, f *flow.Flow, cfg config.Config, worktreeDir string) (runEnvironment, error) {
	var env runEnvironment
	// Absolute: the root, memory and wrapper paths reach agents whose CWD may be a worktree.
	afmRoot, err := filepath.Abs(afmRoot)
	if err != nil {
		return env, err
	}
	if worktreeDir != "" {
		worktreeDir, err = filepath.Abs(worktreeDir)
		if err != nil {
			return env, err
		}
	}
	if worktreeDir != "" {
		env.RootDir = worktreeDir // the worktree IS the effective project root
	} else {
		env.RootDir, err = resolveAgentRoot(afmRoot, f)
		if err != nil {
			return env, err
		}
	}
	env.SkillsBase, err = effectiveRoot(afmRoot, worktreeDir, f)
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
	// Helper-profile commands count too: a recipe used only by a helper (reviewer → codex) still needs its
	// wrapper here, matching the host side's projection in reexecFlow.
	proj, err := projectDocker(f, cfg)
	if err != nil {
		return env, err
	}
	env.GeneratedAgents = proj.Generated
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

// saved is the run this start would resume (nil for a new run): its frozen skill libraries need mounts too.
func reexecFlow(afmRoot string, f *flow.Flow, cfg config.Config, hooks []lifecyclehooks.RegisteredHook, worktreeDir string, saved *savedRun) error {
	absDir, err := filepath.Abs(afmRoot)
	if err != nil {
		return fmt.Errorf("resolve project dir: %w", err)
	}
	if err := docker.CheckClaudeDockerAuth(cfg.Client.Command); err != nil {
		return err
	}
	proj, err := projectDocker(f, cfg)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}
	// Checked here, before docker -v could create a missing source as an empty root-owned directory.
	skillMounts, skillLibraries, err := prepareSkillMounts(absDir, worktreeDir, home, f, cfg, saved)
	if err != nil {
		return err
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

	// Managed browser capability: start the host-side manager NOW (it must be
	// serving while `docker run` blocks) and forward the transport to the container.
	_, browserDesc, browserCleanup, berr := startBrowser(cfg, browser.BrowserModeDocker)
	if berr != nil {
		return berr
	}
	defer browserCleanup()

	// T8.5: assistant (helper-agent) profile commands are pre-prepared for Docker alongside flow-stage
	// commands — a profile configured after this scan has no mount and stays unavailable until the next run.
	rc := docker.ReExecConfig{
		Image:         cfg.Docker.GetImage(),
		ProjectDir:    absDir,
		Commands:      docker.ScanCommandsWithExtra(f, cfg.Client.Command, proj.Generated, proj.Extra),
		DashboardPort: port,
		ExtraMounts:   slices.Concat(cfg.Docker.ExtraMounts, skillMounts),
		// The absolute, final --dir wins over a user's relative flag inside Docker.
		ExtraArgs:          append(os.Args[1:], "--dir="+absDir),
		ClientCommand:      cfg.Client.Command,
		Recipes:            proj.Recipes,
		SecretsFile:        cfg.Docker.SecretsFile,
		Hooks:              hooks,
		MountCodexState:    proj.MountCodexState,
		FileBrowserEnabled: browserEnabled,
		FileRoots:          fileRoots,
		WorktreeDir:        worktreeDir,
		SkillLibraries:     skillLibraries,
	}
	if browserDesc != nil {
		rc.BrowserEnabled = true
		rc.BrowserToken = browserDesc.Token
		rc.BrowserEndpoint = browserDesc.Endpoint
		rc.BrowserEpoch = browserDesc.Epoch
		rc.BrowserAppURL = browserDesc.AppURL
		rc.BrowserAppContainerPort = cfg.Browser.GetAppPort()
		rc.BrowserAppHostPort = portFromURL(browserDesc.AppURL)
	}
	return docker.ReExec(rc)
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
		Reasoning:    recipe.Reasoning,
	}
}
