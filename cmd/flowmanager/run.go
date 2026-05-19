package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"gitlab.ae-rus.net/bx/ai-flow-manager/assets"
	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/config"
	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/flow"
	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/orchestrator"
	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/server"
	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/state"
)

func newRunCmd() *cobra.Command {
	var maxParallel int
	var idleTimeout time.Duration
	var port int

	cmd := &cobra.Command{
		Use:   "run [flow.yaml]",
		Short: "Run a flow (or resume the latest run)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
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

			// Apply flow-level overrides (CLI flag takes priority, then YAML, then config)
			if maxParallel == 0 && f.MaxParallel > 0 {
				cfg.Executor.MaxParallel = f.MaxParallel
			}

			prompts, err := loadPrompts(cfg.PromptsDir)
			if err != nil {
				return err
			}

			runDir, rs, stateFile, err := resolveRun(f)
			if err != nil {
				return err
			}

			fmt.Printf("flowmanager: running %q\n", f.Name)
			fmt.Printf("  run dir: %s\n", runDir)

			orch := orchestrator.New(orchestrator.Options{
				RunDir:    runDir,
				Stages:    f.Stages,
				State:     rs,
				StateFile: stateFile,
				Config:    cfg,
				Prompts:   prompts,
			})

			// Start HTTP server if port > 0
			if cfg.Server.GetPort() > 0 {
				srv := server.New(server.Config{
					Port:      cfg.Server.GetPort(),
					RunDir:    runDir,
					StateFile: stateFile,
					Bus:       orch.Bus(),
					ApproveFn: orch.Approve,
					ReviseFn:  orch.Revise,
					RetryFn:   orch.Retry,
				})
				addr, err := srv.Start()
				if err != nil {
					return fmt.Errorf("start dashboard: %w", err)
				}
				defer func() { _ = srv.Shutdown(context.Background()) }()

				dashURL := fmt.Sprintf("http://%s", addr) //nolint:revive // local dashboard is http
				fmt.Printf("  dashboard: %s\n", dashURL)
				if cfg.Server.IsOpenBrowser() {
					openBrowser(dashURL)
				}
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()

			if err := orch.Run(ctx); err != nil {
				return fmt.Errorf("run: %w", err)
			}

			fmt.Printf("flowmanager: flow %q completed\n", f.Name)
			return nil
		},
	}

	cmd.Flags().IntVar(&maxParallel, "max-parallel", 0, "max parallel stages (0=unlimited)")
	cmd.Flags().DurationVar(&idleTimeout, "idle-timeout", 0, "agent idle timeout")
	cmd.Flags().IntVar(&port, "port", 0, "dashboard port (0=use config)")
	return cmd
}

func openBrowser(url string) {
	var cmd string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "linux":
		cmd = "xdg-open"
	default:
		return
	}
	//nolint:gosec // opening a local URL in the browser is safe
	_ = exec.Command(cmd, url).Start()
}

const extYAML = ".yaml"
const extYML = ".yml"

func resolveFlowPath(args []string) (string, error) {
	if len(args) > 0 {
		return args[0], nil
	}
	entries, err := os.ReadDir(filepath.Join(".flowManager", "flows"))
	if err != nil {
		return "", errors.New("no flow file provided and .flowManager/flows/ not found")
	}
	var yamls []string
	for _, e := range entries {
		if !e.IsDir() && (filepath.Ext(e.Name()) == extYAML || filepath.Ext(e.Name()) == extYML) {
			yamls = append(yamls, filepath.Join(".flowManager", "flows", e.Name()))
		}
	}
	if len(yamls) == 0 {
		return "", errors.New("no flow YAML files found in .flowManager/flows/")
	}
	if len(yamls) == 1 {
		return yamls[0], nil
	}
	return "", fmt.Errorf("multiple flow files found; specify one: %v", yamls)
}

func resolveRun(f *flow.Flow) (runDir string, rs *state.RunState, stateFile string, err error) {
	base := filepath.Join(".flowManager", "runs")

	existing, lookErr := state.FindLatestRunDir(f.Name)
	if lookErr == nil {
		stateFile = filepath.Join(existing, "state.json")
		rs, err = state.Load(stateFile)
		if err == nil && !rs.AllDone() {
			fmt.Printf("flowmanager: resuming run %s\n", filepath.Base(existing))
			return existing, rs, stateFile, nil
		}
	}

	ts := time.Now().Format("20060102-150405")
	runDir = filepath.Join(base, f.Name+"-"+ts)
	if err = os.MkdirAll(runDir, 0755); err != nil {
		return
	}
	stageIDs := make([]string, len(f.Stages))
	for i, s := range f.Stages {
		stageIDs[i] = s.ID
	}
	rs = state.NewRunState(stageIDs)
	rs.FlowName = f.Name
	stateFile = filepath.Join(runDir, "state.json")
	err = rs.Save(stateFile)
	return
}

func loadPrompts(overrideDir string) (orchestrator.Prompts, error) {
	names := []string{"planning.md", "implementation.md", "review.md", "summary.md"}
	texts := make([]string, len(names))
	for i, name := range names {
		text, err := assets.ReadPrompt(name, overrideDir)
		if err != nil {
			return orchestrator.Prompts{}, fmt.Errorf("read prompt %s: %w", name, err)
		}
		texts[i] = text
	}
	return orchestrator.Prompts{
		Planning:       texts[0],
		Implementation: texts[1],
		Review:         texts[2],
		Summary:        texts[3],
	}, nil
}
