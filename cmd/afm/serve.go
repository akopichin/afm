package main

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm/pkg/serve"
)

func newServeCmd() *cobra.Command {
	var port int
	var mcpAllow, configPath string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run a persistent local MCP host that finds, starts and controls many afm runs",
		Long: "Run a persistent host on 127.0.0.1 that serves MCP on /mcp for every afm run on this machine: it lists " +
			"the runs afm processes registered, starts new ones from the projects and pipelines in ~/.afm/serve.yaml, " +
			"attaches runs started elsewhere, and forwards the tools of a single run's MCP endpoint under a run_id.\n\n" +
			"The host owns only its state in ~/.afm/serve: stopping it (SIGINT, SIGTERM) leaves every run running, and " +
			"a restarted host finds them again. Only one host runs per user.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			groups := serve.AllHostGroups()
			if cmd.Flags().Changed("mcp-allow") {
				var err error
				if groups, err = serve.ParseHostGroups(mcpAllow); err != nil {
					return fmt.Errorf("--mcp-allow: %w", err)
				}
			}
			cfg, err := serve.LoadConfig(serveConfigPath(configPath))
			if err != nil {
				return err
			}
			binary, err := os.Executable()
			if err != nil {
				return fmt.Errorf("find the afm executable: %w", err)
			}
			app, err := serve.Open(serve.Options{
				Config: cfg, Binary: binary, Env: os.Environ(), Groups: groups, Port: port, Version: version, Log: os.Stderr,
			})
			var locked *serve.LockedError
			if errors.As(err, &locked) {
				fmt.Fprintf(os.Stderr, "afm serve: %v\n", locked)
				return &ExitError{Code: 1, Silent: true}
			}
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(commandContext(cmd), os.Interrupt, syscall.SIGTERM)
			defer stop()
			fmt.Fprintf(os.Stderr, "afm serve: MCP endpoint %s\n", app.Endpoint())
			return app.Serve(ctx)
		},
	}
	cmd.Flags().IntVar(&port, "port", serve.DefaultPort, "loopback port of the MCP endpoint (0 picks a free one)")
	cmd.Flags().StringVar(&mcpAllow, "mcp-allow", "", "permissions of the host's MCP, comma-separated: reply,stages,approve,budget,runs, or read for read-only (default: all)")
	cmd.Flags().StringVar(&configPath, "config", "", "serve config file (default: ~/.afm/serve.yaml)")
	return cmd
}

// serveConfigPath is the --config value, or serve.yaml next to the user's other global afm files.
func serveConfigPath(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".afm", "serve.yaml")
}
