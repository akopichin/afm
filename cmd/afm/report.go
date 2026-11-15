package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/akopichin/afm/pkg/runreport"
	"github.com/akopichin/afm/pkg/usageview"
	"github.com/spf13/cobra"
)

const (
	reportFormatMarkdown = "markdown"
	reportFormatJSON     = "json"
)

// reportNow is the report's clock; tests replace it to get a stable as_of.
var reportNow = time.Now

func newReportCmd() *cobra.Command {
	var format, stageID string
	cmd := &cobra.Command{
		Use:   "report [run]",
		Short: "Report usage, cost and agent time of a run (markdown or json)",
		Long: "Report usage, cost, stage time and agent jobs of a run from its files alone: nothing is written and no " +
			"lock is taken, so it is safe on a running run (the figures are a snapshot).\n\n" +
			"What is shown follows accounting.enabled and accounting.show_money of the afm root (--dir > AFM_DIR > cwd). " +
			"Exit code 3 means the report was printed but the usage ledger or the agent-jobs journal is unusable.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != reportFormatMarkdown && format != reportFormatJSON {
				return fmt.Errorf("unknown --format %q (want %s or %s)", format, reportFormatMarkdown, reportFormatJSON)
			}
			var runArg string
			if len(args) > 0 {
				runArg = args[0]
			}
			runDir, err := resolveReportRunDir(runsDir(), runArg)
			if err != nil {
				return err
			}
			src, err := runreport.Load(runDir, runreport.LoadOptions{Now: reportNow})
			if err != nil {
				return err
			}

			enabled := accountingDisplayEnabled()
			if !enabled {
				fmt.Fprintln(os.Stderr, "accounting display disabled; set AFM_ACCOUNTING=1 or accounting.enabled: true to view tokens and cost")
			}
			mode := usageview.ModeFor(enabled, accountingShowMoney())
			report, err := runreport.Build(src, runreport.Options{Mode: mode, StageID: stageID})
			if err != nil {
				return err
			}

			// Rendered into a buffer so a failure never leaves half a document on stdout.
			var out bytes.Buffer
			if format == reportFormatJSON {
				enc := json.NewEncoder(&out)
				enc.SetEscapeHTML(false)
				enc.SetIndent("", "  ")
				if err := enc.Encode(report); err != nil {
					return fmt.Errorf("encode report: %w", err)
				}
			} else {
				renderReportMarkdown(&out, report)
			}
			if _, err := out.WriteTo(os.Stdout); err != nil {
				return err
			}

			if reason := runreport.Failure(src, mode); reason != "" {
				fmt.Fprintln(os.Stderr, reason)
				return &ExitError{Code: 3, Silent: true}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", reportFormatMarkdown, "output format: markdown or json")
	cmd.Flags().StringVar(&stageID, "stage", "", "limit the report to one stage by its exact ID")
	return cmd
}

// resolveReportRunDir resolves the `afm report [run]` positional argument to a run directory.
func resolveReportRunDir(base, arg string) (string, error) {
	if arg == "" {
		return latestRunDir(base)
	}
	if info, err := os.Stat(arg); err == nil && info.IsDir() {
		return arg, nil
	}
	candidate := filepath.Join(base, arg)
	if info, err := os.Stat(candidate); err == nil && info.IsDir() {
		return candidate, nil
	}
	return "", fmt.Errorf("run %q not found (tried as a path and as a run id under %s)", arg, base)
}

// latestRunDir returns the lexicographically-last run directory under base,
// mirroring the resolution `afm check` (cmd/afm/check.go) uses.
func latestRunDir(base string) (string, error) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return "", fmt.Errorf("no runs found in %s", base)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(base, e.Name()))
		}
	}
	if len(dirs) == 0 {
		return "", errors.New("no runs found")
	}
	slices.Sort(dirs)
	return dirs[len(dirs)-1], nil
}
