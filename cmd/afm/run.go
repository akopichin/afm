package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm-engine/engine"
	"github.com/akopichin/afm/contracts/browser"
	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/contracts/flow"
	"github.com/akopichin/afm/contracts/lifecyclehooks"
	"github.com/akopichin/afm/contracts/worktree"
	"github.com/akopichin/afm/pkg/server"
)

// runOptions carries per-invocation choices into executeFlow.
type runOptions struct {
	requireApproval bool
	exitOnFail      bool
	launch          launchMode
	worktreeDir     string // "" = agents run in the main checkout
	forceNewRun     bool   // set by the clear flags: never resume
	runName         string // run directory prefix; f.Name when --worktree was not given
	// cycle — номер прохода с 1; метаданные repeat публикуются только при f.Repeat.Enabled. previousRunID —
	// ран предыдущего цикла. afterRun вызывается после успешного рана, до паузы dashboard, и оценивает, последует
	// ли ещё цикл: тогда dashboard держится недолго. Оценка выбирает лишь паузу; цикл стартует по свежему чтению.
	cycle         int
	previousRunID string
	afterRun      func() (moreCycles bool)
	// onRunCreated вызывается сразу после создания (или открытия) run dir, до запуска стадий.
	onRunCreated func(runID string)
	// containerBrowser — дескриптор browser из transport, прочитанный один раз за процесс (только в контейнере).
	containerBrowser *browser.Descriptor
}

func newRunCmd() *cobra.Command {
	var maxParallel int
	var idleTimeout time.Duration
	var port int
	var modeFlag string
	var requireApproval bool
	var exitOnFail bool
	var worktreeClear bool
	var worktreeClearForce bool
	var worktreeName string

	cmd := &cobra.Command{
		Use:   "run [flow.yaml]",
		Short: "Run a flow (or resume the latest run)",
		RunE: func(cmd *cobra.Command, args []string) error {
			resolvedExitOnFail, err := resolveExitOnFail(exitOnFail, cmd.Flags().Changed("exit-on-fail"), os.Getenv("AFM_EXIT_ON_FAIL"))
			if err != nil {
				return err
			}
			home, _ := os.UserHomeDir()
			cfg, err := loadProductConfig(filepath.Join(home, config.AfmDir), fmDir())
			if err != nil {
				return err
			}
			if maxParallel > 0 {
				cfg.Executor.MaxParallel = maxParallel
			}
			if idleTimeout > 0 {
				cfg.Executor.IdleTimeout = idleTimeout
			}
			if cmd.Flags().Changed("port") {
				cfg.Server.Port = &port
			}
			// Before the flow, worktree, Docker re-exec or the run itself: a refused mode must create nothing.
			launch, err := parseLaunchMode(modeFlag)
			if err != nil {
				return err
			}
			if err := launch.validate(cfg); err != nil {
				return err
			}

			flowPath, err := resolveFlowPath(args)
			if err != nil {
				return err
			}
			f, err := flow.ParseFile(flowPath)
			if err != nil {
				return fmt.Errorf("parse flow: %w", err)
			}

			if err := config.ValidateVerifySpecs(f, cfg); err != nil {
				return fmt.Errorf("verify: %w", err)
			}
			// Combine before re-exec: host and container must use identical secret indices.
			hooks := combineRunHooks(cfg, f)

			// Fail fast before anything is created — the name becomes part of a run directory even when
			// worktrees themselves end up unused in-container (resolveWorktree's ReExecedIntoContainer
			// branch never reaches worktree.PlanFor to validate it there).
			if worktreeName != "" {
				if err := worktree.ValidateName(worktreeName); err != nil {
					return err
				}
			}

			mode, err := clearModeFromFlags(worktreeClear, worktreeClearForce)
			if err != nil {
				return err
			}
			wtDir, wtLock, err := resolveWorktree(context.Background(), absRootDir(), f, cfg, mode, worktreeName)
			if err != nil {
				return err // resolveWorktree's own errors already carry a "worktree: " prefix
			}
			// Held for the entire run so a concurrent --worktree-clear always sees this flow's lock held
			// — never released right after resolveWorktree returns, which would leave the run's whole
			// lifetime unprotected. docker.ReExec spawns `docker run` as a child process and blocks on
			// it (not a process-image replacement), so this defer spans the container's whole lifetime
			// too when Docker is enabled; the re-exec'd container's own resolveWorktree call always
			// returns a nil lock (see its doc comment), so wtLock is only ever non-nil on the host.
			if wtLock != nil {
				defer wtLock.Close() //nolint:errcheck
			}
			// In Docker mode the host process re-execs into the container, which re-runs RunE and
			// reaches this point a second time (resolveWorktree's ReExecedIntoContainer branch just
			// reads the transport env) — skip the print there so it isn't shown twice.
			if wtDir != "" && !config.ReExecedIntoContainer() {
				fmt.Printf("afm: worktree %s (branch %s)\n", wtDir, runNameFor(worktreeName, f.Name))
			}

			// Skills are synced only on the host (see syncSkillsForRun), so this must run before the Docker re-exec.
			syncSkillsForRun(home, os.Stderr)

			if cfg.Docker.IsDockerEnabled() {
				saved, err := engine.SavedRunFor(mode, runsDir(), runNameFor(worktreeName, f.Name), f)
				if err != nil {
					return err
				}
				releaseOwnerTokenForDocker(os.Stdout)
				return reexecFlow(absRootDir(), f, cfg, hooks, wtDir, saved, launch)
			}

			// reasoning is never a hard error — warn on non-canonical/cursor values and proceed. Emitted
			// after the Docker re-exec branch so it prints exactly once: on a Docker run the in-container
			// afm reaches this point, not the host (which returned above).
			for _, w := range cfg.ReasoningWarnings() {
				fmt.Fprintf(os.Stderr, "warning: %s\n", w)
			}

			// Один signal-контекст на все циклы repeat и ожидание dashboard между ними.
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()
			_, err = cycleLoop{
				flowPath:        flowPath,
				cfg:             cfg,
				cliMaxParallel:  maxParallel != 0,
				launch:          launch,
				worktreeName:    worktreeName,
				worktreeDir:     wtDir,
				requireApproval: requireApproval,
				exitOnFail:      resolvedExitOnFail,
				forceNewRun:     mode != worktree.ClearNone,
			}.run(ctx, f, hooks)
			return err
		},
	}
	cmd.Flags().IntVar(&maxParallel, "max-parallel", 0, "max parallel stages (0=unlimited)")
	cmd.Flags().DurationVar(&idleTimeout, "idle-timeout", 0, "agent idle timeout")
	cmd.Flags().IntVar(&port, "port", 0, "dashboard port (0=use config)")
	cmd.Flags().StringVar(&modeFlag, "mode", "", "launch mode: dashboard (HTTP API and Web UI, default) or core (HTTP API only)")
	cmd.Flags().BoolVar(&requireApproval, "require-approval", false, "fail if a stage plan needs approval but no dashboard is running")
	cmd.Flags().BoolVar(&exitOnFail, "exit-on-fail", false, "stop the run and exit 1 when a stage fails (env: AFM_EXIT_ON_FAIL)")
	cmd.Flags().BoolVar(&worktreeClear, "worktree-clear", false, "recreate the flow worktree, discarding uncommitted changes but keeping its branch and commits; also starts a fresh run")
	cmd.Flags().BoolVar(&worktreeClearForce, "worktree-clear-force", false, "recreate the flow worktree AND its branch from HEAD, discarding the branch's commits; also starts a fresh run")
	cmd.Flags().StringVar(&worktreeName, "worktree", "", "worktree/branch name (default: flow name); implies worktrees are enabled and isolates this run under .afm/runs/<name>-…")
	return cmd
}

func resolveExitOnFail(flagValue, flagSet bool, env string) (bool, error) {
	if flagSet {
		return flagValue, nil
	}
	enabled, known := parseEnvBool(env)
	if !known {
		return false, errors.New("AFM_EXIT_ON_FAIL must be 1/true/yes/on or 0/false/no/off")
	}
	return enabled, nil
}

// runBrowser hands the engine the managed browser: in the container the manager runs on the host and the
// descriptor was rebuilt from transport env once per process (takeContainerTransport); natively we own the
// manager for the run's lifetime.
func (o runOptions) runBrowser(cfg config.Config) (*browser.Descriptor, func(), error) {
	if config.ReExecedIntoContainer() {
		return o.containerBrowser, func() {}, nil
	}
	_, desc, cleanup, err := startBrowser(cfg, browser.BrowserModeNative)
	return desc, cleanup, err
}

// enginePaths turns the process-wide roots into the explicit paths the engine works with.
func enginePaths(worktreeDir string) engine.Paths {
	exe, _ := os.Executable()
	return engine.Paths{
		AfmRoot:     absRootDir(),
		AfmDir:      absPath(fmDir()),
		RunsDir:     absPath(runsDir()),
		WorktreeDir: worktreeDir,
		Executable:  exe,
	}
}

// executeFlow runs one pass of the flow under the caller's signal context. The engine owns the run
// resources; this function only composes the transports around it, in the order the run needs:
// workspace, Open, dashboard, Start, Wait, hold, Shutdown, dashboard shutdown, Close.
func executeFlow(ctx context.Context, f *flow.Flow, cfg config.Config, hooks []lifecyclehooks.RegisteredHook, opts runOptions) (cycleResult, error) {
	humanControl := cfg.Server.GetPort() > 0
	ws := openRunWorkspace(humanControl)
	var files engine.FileResolver
	if ws != nil {
		files = engine.FileResolver{Resolve: workspaceResolveFile(ws), CurrentSHA: workspaceCurrentFileSHA(ws)}
	}
	run, err := engine.Open(ctx, engine.Spec{Flow: f, Config: cfg.Execution(), Hooks: hooks}, engine.Options{
		Paths:    enginePaths(opts.worktreeDir),
		RunName:  opts.runName,
		ForceNew: opts.forceNewRun,
		Policies: engine.Policies{
			HumanControl:       humanControl,
			RequireApproval:    opts.requireApproval,
			ExitOnFail:         opts.exitOnFail,
			AssistantAvailable: humanControl,
		},
		Cycle:        engine.Cycle{Number: opts.cycle, PreviousRunID: opts.previousRunID},
		OnRunCreated: opts.onRunCreated,
		Browser:      func() (*browser.Descriptor, func(), error) { return opts.runBrowser(cfg) },
		Files:        files,
		Debug:        debugEnabled,
		Stdout:       os.Stdout,
		Stderr:       os.Stderr,
	})
	if err != nil {
		if ws != nil {
			_ = ws.Close()
		}
		return cycleResult{}, err
	}
	// Runs last: stores are released after the dashboard, which reads them until it is shut down.
	defer func() { _ = run.Close() }()

	read := run.Read()
	collab := run.CollaborationSetup()
	api := run.Collaboration()
	srv, agentAPIURL, err := startServer(cfg, server.Config{
		RunDir:      read.RunDir(),
		Runtime:     server.RuntimeInfo{Mode: opts.launch.String(), Ready: run.Ready},
		Description: f.Description,
		// The dashboard reads stage config from the runtime registry on every call, not a map frozen
		// at startup.
		Stages:            registryStageProvider{reader: read},
		Store:             read,
		UIBus:             read.Events(),
		RedactSecrets:     read.RedactSecrets,
		ReviewState:       read.ReviewState,
		Actions:           run.Stages(),
		Secondary:         run.Stages(),
		Workspace:         ws,
		Accounting:        run.CostProvider(cfg.Accounting.IsEnabled()),
		UsageAnalytics:    run.UsageAnalytics(),
		SideAgent:         run.Assistant(),
		AssistantProfiles: profilesService(collab, api),
		CollaborationMode: run.Capabilities().Mode,
		AssistantActions:  assistantActions(collab, api),
		AssistantsReader:  assistantsReader(collab, api),
		DiscussionService: discussionService(collab, api),
		ProposalActions:   proposalActions(collab, api),
	}, runSurface{run.Mesh(), run.Graph(), run.Flow()}, opts.launch, opts.cycle <= 1)
	if err != nil {
		return cycleResult{}, err
	}
	if srv != nil {
		defer func() { _ = srv.Shutdown(context.Background()) }()
	}
	// Drains the Assistant and its bridges before the dashboard they talk to goes away.
	defer func() { _ = run.Shutdown(context.Background()) }()

	if err := run.Start(ctx, engine.Endpoints{AgentAPIURL: agentAPIURL}); err != nil {
		return cycleResult{}, err
	}
	// The run is cancelled through ctx given to Start; Wait itself must outlive that cancellation to report
	// the interrupted run's error.
	res, _ := run.Wait(context.WithoutCancel(ctx))
	if res.Err != nil {
		return cycleResult{RunID: res.RunID, Resumed: res.Resumed}, runExitError(ctx, res.Err, res.RunDir)
	}
	// Err is nil for a run with a failed stage too; success is the persisted finished state with all done.
	out := cycleResult{RunID: res.RunID, Resumed: res.Resumed, Completed: res.Completed}
	if out.Completed || !f.Repeat.Enabled {
		fmt.Printf("afm: flow %q completed\n", f.Name)
	} else {
		fmt.Printf("afm: flow %q: cycle %d did not complete successfully\n", f.Name, opts.cycle)
	}
	// The next cycle estimate is needed before the dashboard hold, to choose its length.
	moreCycles := out.Completed && opts.afterRun != nil && opts.afterRun()
	if srv != nil {
		holdAfterResult(ctx, srv, opts.launch, moreCycles)
	}
	// Normal end: wait for an active Assistant turn without the emergency cap.
	_ = run.Shutdown(ctx)
	return out, nil
}

// holdAfterResult keeps the server up after a successful run so a client can read the final state. Dashboard
// waits a grace for the UI; core has no UI to wait for, so its last hold ends only when the owner interrupts.
func holdAfterResult(ctx context.Context, srv *server.Server, launch launchMode, moreCycles bool) {
	switch {
	case moreCycles && launch == launchCore:
		fmt.Printf("  api: holding %s before the next cycle\n", interCycleDrainGrace)
		waitForDashboardGrace(ctx, interCycleDrainGrace)
	case moreCycles:
		// The next cycle serves the dashboard on the same port: no need to wait for a tab.
		fmt.Printf("  dashboard: holding %s for UI to render final state\n", interCycleDrainGrace)
		waitForDashboardGrace(ctx, interCycleDrainGrace)
	case launch == launchCore:
		fmt.Println("  api: holding the final state until interrupted (Ctrl+C)")
		<-ctx.Done()
	default:
		fmt.Printf("  dashboard: holding at least %s for UI to render final state\n", dashboardExitGraceMinimum)
		drainDashboard(ctx, srv.ConnectedClients)
	}
}

func resolveFlowPath(args []string) (string, error) {
	if len(args) > 0 {
		return args[0], nil
	}
	entries, err := os.ReadDir(flowsDir())
	if err != nil {
		return "", errors.New("no flow file provided and " + flowsDir() + "/ not found")
	}
	var yamls []string
	for _, e := range entries {
		if !e.IsDir() && (filepath.Ext(e.Name()) == extYAML || filepath.Ext(e.Name()) == extYML) {
			yamls = append(yamls, filepath.Join(flowsDir(), e.Name()))
		}
	}
	if len(yamls) == 0 {
		return "", errors.New("no flow YAML files found in " + flowsDir() + "/")
	}
	if len(yamls) == 1 {
		return yamls[0], nil
	}
	return "", fmt.Errorf("multiple flow files found; specify one: %v", yamls)
}
