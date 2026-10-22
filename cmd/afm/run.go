package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm/assets"
	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/browser"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/lifecyclehooks"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/server"
	"github.com/akopichin/afm/pkg/state"
	"github.com/akopichin/afm/pkg/worktree"
)

// runOptions carries per-invocation choices into executeFlow.
type runOptions struct {
	requireApproval bool
	worktreeDir     string // "" = agents run in the main checkout
	forceNewRun     bool   // set by the clear flags: never resume
	runName         string // run directory prefix; f.Name when --worktree was not given
}

func newRunCmd() *cobra.Command {
	var maxParallel int
	var idleTimeout time.Duration
	var port int
	var requireApproval bool
	var worktreeClear bool
	var worktreeClearForce bool
	var worktreeName string

	cmd := &cobra.Command{
		Use:   "run [flow.yaml]",
		Short: "Run a flow (or resume the latest run)",
		RunE: func(cmd *cobra.Command, args []string) error {
			home, _ := os.UserHomeDir()
			cfg, err := config.LoadFrom(filepath.Join(home, config.AfmDir), fmDir())
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

			if cfg.Docker.IsDockerEnabled() {
				return reexecFlow(absRootDir(), f, cfg, hooks, wtDir)
			}

			// reasoning is never a hard error — warn on non-canonical/cursor values and proceed. Emitted
			// after the Docker re-exec branch so it prints exactly once: on a Docker run the in-container
			// afm reaches this point, not the host (which returned above).
			for _, w := range cfg.ReasoningWarnings() {
				fmt.Fprintf(os.Stderr, "warning: %s\n", w)
			}

			// CLI > flow YAML > config.
			if maxParallel == 0 && f.MaxParallel > 0 {
				cfg.Executor.MaxParallel = f.MaxParallel
			}
			return executeFlow(f, cfg, hooks, runOptions{
				requireApproval: requireApproval,
				worktreeDir:     wtDir,
				forceNewRun:     mode != worktree.ClearNone,
				runName:         runNameFor(worktreeName, f.Name),
			})
		},
	}
	cmd.Flags().IntVar(&maxParallel, "max-parallel", 0, "max parallel stages (0=unlimited)")
	cmd.Flags().DurationVar(&idleTimeout, "idle-timeout", 0, "agent idle timeout")
	cmd.Flags().IntVar(&port, "port", 0, "dashboard port (0=use config)")
	cmd.Flags().BoolVar(&requireApproval, "require-approval", false, "fail if a stage plan needs approval but no dashboard is running")
	cmd.Flags().BoolVar(&worktreeClear, "worktree-clear", false, "recreate the flow worktree, discarding uncommitted changes but keeping its branch and commits; also starts a fresh run")
	cmd.Flags().BoolVar(&worktreeClearForce, "worktree-clear-force", false, "recreate the flow worktree AND its branch from HEAD, discarding the branch's commits; also starts a fresh run")
	cmd.Flags().StringVar(&worktreeName, "worktree", "", "worktree/branch name (default: flow name); implies worktrees are enabled and isolates this run under .afm/runs/<name>-…")
	return cmd
}

// executeFlow owns run resources. Each resource is released on every return path.
func executeFlow(f *flow.Flow, cfg config.Config, hooks []lifecyclehooks.RegisteredHook, opts runOptions) error {
	prompts, err := loadPrompts(cfg.PromptsDir)
	if err != nil {
		return err
	}
	runDir, store, err := resolveRun(f, opts.runName, opts.forceNewRun)
	if err != nil {
		return err
	}
	defer store.Close()

	// Collection remains enabled even when cost display is disabled.
	acct, acctErr := accounting.Open(runDir, accounting.NewResolver(cfg.Pricing))
	if acctErr != nil {
		fmt.Fprintf(os.Stderr, "warning: accounting: open usage ledger: %v\n", acctErr)
	}
	if acct != nil {
		defer acct.Close()
	}

	store.SetFlowName(f.Name)
	stageNames := make(map[string]string, len(f.Stages))
	for _, s := range f.Stages {
		if s.Name != "" {
			stageNames[s.ID] = s.Name
		}
	}
	store.SetStageNames(stageNames)
	fmt.Printf("afm: running %q\n", f.Name)
	fmt.Printf("  run dir: %s\n", runDir)

	env, err := prepareRunEnvironment(rootDir, f, cfg, opts.worktreeDir)
	if err != nil {
		return err
	}
	defer env.Close()

	// Managed browser capability. In the container the manager runs on the host — we only rebuild the
	// descriptor from transport env; natively we own the manager here for the run's lifetime.
	var browserDesc *browser.Descriptor
	if config.ReExecedIntoContainer() {
		browserDesc = descriptorFromEnv(true)
		// Strip consumed AFM_BROWSER_* transport from afm's env so even direct child processes cannot inherit
		// the token.
		browser.UnsetTransportVars()
	} else {
		_, desc, browserCleanup, berr := startBrowser(cfg, browser.BrowserModeNative)
		if berr != nil {
			return berr
		}
		defer browserCleanup()
		browserDesc = desc
	}

	stages := normalizeInteractiveStages(f.Stages, cfg.Server.GetPort() > 0)
	// T8.6 recovery ordering: the stage registry itself must be settled — restored from this run's own
	// durable graph_registry.json on resume, or persisted fresh on a first start — BEFORE any consumer
	// (orchestrator.New, the dashboard's stage provider) is constructed below. A flow.yaml edited between
	// runs must never silently replace a resumed run's graph (graph.RestoreOrPersist's whole point).
	stages, err = graph.RestoreOrPersist(runDir, stages)
	if err != nil {
		return fmt.Errorf("restore stage registry: %w", err)
	}
	runID := filepath.Base(runDir)
	runSnap := store.Snapshot()
	resumed := runSnap.LastSeq > 0
	// A fresh run is durably marked with the typed board task protocol before any agent starts; a resumed
	// run without the marker stays legacy.
	if !resumed {
		if err := store.RecordBoardTaskProtocol(agentcomm.BoardTaskProtocolTypedV1, state.Actor{Kind: "system", ID: "afm"}); err != nil {
			return fmt.Errorf("record board task protocol: %w", err)
		}
	}
	// Этап 9, T9.5: fold every stage_graph_added record committed before this process started (or crashed)
	// back on top of the frozen stage set — orchestrator.New must see a dynamically-added stage from its very
	// first Stages() read, the same way it already sees every stage flow.yaml originally declared. The
	// original flow.yaml file on disk is never touched by this; graph_registry.json (restored above) stays the
	// frozen baseline forever, only the IN-MEMORY stage list handed to the orchestrator grows.
	if len(runSnap.DynamicStages) > 0 {
		stages = graph.Materialize(stages, runSnap.DynamicStages, runSnap.StageOrder)
	}

	// T2.7 recovery ordering: collaboration state (task tree/quotas/continuations restored by state.Open's
	// replay above, policy resolved and durably recorded here) must be settled BEFORE any consumer —
	// orchestrator.New below — is created. A flow.yaml change after this run's policy was first recorded
	// must not silently retake effect on resume without going through its own durable record (T2.6).
	collab := startCollaboration(store, cfg, f)
	if err := checkDeclaredAssistants(f, collab); err != nil {
		return err
	}
	if err := checkFlowTeam(store, f.Team, collab); err != nil {
		return err
	}

	var orch *orchestrator.Orchestrator
	hooksDisp, err := prepareLifecycleDispatcher(absRootDir(), lifecyclehooks.DispatcherConfig{
		FlowName: f.Name,
		RunID:    runID,
		RunDir:   runDir,
		RootDir:  lifecycleRootDir(env.RootDir),
		Resumed:  resumed,
	}, hooks, func(hookID, eventID string, err error) {
		orch.PublishLifecycleHookFailure(hookID, eventID, err)
	})
	if err != nil {
		return err
	}
	defer stopLifecycleDispatcher(hooksDisp)

	ws := openRunWorkspace(cfg.Server.GetPort() > 0)
	orchOpts := orchestrator.Options{
		RunDir:             runDir,
		Stages:             stages,
		Store:              store,
		Config:             cfg,
		Prompts:            prompts,
		WrapperDir:         env.WrapperDir,
		GeneratedAgents:    env.GeneratedAgents,
		GlobalPrompt:       f.Prompt,
		RootDir:            env.RootDir,
		RequireApproval:    opts.requireApproval,
		Debug:              debugEnabled,
		Memory:             f.Memory,
		MemoryDir:          env.MemoryDir,
		Accounting:         acct,
		FlowName:           f.Name,
		RunID:              runID,
		Resumed:            resumed,
		Hooks:              hooksDisp,
		BrowserDescriptor:  browserDesc,
		AssistantsEnabled:  collab.policy.Enabled,
		AssistantsRegistry: collab.registry,
		Team:               f.Team,
		SecretValues:       loadRedactionSecrets(absRootDir(), cfg, hooks),
	}
	if ws != nil {
		orchOpts.ResolveFile = workspaceResolveFile(ws)
		orchOpts.CurrentFileSHA = workspaceCurrentFileSHA(ws)
	}
	orch = orchestrator.New(orchOpts)
	// Workers may report errors only after the orchestrator is available.
	if hooksDisp != nil {
		hooksDisp.Start()
	}

	// Создаём процессный ctx до бокового агента и дашборда: Ctrl+C отменяет флоу и ожидание завершения
	// бокового хода.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// Боковой агент дашборда поднимается, когда поднят дашборд И не выключен переключателем. Жёсткий сбой
	// открытия журнала не валит ран — просто отключает боковой агент.
	var side *sideAgentRuntime
	var sideDrained bool
	if cfg.Server.GetPort() > 0 && sideAgentEnabled(cfg, f) {
		side, err = startSideAgent(runID, cfg, env, collab.registry, orch)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: side agent disabled: %v\n", err)
		}
	}
	// Журнал/учёт закрываются ПОСЛЕ остановки сервера и только если агент дренирован. Регистрируем ДО defer
	// srv.Shutdown — LIFO выполнит closeStores уже после srv.Shutdown.
	defer func() { side.closeStores(sideDrained) }()

	srv, err := startDashboard(cfg, server.Config{
		RunDir:      runDir,
		Description: f.Description,
		// T8.3: the dashboard reads stage config through the orchestrator's runtime registry on every call,
		// not a map frozen here at startup (registryStageProvider, cmd/afm/run_dashboard.go).
		Stages:            registryStageProvider{orch: orch},
		Store:             store,
		Workspace:         ws,
		Accounting:        serverAccountingProvider(cfg.Accounting.IsEnabled(), acct, acctErr),
		SideAgent:         side.service(),
		AssistantProfiles: collab.service(),
		AssistantActions:  collab.actions(orch),
		AssistantsReader:  collab.reader(orch),
		DiscussionService: collab.discussionService(orch),
		ProposalActions:   collab.proposalActions(orch),
	}, orch)
	if err != nil {
		sideDrained = side.shutdownEmergency(sideAgentEmergencyDrain)
		return err
	}
	var unavailableSideMesh *sideAgentMeshBridge
	if side != nil {
		side.mesh = startSideAgentMesh(orch, side.manager)
		// Flow finalization drains Mesh input. Direct modal admission remains open
		// while the dashboard stays up after the flow has finalized.
		orch.SetSideAgentDrain(side.drainMesh)
	} else {
		// A resumed run can retain a durable side identity even when the
		// capability is disabled or its journal failed to open. Reconcile its
		// historical inbox without creating a Manager or making it reachable.
		var listErr error
		unavailableSideMesh, listErr = startHistoricalSideAgentMesh(ctx, orch)
		if listErr != nil {
			return listErr
		}
		if unavailableSideMesh != nil {
			orch.SetSideAgentDrain(func(ctx context.Context) bool {
				unavailableSideMesh.CloseInput()
				return unavailableSideMesh.Drain(ctx)
			})
		}
	}
	defer func() {
		if unavailableSideMesh != nil {
			unavailableSideMesh.Abort()
			ctx, cancel := context.WithTimeout(context.Background(), sideAgentEmergencyDrain)
			defer cancel()
			_ = unavailableSideMesh.Drain(ctx)
		}
	}()
	if srv != nil {
		defer func() { _ = srv.Shutdown(context.Background()) }()
	}

	runErr := orch.Run(ctx)
	if runErr != nil {
		// Аварийный выход (Ctrl+C / сбой): прерываем активный боковой ход и ждём
		// его лишь ограниченное время; журнал закрываем только если дренирован.
		sideDrained = side.shutdownEmergency(sideAgentEmergencyDrain)
		return runExitError(ctx, runErr)
	}
	fmt.Printf("afm: flow %q completed\n", f.Name)
	if srv != nil {
		fmt.Printf("  dashboard: holding at least %s for UI to render final state\n", dashboardExitGraceMinimum)
		waitForDashboardDrain(ctx, srv.ConnectedClients)
	}
	// Штатное завершение: ждём активный боковой ход без 2-минутного колпака.
	sideDrained = side.shutdownAfterFlow(ctx, sideAgentEmergencyDrain)
	return nil
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

// resolveRun opens the latest resumable run under runName (the flow name by default, or an explicit
// --worktree override — see runNameFor), unless forceNew is set — the clear flags imply a fresh worktree,
// which has no business resuming a run that may reference the previous checkout's state.
func resolveRun(f *flow.Flow, runName string, forceNew bool) (runDir string, store *state.Store, err error) {
	// Absolute: run paths reach agents (prompts, AFM_SCRATCH_DIR, report links) whose CWD may be a worktree.
	base, err := filepath.Abs(runsDir())
	if err != nil {
		return "", nil, err
	}

	stageIDs := make([]string, len(f.Stages))
	for i, s := range f.Stages {
		stageIDs[i] = s.ID
	}

	if !forceNew {
		existing, lookErr := state.FindLatestRunDir(base, runName)
		if lookErr == nil {
			store, err = state.Open(existing, stageIDs)
			if err == nil {
				snap := store.Snapshot()
				if !snap.AllDone() {
					fmt.Printf("afm: resuming run %s\n", filepath.Base(existing))
					return existing, store, nil
				}
			} else {
				fmt.Fprintf(os.Stderr, "warning: failed to open existing run %s: %v; starting new run\n", filepath.Base(existing), err)
			}
			if store != nil {
				store.Close()
			}
		}
	}

	runDir = filepath.Join(base, newRunID(runName))
	if err = os.MkdirAll(runDir, 0755); err != nil {
		return
	}
	store, err = state.Open(runDir, stageIDs)
	return
}

// newRunID строит уникальный id run: timestamp секундной гранулярности плюс короткий случайный суффикс,
// чтобы два запуска в одну секунду не делили одну директорию и один events.jsonl.
func newRunID(runName string) string {
	ts := time.Now().Format("20060102-150405")
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-%s-%s", runName, ts, hex.EncodeToString(b))
}

func loadPrompts(overrideDir string) (orchestrator.Prompts, error) {
	names := []string{"planning.md", "implementation.md", "review.md", "summary.md", "autonomous.md",
		"reflect.md", "aggregate.md", "prioritize.md", "update.md", "verify.md",
		"helper-result.md", "helper-delegate.md"}
	texts := make([]string, len(names))
	var custom, embedded []string
	for i, name := range names {
		text, fromOverride, err := assets.ReadPrompt(name, overrideDir)
		if err != nil {
			return orchestrator.Prompts{}, fmt.Errorf("read prompt %s: %w", name, err)
		}
		texts[i] = text
		if fromOverride {
			custom = append(custom, name)
		} else {
			embedded = append(embedded, name)
		}
	}
	// Кастомная prompts_dir может быть неполной — сообщаем, что взято из неё,
	// а что подхвачено из вкомпиленных дефолтов (вместо падения на нехватке).
	if overrideDir != "" {
		log.Printf("prompts: from %s: %v; embedded defaults: %v", overrideDir, custom, embedded)
	}
	return orchestrator.Prompts{
		Planning:       texts[0],
		Implementation: texts[1],
		Review:         texts[2],
		Summary:        texts[3],
		Autonomous:     texts[4],
		Reflect:        texts[5],
		Aggregate:      texts[6],
		Prioritize:     texts[7],
		Update:         texts[8],
		Verify:         texts[9],
		HelperResult:   texts[10],
		HelperDelegate: texts[11],
	}, nil
}
