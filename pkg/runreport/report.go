package runreport

import (
	"fmt"
	"slices"
	"time"

	"github.com/akopichin/afm/contracts/state"
	"github.com/akopichin/afm/pkg/usageview"
)

// SchemaVersion is bumped on any incompatible change of the JSON document.
const SchemaVersion = 1

// Scope values of the report.
const (
	ScopeAll   = "all"
	ScopeStage = "stage"
)

// Options select what the report shows.
type Options struct {
	Mode usageview.Mode
	// StageID narrows the report to one stage; empty means the whole run.
	StageID string
}

// Report is the JSON document of `afm report`; each block has one named source (see decision 4 of the plan).
// Money lives only under money, price, rates or *_usd keys and tokens only under tokens, so a hidden mode leaves none.
type Report struct {
	SchemaVersion int            `json:"schema_version"`
	AsOf          time.Time      `json:"as_of"`
	Mode          usageview.Mode `json:"mode"`
	Run           RunInfo        `json:"run"`
	Scope         ScopeInfo      `json:"scope"`
	Sources       SourceReport   `json:"sources"`
	Stages        []StageReport  `json:"stages"`
	StageTime     StageTimeSum   `json:"stage_time"`
	Cost          *Cost          `json:"cost,omitempty"`
	Analytics     *Analytics     `json:"analytics"`
	Budgets       Budgets        `json:"budgets"`
	Diagnostics   []Diagnostic   `json:"diagnostics"`
}

// RunInfo is the run itself. ElapsedMS counts closed time only; an unfinished run reports its open interval
// separately and it is never added to a sum.
type RunInfo struct {
	ID           string          `json:"id"`
	Flow         string          `json:"flow"`
	Status       state.RunStatus `json:"status"`
	StartedAt    time.Time       `json:"started_at"`
	EndedAt      *time.Time      `json:"ended_at"`
	ElapsedMS    int64           `json:"elapsed_ms"`
	OpenInterval *OpenInterval   `json:"open_interval,omitempty"`
}

// OpenInterval is time that was still running when the log ended.
type OpenInterval struct {
	Kind  string    `json:"kind"`
	Since time.Time `json:"since"`
}

// ScopeInfo says whether the report covers the whole run or one stage.
type ScopeInfo struct {
	Kind    string `json:"kind"`
	StageID string `json:"stage_id,omitempty"`
}

// SourceReport is the state of every input.
type SourceReport struct {
	Usage          SourceStatus `json:"usage"`
	Jobs           SourceStatus `json:"jobs"`
	AssistantUsage SourceStatus `json:"assistant_usage"`
	AssistantJobs  SourceStatus `json:"assistant_jobs"`
	Budgets        SourceStatus `json:"budgets"`
}

// Diagnostic is a machine-readable note about a gap or a degraded input.
type Diagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Build assembles the report from the sources taken at src.AsOf. It fails only for a stage the run does not
// have; every degraded input becomes a status and a diagnostic.
func Build(src *Sources, opts Options) (*Report, error) {
	mode := opts.Mode.OrClosed()
	rs := src.State
	stageIDs := stageIDsOf(rs)
	if opts.StageID != "" && !slices.Contains(stageIDs, opts.StageID) {
		return nil, fmt.Errorf("unknown stage %q (stages of this run: %v)", opts.StageID, stageIDs)
	}

	r := &Report{
		SchemaVersion: SchemaVersion,
		AsOf:          src.AsOf,
		Mode:          mode,
		Run:           runInfo(src),
		Scope:         ScopeInfo{Kind: ScopeAll},
		Sources:       sourceReport(src),
		Budgets:       buildBudgets(src),
	}
	if opts.StageID != "" {
		r.Scope = ScopeInfo{Kind: ScopeStage, StageID: opts.StageID}
	}
	r.Stages, r.StageTime = buildStages(src, opts.StageID)
	if mode.HasTokens() {
		r.Cost = buildCost(src, mode, opts.StageID)
	}
	var analyticsDiag []Diagnostic
	r.Analytics, analyticsDiag = buildAnalytics(src, mode, opts.StageID)
	r.Diagnostics = append(diagnostics(src, mode, opts.StageID), analyticsDiag...)
	return r, nil
}

// Failure is the reason `afm report` exits with code 3 while still printing the report; empty when the
// inputs the mode needs are usable. A damaged Assistant journal or a missing journal of a legacy run is not a
// failure.
func Failure(src *Sources, mode usageview.Mode) string {
	if mode.HasTokens() && src.Usage.Err != nil {
		switch src.Usage.Status.State {
		case SourceCorrupt:
			return "cost unavailable (ledger corrupt)"
		default:
			return fmt.Sprintf("cost unavailable (cannot read usage ledger): %v", src.Usage.Err)
		}
	}
	if st := src.Jobs.Status; st.State == SourceUnavailable {
		return fmt.Sprintf("agent jobs unavailable (%s)", st.Reason)
	}
	return ""
}

func runInfo(src *Sources) RunInfo {
	rs := src.State
	info := RunInfo{
		ID: src.RunID, Flow: rs.FlowName, Status: rs.RunStatus,
		StartedAt: rs.StartedAt, EndedAt: rs.EndedAt, ElapsedMS: rs.ElapsedAccumulatedMs,
	}
	if since := rs.ElapsedSince(); since != nil {
		info.OpenInterval = &OpenInterval{Kind: "elapsed", Since: since.UTC()}
	}
	return info
}

func sourceReport(src *Sources) SourceReport {
	budgets := SourceStatus{State: SourceOK}
	if src.BudgetsErr != nil {
		budgets = SourceStatus{State: SourceUnavailable, Reason: "budgets_unavailable"}
	}
	return SourceReport{
		Usage: src.Usage.Status, Jobs: src.Jobs.Status,
		AssistantUsage: src.Assistant.Usage.Status, AssistantJobs: src.Assistant.Journal.Status,
		Budgets: budgets,
	}
}

// diagnostics lists the gaps a reader must not mistake for zero.
func diagnostics(src *Sources, mode usageview.Mode, stageID string) []Diagnostic {
	out := []Diagnostic{}
	add := func(code, format string, args ...any) {
		out = append(out, Diagnostic{Code: code, Message: fmt.Sprintf(format, args...)})
	}
	if mode.HasTokens() {
		switch src.Usage.Status.State {
		case SourceCorrupt, SourceUnavailable:
			add("usage_"+string(src.Usage.Status.State), "usage ledger %s: token and cost figures are omitted", src.Usage.Status.State)
		default:
			// absent or ok: nothing to flag
		}
	}
	switch st := src.Jobs.Status; st.State {
	case SourceAbsent:
		add("jobs_absent", "no agent-jobs journal (legacy run): agent time and per-job figures are unknown")
	case SourceDegraded, SourceUnavailable:
		add("jobs_"+string(st.State), "agent-jobs journal %s (%s)", st.State, st.Reason)
	default:
		// ok: nothing to flag
	}
	for _, a := range []struct {
		name string
		st   SourceStatus
	}{{"assistant_usage", src.Assistant.Usage.Status}, {"assistant_jobs", src.Assistant.Journal.Status}} {
		if a.st.State == SourceCorrupt || a.st.State == SourceUnavailable || a.st.State == SourceDegraded {
			add(a.name+"_"+string(a.st.State), "%s %s (%s)", a.name, a.st.State, a.st.Reason)
		}
	}
	if src.BudgetsErr != nil {
		add("budgets_unavailable", "budgets could not be read: %v", src.BudgetsErr)
	}
	if stageID != "" {
		add("stage_scope", "stage scope: run-wide totals, the Assistant and run agents are not included; budgets are run-wide")
	}
	return out
}

// StageTiming is the stage time ledger as the dashboard shows it. The sums exist only for a started stage with
// complete data; zeros are kept to tell 0 from unknown.
type StageTiming struct {
	Started  bool `json:"started"`
	Complete bool `json:"complete"`
	*StageTimeSums
	Open *OpenInterval `json:"open,omitempty"`
}

// StageTimeSums are the closed intervals per kind in milliseconds.
type StageTimeSums struct {
	WorkMS       int64 `json:"work_ms"`
	ApprovalMS   int64 `json:"approval_ms"`
	HelpersMS    int64 `json:"helpers_ms"`
	BackoffMS    int64 `json:"backoff_ms"`
	SchedulingMS int64 `json:"scheduling_ms"`
	PausedMS     int64 `json:"paused_ms"`
}

// working, waiting and paused are the three groups of docs/dashboard.md#stage-time.
func (s StageTimeSums) working() int64 { return s.WorkMS }
func (s StageTimeSums) waiting() int64 {
	return s.ApprovalMS + s.HelpersMS + s.BackoffMS + s.SchedulingMS
}
func (s StageTimeSums) paused() int64 { return s.PausedMS }

// newStageTiming projects the ledger without reclassifying it, like the dashboard's stage time view.
func newStageTiming(tm state.StageTiming) StageTiming {
	switch {
	case !tm.Started:
		return StageTiming{Complete: true}
	case tm.Incomplete:
		return StageTiming{Started: true}
	}
	view := StageTiming{
		Started: true, Complete: true,
		StageTimeSums: &StageTimeSums{
			WorkMS: tm.WorkMs, ApprovalMS: tm.ApprovalMs, HelpersMS: tm.HelpersMs,
			BackoffMS: tm.BackoffMs, SchedulingMS: tm.SchedulingMs, PausedMS: tm.PausedMs,
		},
	}
	if tm.OpenKind != state.TimeNone {
		view.Open = &OpenInterval{Kind: string(tm.OpenKind), Since: tm.OpenSince.UTC()}
	}
	return view
}

// StageReport is one stage: its status and time.
type StageReport struct {
	ID     string      `json:"id"`
	Status string      `json:"status"`
	Timing StageTiming `json:"timing"`
}

// StageTimeSum groups the stage times of the report. Stages with unknown time are listed, not counted.
type StageTimeSum struct {
	WorkingMS    int64    `json:"working_ms"`
	WaitingMS    int64    `json:"waiting_ms"`
	PausedMS     int64    `json:"paused_ms"`
	IncompleteID []string `json:"incomplete_stages"`
}

// stageIDsOf is the stage registry order, then stages that only the log knows, so none is silently dropped.
func stageIDsOf(rs state.RunState) []string {
	ids := slices.Clone(rs.StageOrder)
	var extra []string
	for id := range rs.Stages {
		if !slices.Contains(ids, id) {
			extra = append(extra, id)
		}
	}
	slices.Sort(extra)
	return append(ids, extra...)
}

func buildStages(src *Sources, stageID string) ([]StageReport, StageTimeSum) {
	stages := []StageReport{}
	sum := StageTimeSum{IncompleteID: []string{}}
	for _, id := range stageIDsOf(src.State) {
		if stageID != "" && id != stageID {
			continue
		}
		st := src.State.Stages[id]
		timing := newStageTiming(st.Timing)
		stages = append(stages, StageReport{ID: id, Status: string(st.Status), Timing: timing})
		switch {
		case timing.StageTimeSums != nil:
			sum.WorkingMS += timing.working()
			sum.WaitingMS += timing.waiting()
			sum.PausedMS += timing.paused()
		case timing.Started && !timing.Complete:
			sum.IncompleteID = append(sum.IncompleteID, id)
		default:
			// never started: no time to count
		}
	}
	return stages, sum
}
