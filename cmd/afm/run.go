package main

import (
	"context"
	"errors"
	"fmt"
	"net"
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
	"github.com/akopichin/afm/pkg/mcpserver"
	"github.com/akopichin/afm/pkg/pipeline"
	"github.com/akopichin/afm/pkg/runregistry"
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
	flowPath     string // файл флоу, в который «Save to flow.yaml» записывает лимит; "" — сохранение недоступно
	// pipeline — binding рана к снимку пакета; nil без --pipeline.
	pipeline *pipelineBinder
	// containerBrowser — дескриптор browser из transport, прочитанный один раз за процесс (только в контейнере).
	containerBrowser *browser.Descriptor
	mcp              mcpSettings
	// task дописывается к prompt флоу и сохраняется в каталоге каждого рана; пусто — ран берёт задачу из своего
	// task.md, если он продолжает прежний.
	task string
	// instanceID отличает этот процесс по /api/runtime; registrar — его запись в реестре (nil без неё).
	instanceID string
	registrar  *runRegistrar
	// listener — заранее открытый сокет API для первого цикла (--listen-any-port); остальные циклы слушают порт сами.
	listener net.Listener
}

// runDeps are the steps of `afm run` a test replaces to stop the command at a known point.
type runDeps struct {
	preparePipeline func(ctx context.Context, pf pipelineFlags, base config.Config, worktreeName string, clearWorktree, clearForce, forceNew bool) (*pipelinePlan, error)
}

func newRunCmd() *cobra.Command {
	return newRunCmdWith(runDeps{preparePipeline: preparePipelineRun})
}

func newRunCmdWith(deps runDeps) *cobra.Command {
	var maxParallel int
	var idleTimeout time.Duration
	var port int
	var modeFlag string
	var requireApproval bool
	var exitOnFail bool
	var worktreeClear bool
	var worktreeClearForce bool
	var worktreeName string
	var noWorktree bool
	var mcpFlag bool
	var mcpOff bool
	var mcpAllow string
	var taskFlag string
	var instanceIDFlag string
	var launchID string
	var listenAnyPort bool
	var pf pipelineFlags

	cmd := &cobra.Command{
		Use:   "run [flow.yaml]",
		Short: "Run a flow (or resume the latest run)",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Before any config, cache or run is touched: a contradictory command line must leave nothing behind.
			if err := pf.validate(args); err != nil {
				return err
			}
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
			if listenAnyPort && cmd.Flags().Changed("port") {
				return errors.New("--listen-any-port cannot be combined with --port")
			}
			instanceID := instanceIDFlag
			if instanceID == "" {
				instanceID = runregistry.NewInstanceID()
			} else if !runregistry.ValidInstanceID(instanceID) {
				return errors.New("--instance-id must be 32 lowercase hex characters")
			}
			if launchID != "" {
				// A host-started run has nobody at the terminal: a tab would open on every launch.
				noBrowser := false
				cfg.Server.OpenBrowser = &noBrowser
			}
			// Opened once the command line is accepted: the port is known before the run is prepared and
			// reaches the API server as is.
			var apiListener net.Listener
			if listenAnyPort {
				ln, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					return fmt.Errorf("--listen-any-port: %w", err)
				}
				defer ln.Close() //nolint:errcheck
				tcpAddr, ok := ln.Addr().(*net.TCPAddr)
				if !ok {
					return fmt.Errorf("--listen-any-port: unexpected address %v", ln.Addr())
				}
				realPort := tcpAddr.Port
				cfg.Server.Port = &realPort
				apiListener = ln
			}
			// Before the flow, worktree, Docker re-exec or the run itself: a refused mode must create nothing.
			launch, err := parseLaunchMode(modeFlag)
			if err != nil {
				return err
			}
			if err := launch.validate(cfg); err != nil {
				return err
			}
			mcp, err := parseMCPSettings(mcpFlags{
				on:       mcpFlag,
				off:      mcpOff,
				allowSet: cmd.Flags().Changed("mcp-allow"),
				allow:    mcpAllow,
			}, cfg)
			if err != nil {
				return err
			}
			// Before the flow, worktree or Docker re-exec: a refused task must create nothing.
			var task string
			if cmd.Flags().Changed("task") {
				if task, err = loadTask(commandContext(cmd), taskFlag, cmd.InOrStdin()); err != nil {
					return err
				}
			}

			// Before the pipeline or worktree is prepared, so a process that dies there is still discoverable.
			var registrar *runRegistrar
			if cfg.Server.GetPort() > 0 && !config.ReExecedIntoContainer() {
				registrar = openRegistrar(launch, cfg, mcp, instanceID, launchID)
				defer registrar.Close()
			}

			// Before the pipeline or flow is read: a contradictory request must create nothing.
			if noWorktree && worktreeName != "" {
				return fmt.Errorf("--no-worktree and --worktree cannot be used together")
			}

			var f *flow.Flow
			var flowPath string
			var plan *pipelinePlan
			if pf.active() {
				// The package's flow and agents are assembled in memory; no config file is read for them or written.
				if plan, err = deps.preparePipeline(commandContext(cmd), pf, cfg, worktreeName, worktreeClear, worktreeClearForce, task != ""); err != nil {
					return err
				}
				f, cfg = plan.setup.loaded.Flow, plan.setup.cfg
				announcePipeline(plan)
			} else {
				if flowPath, err = resolveFlowPath(args); err != nil {
					return err
				}
				if f, err = flow.ParseFile(flowPath); err != nil {
					return fmt.Errorf("parse flow: %w", err)
				}
			}

			if noWorktree {
				disableWorktrees(f)
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
			registrar.SetProject(runNameFor(worktreeName, f.Name), wtDir)
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
				// A task starts a new run, so there is nothing saved to continue.
				var saved *engine.SavedRun
				if task == "" {
					if saved, err = engine.SavedRunFor(mode, runsDir(), runNameFor(worktreeName, f.Name), f); err != nil {
						return err
					}
				}
				releaseOwnerTokenForDocker(os.Stdout)
				var launchArgs []string
				if plan != nil {
					launchPath, err := writePipelineLaunch(plan)
					if err != nil {
						return err
					}
					defer os.Remove(launchPath) //nolint:errcheck
					launchArgs = []string{"--pipeline-launch=" + launchPath}
				}
				if task != "" {
					// The container reads the task from a file: the text must not appear in docker's argv.
					taskPath, err := writeTaskSnapshot(absRootDir(), task)
					if err != nil {
						return err
					}
					defer os.Remove(taskPath) //nolint:errcheck
					launchArgs = append(launchArgs, "--task=@"+taskPath)
				}
				launchArgs = append(launchArgs, "--instance-id="+instanceID)
				containerStarted := func() bool { return false }
				if port := cfg.Server.GetPort(); port > 0 {
					// Docker publishes the port itself: release it, and keep the container on the same one.
					if apiListener != nil {
						_ = apiListener.Close()
					}
					launchArgs = append(launchArgs, fmt.Sprintf("--port=%d", port), "--listen-any-port=false")
					containerStarted = watchContainer(registrar, port)
				}
				return reexecFlow(absRootDir(), f, cfg, hooks, wtDir, saved, launch, containerStarted, launchArgs...)
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
			loop := cycleLoop{
				flowPath:        flowPath,
				cfg:             cfg,
				cliMaxParallel:  maxParallel != 0,
				launch:          launch,
				worktreeName:    worktreeName,
				noWorktree:      noWorktree,
				worktreeDir:     wtDir,
				requireApproval: requireApproval,
				exitOnFail:      resolvedExitOnFail,
				forceNewRun:     mode != worktree.ClearNone || task != "",
				mcp:             mcp,
				task:            task,
				instanceID:      instanceID,
				registrar:       registrar,
				listener:        apiListener,
			}
			if plan != nil {
				loop.forceNewRun = loop.forceNewRun || plan.forceNew
				loop.binder = newPipelineBinder(plan.setup, plan.runName, worktreeName, plan.resume)
				loop.loadFlow = func() (*flow.Flow, error) {
					return pipeline.LoadFlow(plan.setup.loaded.Snapshot, plan.setup.loaded.Manifest)
				}
			}
			_, err = loop.run(ctx, f, hooks)
			return err
		},
	}
	cmd.Flags().IntVar(&maxParallel, "max-parallel", 0, "max parallel stages (0=unlimited)")
	cmd.Flags().DurationVar(&idleTimeout, "idle-timeout", 0, "agent idle timeout")
	cmd.Flags().IntVar(&port, "port", 0, "dashboard port (0=use config)")
	cmd.Flags().StringVar(&modeFlag, "mode", "", "launch mode: dashboard (HTTP API and Web UI, default) or core (HTTP API only)")
	cmd.Flags().BoolVar(&mcpFlag, "mcp", false, "serve MCP on /mcp of the HTTP API (default whenever the API runs); needs a port")
	cmd.Flags().BoolVar(&mcpOff, "mcp-off", false, "do not serve MCP")
	cmd.Flags().StringVar(&mcpAllow, "mcp-allow", "", "MCP permissions, comma-separated: reply,stages,approve,budget, or read for read-only (default: all groups)")
	cmd.Flags().StringVar(&instanceIDFlag, "instance-id", "", "internal: process id a host expects in /api/runtime")
	cmd.Flags().StringVar(&launchID, "launch-id", "", "internal: launch id of a host-started run")
	cmd.Flags().BoolVar(&listenAnyPort, "listen-any-port", false, "internal: serve the HTTP API on a free loopback port")
	_ = cmd.Flags().MarkHidden("instance-id")
	_ = cmd.Flags().MarkHidden("launch-id")
	_ = cmd.Flags().MarkHidden("listen-any-port")
	cmd.Flags().StringVar(&taskFlag, "task", "", "initial task appended to the flow prompt: text, @file, @- (stdin) or an http(s) URL; starts a new run")
	cmd.Flags().BoolVar(&requireApproval, "require-approval", false, "fail if a stage plan needs approval but no dashboard is running")
	cmd.Flags().BoolVar(&exitOnFail, "exit-on-fail", false, "stop the run and exit 1 when a stage fails (env: AFM_EXIT_ON_FAIL)")
	cmd.Flags().BoolVar(&worktreeClear, "worktree-clear", false, "recreate the flow worktree, discarding uncommitted changes but keeping its branch and commits; also starts a fresh run")
	cmd.Flags().BoolVar(&worktreeClearForce, "worktree-clear-force", false, "recreate the flow worktree AND its branch from HEAD, discarding the branch's commits; also starts a fresh run")
	cmd.Flags().StringVar(&pf.source, "pipeline", "", "run a pipeline package from a local directory or Git (<repo>[//<subdir>][@<ref>]) instead of a flow file")
	cmd.Flags().StringVar(&pf.options, "pipeline-options", "", "options file (docker.agents and collaboration.profiles) layered over the pipeline package; requires --pipeline")
	cmd.Flags().StringVar(&pf.launch, "pipeline-launch", "", "internal: launch record handed to the container")
	_ = cmd.Flags().MarkHidden("pipeline-launch")
	cmd.Flags().BoolVar(&noWorktree, "no-worktree", false, "run in the checkout itself even if the flow or config enables worktrees; cannot be combined with --worktree")
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
	// Before the workspace, so a bad declaration leaves nothing to close.
	loops, err := declaredAssistantLoops(f.Stages)
	if err != nil {
		return cycleResult{}, err
	}
	humanControl := cfg.Server.GetPort() > 0
	ws := openRunWorkspace(humanControl)
	var files engine.FileResolver
	if ws != nil {
		files = engine.FileResolver{Resolve: workspaceResolveFile(ws), CurrentSHA: workspaceCurrentFileSHA(ws)}
	}
	// The task joins the prompt of this pass only: the engine reads Prompt after OnRunCreated, and the caller's
	// flow stays as the user wrote it.
	passFlow := *f
	var task string
	// The binder cannot fail the engine's callback; its error is acted on right after Open, before anything starts.
	var runCreatedErr error
	onRunCreated := func(runID string) {
		if opts.pipeline != nil {
			runCreatedErr = opts.pipeline.runCreated(runID, opts.cycle)
		}
		var err error
		if task, err = prepareRunTask(filepath.Join(absPath(runsDir()), runID), opts.task); err != nil && runCreatedErr == nil {
			runCreatedErr = err
		}
		passFlow.Prompt = promptWithTask(f.Prompt, task)
		opts.registrar.SetRun(runID)
		if opts.onRunCreated != nil {
			opts.onRunCreated(runID)
		}
	}
	engineOpts := engine.Options{
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
		OnRunCreated: onRunCreated,
		Browser:      func() (*browser.Descriptor, func(), error) { return opts.runBrowser(cfg) },
		Files:        files,
		Debug:        debugEnabled,
		Stdout:       os.Stdout,
		Stderr:       os.Stderr,
	}
	// Assigned only when set: a nil *budgetExporter in the interface would look like a configured exporter.
	exporter := budgetExporterFor(opts.flowPath, f)
	if exporter != nil {
		engineOpts.BudgetExporter = exporter
	}
	run, err := engine.Open(ctx, engine.Spec{Flow: &passFlow, Config: cfg.Execution(), Hooks: hooks, AssistantLoops: loops}, engineOpts)
	if err != nil {
		if ws != nil {
			_ = ws.Close()
		}
		return cycleResult{}, err
	}
	// Runs last: stores are released after the dashboard, which reads them until it is shut down.
	defer func() { _ = run.Close() }()
	if runCreatedErr != nil {
		if ws != nil {
			_ = ws.Close()
		}
		return cycleResult{}, runCreatedErr
	}

	read := run.Read()
	collab := run.CollaborationSetup()
	api := run.Collaboration()
	serverCfg := server.Config{
		RunDir:      read.RunDir(),
		Runtime:     server.RuntimeInfo{Mode: opts.launch.String(), InstanceID: opts.instanceID, Ready: run.Ready},
		Description: f.Description,
		// The dashboard reads stage config from the runtime registry on every call, not a map frozen
		// at startup.
		Stages:            registryStageProvider{reader: read, profiles: profileRegistry(collab)},
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
		Budgets:           run.Budgets(),
	}
	if exporter != nil {
		serverCfg.BudgetExport = exporter
	}
	if run.Capabilities().ConditionalApproval {
		serverCfg.Approval = run.Stages()
	}
	// Assigned only when enabled: a nil *Service in the interface would look like a mounted MCP endpoint.
	if opts.mcp.enabled {
		svc, err := mcpserver.New(mcpserver.Config{
			RunDir:        serverCfg.RunDir,
			Reader:        read,
			Events:        read.Events(),
			Stages:        serverCfg.Stages,
			Commands:      run.Stages(),
			Approval:      serverCfg.Approval,
			Budgets:       run.Budgets(),
			Cost:          serverCfg.Accounting,
			ShowMoney:     cfg.Accounting.ShowMoneyEnabled(),
			RedactSecrets: read.RedactSecrets,
			Groups:        opts.mcp.groups,
			Version:       version,
		})
		if err != nil {
			return cycleResult{}, fmt.Errorf("mcp: %w", err)
		}
		serverCfg.MCP = svc
	}
	srv, agentAPIURL, err := startServerOn(cfg, serverCfg, runSurface{run.Mesh(), run.Graph(), run.Flow()}, opts.launch, opts.cycle <= 1, opts.listener)
	if err != nil {
		return cycleResult{}, err
	}
	if srv != nil {
		defer func() { _ = srv.Shutdown(context.Background()) }()
		opts.registrar.SetEndpoint(loopbackURL(agentAPIURL))
	}
	// Drains the Assistant and its bridges before the dashboard they talk to goes away.
	defer func() { _ = run.Shutdown(context.Background()) }()

	if err := run.Start(ctx, engine.Endpoints{AgentAPIURL: agentAPIURL}); err != nil {
		return cycleResult{}, err
	}
	opts.registrar.SetReady(true)
	// The run is cancelled through ctx given to Start; Wait itself must outlive that cancellation to report
	// the interrupted run's error.
	res, _ := run.Wait(context.WithoutCancel(ctx))
	if res.Err != nil {
		return cycleResult{RunID: res.RunID, Resumed: res.Resumed}, runExitError(ctx, res.Err, res.RunDir)
	}
	// Err is nil for a run with a failed stage too; success is the persisted finished state with all done.
	out := cycleResult{RunID: res.RunID, Resumed: res.Resumed, Completed: res.Completed, Budget: res.Budget, Task: task}
	if res.Budget != nil && res.Budget.Held {
		return out, budgetHeldExit(ctx, run, opts.flowPath, opts.pipeline != nil)
	}
	if out.Completed || !f.Repeat.Enabled {
		fmt.Printf("afm: flow %q completed\n", f.Name)
	} else {
		fmt.Printf("afm: flow %q: cycle %d did not complete successfully\n", f.Name, opts.cycle)
	}
	// The next cycle estimate is needed before the dashboard hold, to choose its length.
	moreCycles := out.Completed && res.Budget == nil && opts.afterRun != nil && opts.afterRun()
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

// budgetExporterFor is nil without a flow file, which leaves saving to the flow unavailable. A typed nil must
// not reach the engine's interface.
func budgetExporterFor(flowPath string, f *flow.Flow) *budgetExporter {
	if flowPath == "" {
		return nil
	}
	return newBudgetExporter(flowPath, absRootDir(), f)
}

// budgetHeldExit reports the scopes that keep a run without a dashboard from finishing. Nobody can raise a limit
// there, so the process ends with the hold code; a restarted run reads the flow again and continues by itself.
func budgetHeldExit(ctx context.Context, run *engine.Run, flowPath string, packaged bool) error {
	views, err := run.Budgets().List(context.WithoutCancel(ctx))
	if err != nil {
		fmt.Fprintf(os.Stderr, "afm: cannot list the held budgets: %v\n", err)
	}
	fmt.Println("afm: the run is held by a budget limit")
	for _, v := range views {
		if v.Hold != nil {
			fmt.Printf("  %s: %s · %s\n", budgetLabel(v), budgetFigures(v), budgetState(v))
		}
	}
	if packaged {
		// The flow lives in an immutable package snapshot: there is no file to edit here.
		fmt.Println("afm: the limit is part of the pipeline package; run with a dashboard port to raise it and continue")
		return &ExitError{Code: budgetHeldExitCode, Silent: true}
	}
	file := "the flow file"
	if flowPath != "" {
		file = relativeToRoot(absRootDir(), flowPath)
	}
	fmt.Printf("afm: raise the limit in %s; a restarted run continues by itself, or run with a dashboard port to edit and continue\n", file)
	return &ExitError{Code: budgetHeldExitCode, Silent: true}
}
