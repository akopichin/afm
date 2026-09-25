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
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/lifecyclehooks"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/server"
	"github.com/akopichin/afm/pkg/state"
)

func newRunCmd() *cobra.Command {
	var maxParallel int
	var idleTimeout time.Duration
	var port int
	var requireApproval bool

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

			// Recipe-based verification requires the read-only wrapper, both before
			// Docker re-exec and inside the container that generates it.
			shimmed := cfg.Docker.IsAutoShim() && (config.ReExecedIntoContainer() || cfg.Docker.IsDockerEnabled())
			if err := config.ValidateVerifySpecs(f, cfg, shimmed); err != nil {
				return fmt.Errorf("verify: %w", err)
			}
			// Combine before re-exec: host and container must use identical secret indices.
			hooks := combineRunHooks(cfg, f)
			if cfg.Docker.IsDockerEnabled() {
				return reexecFlow(rootDir, f, cfg, hooks)
			}

			// CLI > flow YAML > config.
			if maxParallel == 0 && f.MaxParallel > 0 {
				cfg.Executor.MaxParallel = f.MaxParallel
			}
			return executeFlow(f, cfg, hooks, requireApproval)
		},
	}
	cmd.Flags().IntVar(&maxParallel, "max-parallel", 0, "max parallel stages (0=unlimited)")
	cmd.Flags().DurationVar(&idleTimeout, "idle-timeout", 0, "agent idle timeout")
	cmd.Flags().IntVar(&port, "port", 0, "dashboard port (0=use config)")
	cmd.Flags().BoolVar(&requireApproval, "require-approval", false, "fail if a stage plan needs approval but no dashboard is running")
	return cmd
}

// executeFlow owns run resources. Each resource is released on every return path.
func executeFlow(f *flow.Flow, cfg config.Config, hooks []lifecyclehooks.RegisteredHook, requireApproval bool) error {
	prompts, err := loadPrompts(cfg.PromptsDir)
	if err != nil {
		return err
	}
	runDir, store, err := resolveRun(f)
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

	env, err := prepareRunEnvironment(rootDir, f, cfg)
	if err != nil {
		return err
	}
	defer env.Close()
	stages := normalizeInteractiveStages(f.Stages, cfg.Server.GetPort() > 0)
	runID := filepath.Base(runDir)
	resumed := store.Snapshot().LastSeq > 0

	var orch *orchestrator.Orchestrator
	hooksDisp, err := prepareLifecycleDispatcher(rootDir, lifecyclehooks.DispatcherConfig{
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
		RunDir:          runDir,
		Stages:          stages,
		Store:           store,
		Config:          cfg,
		Prompts:         prompts,
		WrapperDir:      env.WrapperDir,
		GeneratedAgents: env.GeneratedAgents,
		GlobalPrompt:    f.Prompt,
		RootDir:         env.RootDir,
		RequireApproval: requireApproval,
		Debug:           debugEnabled,
		Memory:          f.Memory,
		MemoryDir:       env.MemoryDir,
		Accounting:      acct,
		FlowName:        f.Name,
		RunID:           runID,
		Resumed:         resumed,
		Hooks:           hooksDisp,
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

	srv, err := startDashboard(cfg, server.Config{
		RunDir:      runDir,
		Description: f.Description,
		Stages:      dashboardStageConfig(stages),
		Store:       store,
		Workspace:   ws,
		Accounting:  serverAccountingProvider(cfg.Accounting.IsEnabled(), acct, acctErr),
	}, orch)
	if err != nil {
		return err
	}
	if srv != nil {
		defer func() { _ = srv.Shutdown(context.Background()) }()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := orch.Run(ctx); err != nil {
		return fmt.Errorf("run: %w", err)
	}
	fmt.Printf("afm: flow %q completed\n", f.Name)
	if srv != nil {
		fmt.Printf("  dashboard: holding at least %s for UI to render final state\n", dashboardExitGraceMinimum)
		waitForDashboardDrain(ctx, srv.ConnectedClients)
	}
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

func resolveRun(f *flow.Flow) (runDir string, store *state.Store, err error) {
	base := runsDir()

	stageIDs := make([]string, len(f.Stages))
	for i, s := range f.Stages {
		stageIDs[i] = s.ID
	}

	existing, lookErr := state.FindLatestRunDir(base, f.Name)
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

	runDir = filepath.Join(base, newRunID(f.Name))
	if err = os.MkdirAll(runDir, 0755); err != nil {
		return
	}
	store, err = state.Open(runDir, stageIDs)
	return
}

// newRunID строит уникальный id run: timestamp секундной гранулярности плюс
// короткий случайный суффикс, чтобы два запуска в одну секунду не делили
// одну директорию и один events.jsonl.
func newRunID(flowName string) string {
	ts := time.Now().Format("20060102-150405")
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-%s-%s", flowName, ts, hex.EncodeToString(b))
}

func loadPrompts(overrideDir string) (orchestrator.Prompts, error) {
	names := []string{"planning.md", "implementation.md", "review.md", "summary.md", "autonomous.md",
		"reflect.md", "aggregate.md", "prioritize.md", "update.md", "verify.md"}
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
	}, nil
}
