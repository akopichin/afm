package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm/assets"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/proxy"
	"github.com/akopichin/afm/pkg/server"
	"github.com/akopichin/afm/pkg/state"
)

func newRunCmd() *cobra.Command {
	var maxParallel int
	var idleTimeout time.Duration
	var port int

	cmd := &cobra.Command{
		Use:   "run [flow.yaml]",
		Short: "Run a flow (or resume the latest run)",
		RunE: func(cmd *cobra.Command, args []string) error {
			home, _ := os.UserHomeDir()
			cfg, err := config.LoadFrom(filepath.Join(home, ".afm"), fmDir())
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

			runDir, store, err := resolveRun(f)
			if err != nil {
				return err
			}
			defer store.Close()

			fmt.Printf("afm: running %q\n", f.Name)
			fmt.Printf("  run dir: %s\n", runDir)

			var proxyAddr, proxyShimDir string
			if cfg.Proxy.IsEnabled() {
				upstream := cfg.Proxy.Upstream
				if upstream == "" {
					upstream = os.Getenv("ANTHROPIC_BASE_URL")
				}
				if upstream != "" {
					transforms := proxy.BuildTransforms(upstream, cfg.Proxy.Transforms.ZAI)
					p := proxy.New(upstream, transforms)
					addr, err := p.Start(cfg.Proxy.Port)
					if err != nil {
						return fmt.Errorf("start proxy: %w", err)
					}
					defer p.Shutdown(context.Background()) //nolint:errcheck // best-effort graceful shutdown
					proxyAddr = addr
					fmt.Printf("  proxy: %s → %s\n", addr, upstream)

					if shimDir, shimErr := proxy.CreateShim(addr); shimErr == nil {
						proxyShimDir = shimDir
						defer os.RemoveAll(shimDir) //nolint:errcheck // best-effort cleanup
					} else {
						fmt.Fprintf(os.Stderr, "warning: proxy shim: %v\n", shimErr)
					}
				} else {
					fmt.Println("  proxy: skipped (no upstream) — set proxy.upstream in config or export ANTHROPIC_BASE_URL to enable")
				}
			}

			orch := orchestrator.New(orchestrator.Options{
				RunDir:       runDir,
				Stages:       f.Stages,
				Store:        store,
				Config:       cfg,
				Prompts:      prompts,
				ProxyURL:     proxyAddr,
				ProxyShimDir: proxyShimDir,
			})

			// Disable interactive flags when dashboard is not running
			if cfg.Server.GetPort() == 0 {
				for i := range f.Stages {
					if f.Stages[i].Interactive {
						f.Stages[i].Interactive = false
						fmt.Fprintf(os.Stderr, "warning: stage %q: interactive requires dashboard (server port > 0); running as non-interactive\n", f.Stages[i].ID)
					}
				}
			}

			// Start HTTP server if port > 0
			if cfg.Server.GetPort() > 0 {
				srv := server.New(server.Config{
					Port:      cfg.Server.GetPort(),
					RunDir:    runDir,
					Store:     store,
					UIBus:     orch.UIBus(),
					ApproveFn: orch.Approve,
					ReviseFn:  orch.Revise,
					RetryFn:   orch.Retry,
					DialogAnswerFn: func(stageID, phase, qID, answer string, fromOptions bool) error {
						return orch.NotifyAnswer(stageID, phase, qID, answer, fromOptions)
					},
					DialogCancelFn: func(stageID string) error {
						orch.FailStage(stageID, "cancelled by user")
						return nil
					},
				})
				addr, err := srv.Start()
				if err != nil {
					return fmt.Errorf("start dashboard: %w", err)
				}
				defer func() { _ = srv.Shutdown(context.Background()) }()

				// Resolve listener address to localhost for client-facing URLs.
				// ln.Addr() may return [::]:port which is not reachable as a client URL.
				_, port, _ := net.SplitHostPort(addr)
				dashURL := fmt.Sprintf("http://localhost:%s", port) //nolint:revive // local dashboard is http
				orch.SetDashboardURL(dashURL)
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

			fmt.Printf("afm: flow %q completed\n", f.Name)
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
	entries, err := os.ReadDir(filepath.Join(fmDir(), "flows"))
	if err != nil {
		return "", errors.New("no flow file provided and " + fmDir() + "/flows/ not found")
	}
	var yamls []string
	for _, e := range entries {
		if !e.IsDir() && (filepath.Ext(e.Name()) == extYAML || filepath.Ext(e.Name()) == extYML) {
			yamls = append(yamls, filepath.Join(fmDir(), "flows", e.Name()))
		}
	}
	if len(yamls) == 0 {
		return "", errors.New("no flow YAML files found in " + fmDir() + "/flows/")
	}
	if len(yamls) == 1 {
		return yamls[0], nil
	}
	return "", fmt.Errorf("multiple flow files found; specify one: %v", yamls)
}

func resolveRun(f *flow.Flow) (runDir string, store *state.Store, err error) {
	base := filepath.Join(fmDir(), "runs")

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

	ts := time.Now().Format("20060102-150405")
	runDir = filepath.Join(base, f.Name+"-"+ts)
	if err = os.MkdirAll(runDir, 0755); err != nil {
		return
	}
	store, err = state.Open(runDir, stageIDs)
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
