package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorBlue   = "\033[34m"
	colorCyan   = "\033[36m"
	colorGray   = "\033[90m"
)

func statusColor(s state.StageStatus) string {
	switch s {
	case state.StatusDone:
		return colorGreen
	case state.StatusFailed:
		return colorRed
	case state.StatusAwaitingApproval, state.StatusPaused:
		return colorYellow
	case state.StatusRunning, state.StatusPlanning:
		return colorBlue
	case state.StatusRevising:
		return colorCyan
	default:
		return colorGray
	}
}

func newCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Show status of the latest flow run",
		RunE: func(cmd *cobra.Command, args []string) error {
			base := runsDir()
			entries, err := os.ReadDir(base)
			if err != nil {
				return fmt.Errorf("no runs found in %s", base)
			}

			var dirs []string
			for _, e := range entries {
				if e.IsDir() {
					dirs = append(dirs, filepath.Join(base, e.Name()))
				}
			}
			if len(dirs) == 0 {
				return errors.New("no runs found")
			}
			slices.Sort(dirs)
			latest := dirs[len(dirs)-1]

			rs, err := state.LoadRunState(latest)
			if err != nil {
				return fmt.Errorf("load state: %w", err)
			}

			// Read-only: a run with no usage.jsonl at all (accounting
			// disabled, or a run predating this feature) must never fail
			// `check` — Load returns an empty ledger, nil error for that
			// case. A CORRUPT or otherwise-unreadable ledger is different:
			// the stage table still renders (best-effort), but the command
			// now surfaces the problem on stderr and exits 3 instead of
			// silently reading as "no usage data" (see the matrix below).
			//
			// display=false (accounting.enabled: false / AFM_ACCOUNTING=0)
			// hides cost entirely: the ledger is not even read, so the table
			// falls back to its pre-accounting columns and no cost/coverage
			// line is printed. Collection into usage.jsonl is unaffected.
			display := accountingDisplayEnabled()
			var (
				led        *accounting.Ledger
				loadErr    error
				byStage    map[string]accounting.Summary
				runSummary accounting.Summary
			)
			hasUsage := false
			if display {
				led, loadErr = accounting.Load(latest)
				if loadErr == nil {
					byStage = led.SummaryByStage()
					runSummary = led.RunSummary()
					hasUsage = runSummary.Metered+runSummary.Unmetered > 0
				}
			}

			fmt.Printf("Run: %s\n\n", filepath.Base(latest))
			if hasUsage {
				fmt.Printf("%-20s  %-22s  %-10s  %-8s  %-20s  %-10s  %s\n",
					"STAGE", "STATUS", "UPDATED", "TOKENS", "CACHE", "EST. COST", "LAST ACTION")
				fmt.Printf("%-20s  %-22s  %-10s  %-8s  %-20s  %-10s  %s\n",
					"-----", "------", "-------", "------", "-----", "---------", "-----------")
			} else {
				fmt.Printf("%-20s  %-22s  %-10s  %s\n", "STAGE", "STATUS", "UPDATED", "LAST ACTION")
				fmt.Printf("%-20s  %-22s  %-10s  %s\n", "-----", "------", "-------", "-----------")
			}

			type row struct{ id, status, updated, lastAction, tokens, cache, cost string }
			var rows []row
			for id, s := range rs.Stages {
				action := lastLogAction(filepath.Join(latest, id))
				r := row{
					id:         id,
					status:     string(s.Status),
					updated:    s.UpdatedAt.Format("15:04:05"),
					lastAction: action,
				}
				if hasUsage {
					sum := byStage[id]
					r.tokens = humanizeTokens(sum.Tokens.Total())
					r.cache = formatCache(sum.Tokens)
					r.cost = accounting.DisplayCost(sum)
				}
				rows = append(rows, r)
			}
			slices.SortFunc(rows, func(a, b row) int {
				if a.id < b.id {
					return -1
				}
				if a.id > b.id {
					return 1
				}
				return 0
			})
			for _, r := range rows {
				color := statusColor(state.StageStatus(r.status))
				if hasUsage {
					fmt.Printf("%-20s  %s%-22s%s  %-10s  %-8s  %-20s  %-10s  %s\n",
						r.id, color, r.status, colorReset, r.updated, r.tokens, r.cache, r.cost, r.lastAction)
				} else {
					fmt.Printf("%-20s  %s%-22s%s  %-10s  %s\n",
						r.id, color, r.status, colorReset, r.updated, r.lastAction)
				}
			}

			fmt.Println()
			switch {
			case loadErr != nil && errors.Is(loadErr, accounting.ErrCorruptUsage):
				fmt.Fprintln(os.Stderr, "cost unavailable (ledger corrupt)")
				return &ExitError{Code: 3, Silent: true}
			case loadErr != nil:
				fmt.Fprintf(os.Stderr, "cost unavailable (cannot read usage ledger): %v\n", loadErr)
				return &ExitError{Code: 3, Silent: true}
			case !hasUsage:
				// Distinguish "accounting display off" (say nothing — the
				// data may well exist on disk) from "display on but this run
				// recorded no usage" (the informative note).
				if display {
					fmt.Println("No usage data")
				}
				return nil
			}
			fmt.Printf("TOTAL: %s tokens, %s (incl. run overhead)\n",
				humanizeTokens(runSummary.Tokens.Total()), accounting.DisplayCost(runSummary))
			if note := coverageNote(led); note != "" {
				fmt.Printf("  %s\n", note)
			}
			return nil
		},
	}
}

// humanizeTokens renders a token count compactly for the fixed-width table,
// e.g. 84900 -> "84.9k". Values under 1000 are shown as an exact integer; all
// arithmetic (sums, ratios) stays in pkg/accounting — this only formats.
func humanizeTokens(n uint64) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

// formatCache renders the cache read/write split, e.g. "R 47.6k / W 18.6k".
// "—" when the stage recorded no cache activity at all, rather than a noisy
// "R 0 / W 0" for every non-caching stage.
func formatCache(t accounting.Tokens) string {
	read, write := t.CacheRead, t.CacheWriteTotal()
	if read == 0 && write == 0 {
		return "—"
	}
	return fmt.Sprintf("R %s / W %s", humanizeTokens(read), humanizeTokens(write))
}

// coverageNote reports partial pricing coverage (e.g. "2 unmetered, 1
// unpriced") so a reader knows the TOTAL may understate the real cost. Empty
// when every metered record in the run was priced.
//
// Totals are sum(issue.Count) over led.CoverageIssues() — the same grouping
// `afm report`'s coverage appendix and the dashboard's cost tab use — NOT
// len(issues): two identical unpriced invocations group into a single issue
// with Count==2, and must still read as "2 unpriced", never "1".
func coverageNote(led *accounting.Ledger) string {
	var unmetered, unpriced int
	for _, issue := range led.CoverageIssues() {
		switch issue.Kind {
		case accounting.IssueKindUnmetered:
			unmetered += issue.Count
		case accounting.IssueKindUnpriced:
			unpriced += issue.Count
		default:
			// CoverageIssue only ever carries these two kinds.
		}
	}
	var parts []string
	if unmetered > 0 {
		parts = append(parts, fmt.Sprintf("%d unmetered", unmetered))
	}
	if unpriced > 0 {
		parts = append(parts, fmt.Sprintf("%d unpriced", unpriced))
	}
	return strings.Join(parts, ", ")
}

func lastLogAction(stageDir string) string {
	var last string
	for _, p := range flow.Phases() {
		for _, name := range flow.PhaseLogFiles(p) {
			data, err := os.ReadFile(filepath.Join(stageDir, name))
			if err != nil || len(data) == 0 {
				continue
			}
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) > 0 && lines[len(lines)-1] != "" {
				last = lines[len(lines)-1]
			}
		}
	}
	if len(last) > 60 {
		last = last[:60] + "..."
	}
	return last
}
