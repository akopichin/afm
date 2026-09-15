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

	"github.com/spf13/cobra"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/state"
)

func newReportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "report [run]",
		Short: "Render a markdown cost/usage summary for a run",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// `afm report` is a pure cost/usage command. With the accounting
			// display switched off (accounting.enabled: false / AFM_ACCOUNTING=0)
			// it emits nothing to stdout (so a redirected report stays empty) and
			// a one-line hint on stderr, exiting 0. usage.jsonl is still being
			// collected, so `AFM_ACCOUNTING=1 afm report` shows it on demand.
			if !accountingDisplayEnabled() {
				fmt.Fprintln(os.Stderr, "accounting display disabled; set AFM_ACCOUNTING=1 or accounting.enabled: true to view")
				return nil
			}

			var runArg string
			if len(args) > 0 {
				runArg = args[0]
			}
			runDir, err := resolveReportRunDir(runsDir(), runArg)
			if err != nil {
				return err
			}

			rs, err := state.LoadRunState(runDir)
			if err != nil {
				return fmt.Errorf("load state: %w", err)
			}

			// Read-only: a run without usage.jsonl at all (accounting
			// disabled, or a run predating this feature) must still produce
			// a valid report — accounting.Load returns an empty ledger with
			// a nil error for that case. A CORRUPT or otherwise-unreadable
			// ledger renders a self-contained degraded report (see the
			// matrix in RenderMarkdown's RenderState) and exits 3.
			led, loadErr := accounting.Load(runDir)

			var renderState accounting.RenderState
			switch {
			case loadErr == nil:
				renderState = accounting.RenderNormal
			case errors.Is(loadErr, accounting.ErrCorruptUsage):
				renderState = accounting.RenderCorrupt
			default:
				renderState = accounting.RenderUnreadable
			}

			durations := stageDurations(runDir)
			stages := make(map[string]accounting.StageInfo, len(rs.Stages))
			for id, s := range rs.Stages {
				stages[id] = accounting.StageInfo{
					Status:   string(s.Status),
					Duration: formatDuration(durations[id]),
				}
			}

			fmt.Print(accounting.RenderMarkdown(filepath.Base(runDir), stages, led, renderState))

			switch {
			case loadErr == nil:
				return nil
			case errors.Is(loadErr, accounting.ErrCorruptUsage):
				fmt.Fprintln(os.Stderr, "cost unavailable (ledger corrupt)")
				return &ExitError{Code: 3, Silent: true}
			default:
				fmt.Fprintf(os.Stderr, "cost unavailable (cannot read usage ledger): %v\n", loadErr)
				return &ExitError{Code: 3, Silent: true}
			}
		},
	}
}

// resolveReportRunDir resolves the `afm report [run]` positional argument to
// a run directory. Read-only, no flock — this must work while a live `afm
// run` holds the run's lock, same guarantee as `afm check`. Empty means the
// latest run under base (the same pick-the-lexicographically-last-directory
// rule `afm check` uses: run directory names are `<flow>-<timestamp>-<rand>`,
// so sorting equals chronological order). A non-empty arg is tried first as
// a filesystem path (relative or absolute) and, failing that, as a bare run
// id directly under base.
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

// stageDurations best-effort-derives each stage's wall-clock span (its
// earliest to latest recorded transition time) directly from events.jsonl —
// read-only, no flock, tolerant of a missing or corrupt file (a report with
// no duration column is still a valid report; state.LoadRunState already
// applies the same read-only, torn-tail-tolerant treatment for status, this
// just needs Transition.Time per stage, which LoadRunState does not expose).
func stageDurations(runDir string) map[string]time.Duration {
	data, err := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		return nil
	}

	type span struct{ first, last time.Time }
	spans := make(map[string]span)
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var t state.Transition
		if json.Unmarshal(line, &t) != nil {
			continue // torn tail or corrupt line: skip, duration is best-effort
		}
		sp, ok := spans[t.StageID]
		if !ok {
			spans[t.StageID] = span{first: t.Time, last: t.Time}
			continue
		}
		if t.Time.Before(sp.first) {
			sp.first = t.Time
		}
		if t.Time.After(sp.last) {
			sp.last = t.Time
		}
		spans[t.StageID] = sp
	}

	out := make(map[string]time.Duration, len(spans))
	for id, sp := range spans {
		out[id] = sp.last.Sub(sp.first)
	}
	return out
}

// formatDuration renders a stage's derived span, or "" for a non-positive
// duration (a stage with only one transition recorded has no span to show).
// accounting.StageInfo treats an empty Duration as "omit the parens"
// rather than rendering a misleading "0s".
func formatDuration(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	return d.Round(time.Second).String()
}
