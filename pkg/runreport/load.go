// Package runreport builds the offline report of a run directory: the same read model the dashboard shows,
// assembled from the files alone and without ever opening a store for writing.
package runreport

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/akopichin/afm-engine/engine"
	implstate "github.com/akopichin/afm-engine/engine/inspection/state"
	"github.com/akopichin/afm/contracts/accounting"
	"github.com/akopichin/afm/contracts/agentjobs"
	"github.com/akopichin/afm/contracts/budget"
	"github.com/akopichin/afm/contracts/state"
)

// SourceState is how one input of the report could be read.
type SourceState string

const (
	SourceOK          SourceState = "ok"
	SourceAbsent      SourceState = "absent"      // the file does not exist (a legacy run, no Assistant)
	SourceDegraded    SourceState = "degraded"    // readable, but some lines were dropped or conflict
	SourceUnavailable SourceState = "unavailable" // the file could not be read or has no usable header
	SourceCorrupt     SourceState = "corrupt"     // a complete line cannot be parsed
)

// SourceStatus is the state of one input with the reason when it is not ok.
type SourceStatus struct {
	State  SourceState `json:"state"`
	Reason string      `json:"reason,omitempty"`
}

// LoadOptions injects the clock so the report is reproducible.
type LoadOptions struct {
	Now func() time.Time
}

// Ledger is one usage.jsonl read without touching it.
type Ledger struct {
	Records []accounting.UsageRecord
	Status  SourceStatus
	// Err is the load error for a corrupt or unreadable file; it is nil for ok and absent.
	Err error
}

// Journal is one agent-jobs.jsonl read without touching it.
type Journal struct {
	Snapshot agentjobs.Snapshot
	Status   SourceStatus
}

// Assistant holds the side agent's own ledger and journal, which stay outside the run total.
type Assistant struct {
	Dir     string
	Usage   Ledger
	Journal Journal
}

// Sources is everything one report reads, taken at AsOf.
type Sources struct {
	RunDir string
	RunID  string
	AsOf   time.Time
	State  state.RunState
	// FirstStart is the time of each stage's first start, from the same events.jsonl prefix the replay accepts.
	FirstStart map[string]time.Time
	Usage      Ledger
	Jobs       Journal
	Assistant  Assistant
	// Budgets is nil when BudgetsErr is set; the budgets are a separate read of the run.
	Budgets    []budget.View
	BudgetsErr error
}

// Load reads the run once. Only a state that cannot be replayed is an error: every other source degrades to
// a status so a partial report survives one broken file.
func Load(runDir string, opts LoadOptions) (*Sources, error) {
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	asOf := now().UTC().Truncate(time.Millisecond)

	rs, err := implstate.LoadRunState(runDir)
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}
	// A relative path such as "." has no usable base name and no sibling side-agent dir.
	if abs, err := filepath.Abs(runDir); err == nil {
		runDir = abs
	}
	runID := filepath.Base(runDir)
	src := &Sources{
		RunDir:     runDir,
		RunID:      runID,
		AsOf:       asOf,
		State:      rs,
		FirstStart: firstStarts(runDir),
		Usage:      loadLedger(runDir),
		Jobs:       loadJournal(runDir),
	}
	absent := SourceStatus{State: SourceAbsent}
	src.Assistant = Assistant{Usage: Ledger{Status: absent}, Journal: Journal{Status: absent}}
	if sideDir := sideAgentDir(runDir); sideDir != "" {
		src.Assistant = Assistant{Dir: sideDir, Usage: loadLedger(sideDir), Journal: loadJournal(sideDir)}
	}
	src.Budgets, src.BudgetsErr = engine.BudgetViewsOffline(runDir)
	return src, nil
}

// sideAgentDir maps <afm>/runs/<id> to <afm>/side-agent/<id>; a run kept elsewhere has no known Assistant dir.
func sideAgentDir(runDir string) string {
	runs := filepath.Dir(runDir)
	if filepath.Base(runs) != "runs" {
		return ""
	}
	dir := filepath.Join(filepath.Dir(runs), "side-agent", filepath.Base(runDir))
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return ""
	}
	return dir
}

func loadLedger(dir string) Ledger {
	if _, err := os.Stat(filepath.Join(dir, "usage.jsonl")); errors.Is(err, fs.ErrNotExist) {
		return Ledger{Status: SourceStatus{State: SourceAbsent}}
	}
	led, err := accounting.Load(dir)
	switch {
	case err == nil:
		return Ledger{Records: led.Records(), Status: SourceStatus{State: SourceOK}}
	case errors.Is(err, accounting.ErrCorruptUsage):
		return Ledger{Status: SourceStatus{State: SourceCorrupt, Reason: "corrupt_usage"}, Err: err}
	default:
		return Ledger{Status: SourceStatus{State: SourceUnavailable, Reason: "io_failed"}, Err: err}
	}
}

func loadJournal(dir string) Journal {
	snap, err := agentjobs.Load(dir)
	if err != nil {
		// Health must say unavailable too: the facts builder reads it, not Status, to decide the journal is unusable.
		snap.Health = agentjobs.Health{Status: agentjobs.HealthUnavailable, Reasons: []string{agentjobs.ReasonIOFailed}}
		return Journal{Snapshot: snap, Status: SourceStatus{State: SourceUnavailable, Reason: agentjobs.ReasonIOFailed}}
	}
	status := SourceStatus{State: SourceOK}
	switch snap.Health.Status {
	case agentjobs.HealthDegraded:
		status.State = SourceDegraded
	case agentjobs.HealthUnavailable:
		status.State = SourceUnavailable
	default:
		// ok keeps the state set above
	}
	if len(snap.Health.Reasons) > 0 {
		status.Reason = snap.Health.Reasons[0]
	}
	if status.State == SourceUnavailable && status.Reason == agentjobs.ReasonNoJournal {
		status.State = SourceAbsent
	}
	return Journal{Snapshot: snap, Status: status}
}

// firstStartStatuses mirror the statuses whose first entry starts a stage's clock (afm-engine
// internal/state/stage_timing.go, firstStartStatuses).
var firstStartStatuses = map[state.StageStatus]bool{
	state.StatusPlanning: true, state.StatusAwaitingApproval: true, state.StatusRevising: true, state.StatusRunning: true,
}

// firstStarts reads the stage transitions of events.jsonl up to the first line the replay would stop at: a
// torn tail is ignored and a broken line ends the scan, so the result never goes past what LoadRunState used.
func firstStarts(runDir string) map[string]time.Time {
	out := map[string]time.Time{}
	data, err := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		return out
	}
	lines := bytes.Split(data, []byte("\n"))
	for _, line := range lines[:len(lines)-1] { // the last element is the uncommitted tail or empty
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var probe struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal(line, &probe) != nil {
			break
		}
		if probe.Kind != "" {
			continue // a control record, not a stage transition
		}
		var t state.Transition
		if json.Unmarshal(line, &t) != nil {
			break
		}
		if _, seen := out[t.StageID]; !seen && t.StageID != "" && firstStartStatuses[t.To] {
			out[t.StageID] = t.Time
		}
	}
	return out
}
