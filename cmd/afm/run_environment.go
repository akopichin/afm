package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/akopichin/afm-engine/engine"
	"github.com/akopichin/afm/contracts/browser"
	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/contracts/docker"
	"github.com/akopichin/afm/contracts/flow"
	"github.com/akopichin/afm/contracts/lifecyclehooks"
)

// opensHostBrowser — core has no page, so a tab would only point at a 404.
func opensHostBrowser(cfg config.Config, launch launchMode) bool {
	return launch == launchDashboard && cfg.Server.GetPort() > 0 && cfg.Server.IsOpenBrowser()
}

// saved is the run this start would resume (nil for a new run): its frozen skill libraries need mounts too, and its
// unfinished helper tasks their commands.
func reexecFlow(afmRoot string, f *flow.Flow, cfg config.Config, hooks []lifecyclehooks.RegisteredHook, worktreeDir string, saved *engine.SavedRun, launch launchMode) error {
	absDir, err := filepath.Abs(afmRoot)
	if err != nil {
		return fmt.Errorf("resolve project dir: %w", err)
	}
	if err := docker.CheckClaudeDockerAuth(cfg.Client.Command); err != nil {
		return err
	}
	proj, err := engine.ProjectDocker(f, cfg, engine.ResumeTaskCommands(saved, f, cfg))
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}
	// Checked here, before docker -v could create a missing source as an empty root-owned directory.
	skillMounts, skillLibraries, err := engine.PrepareSkillMounts(absDir, worktreeDir, home, f, cfg, saved)
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
	if opensHostBrowser(cfg, launch) {
		hostBrowserOpener(port)
	}

	// Managed browser capability: start the host-side manager NOW (it must be
	// serving while `docker run` blocks) and forward the transport to the container.
	_, browserDesc, browserCleanup, berr := startBrowser(cfg, browser.BrowserModeDocker)
	if berr != nil {
		return berr
	}
	defer browserCleanup()

	// Helper profile and saved task commands are prepared for Docker alongside the stage commands: a profile
	// configured after this scan has no mount and stays unavailable until the next run.
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
