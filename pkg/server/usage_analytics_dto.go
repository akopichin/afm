package server

import (
	"net/http"
	"time"
)

// Wire contract of GET /api/usage/analytics; the dashboard parses testdata/usage-analytics, keep them in sync.
// Everywhere null means unknown and 0 means measured zero. Money and tokens are optional objects that
// ForMode drops, so a mode that hides them has no such key at any depth.

// UsageAnalyticsMode is the display mode the server computed from accounting.enabled and show_money.
type UsageAnalyticsMode string

const (
	UsageModeFull     UsageAnalyticsMode = "full"
	UsageModeNoMoney  UsageAnalyticsMode = "no_money"
	UsageModeTimeOnly UsageAnalyticsMode = "time_only"
)

func (m UsageAnalyticsMode) HasMoney() bool  { return m == UsageModeFull }
func (m UsageAnalyticsMode) HasTokens() bool { return m != UsageModeTimeOnly }

// UsageMetric is a quantity the client can compare and sort by.
type UsageMetric string

const (
	UsageMetricCost      UsageMetric = "cost"
	UsageMetricAgentTime UsageMetric = "agent_time"
	UsageMetricTokens    UsageMetric = "tokens"
)

// Metrics lists the metrics available in the mode, in the order the client shows them.
func (m UsageAnalyticsMode) Metrics() []UsageMetric {
	metrics := make([]UsageMetric, 0, 3)
	if m.HasMoney() {
		metrics = append(metrics, UsageMetricCost)
	}
	metrics = append(metrics, UsageMetricAgentTime)
	if m.HasTokens() {
		metrics = append(metrics, UsageMetricTokens)
	}
	return metrics
}

type (
	UsageView  string
	UsageGroup string
	UsageScope string
	UsageSort  string
	UsageDir   string
)

const (
	UsageViewBreakdown UsageView = "breakdown"
	UsageViewJobs      UsageView = "jobs"
	UsageViewCharts    UsageView = "charts"

	UsageGroupModel UsageGroup = "model"
	UsageGroupAgent UsageGroup = "agent"

	UsageScopeAll          UsageScope = "all"
	UsageScopeStage        UsageScope = "stage" // together with stage_id
	UsageScopeRunWide      UsageScope = "run_wide"
	UsageScopeUnattributed UsageScope = "unattributed"

	UsageSortCost    UsageSort = "cost"
	UsageSortTokens  UsageSort = "tokens"
	UsageSortTime    UsageSort = "time" // agent time in breakdown rows, duration in jobs
	UsageSortJobs    UsageSort = "jobs"
	UsageSortName    UsageSort = "name"
	UsageSortModel   UsageSort = "model"
	UsageSortStarted UsageSort = "started"

	UsageDirAsc  UsageDir = "asc"
	UsageDirDesc UsageDir = "desc"
)

// AllowsSort reports whether the mode has the data the sort key orders by; the endpoint answers
// 400 for a sort the mode cannot serve.
func (m UsageAnalyticsMode) AllowsSort(sort UsageSort) bool {
	switch sort {
	case UsageSortCost:
		return m.HasMoney()
	case UsageSortTokens:
		return m.HasTokens()
	default:
		return true
	}
}

// UsageAnalyticsStatus is the analytics part of /api/status: enough to open the details, to notice changes
// (the revision is opaque, compared for equality) and to label the overview without a request. Models and
// Agents count the run total, Assistant excluded, in every mode.
type UsageAnalyticsStatus struct {
	Revision string             `json:"revision"`
	Mode     UsageAnalyticsMode `json:"mode"`
	Models   int                `json:"models"`
	Agents   int                `json:"agents"`
}

// UsageAnalytics is the response of GET /api/usage/analytics. View says which of Breakdown, Jobs
// and Charts is set; everything else comes from one revision.
type UsageAnalytics struct {
	View     UsageView          `json:"view"`
	AsOf     time.Time          `json:"as_of"` // anchor for the elapsed time of running jobs
	Revision string             `json:"revision"`
	Mode     UsageAnalyticsMode `json:"mode"`
	Metrics  []UsageMetric      `json:"metrics"`
	Health   UsageHealth        `json:"health"`
	// Completeness of time, tokens and price is judged independently.
	Completeness UsageCompleteness `json:"completeness"`
	// Query is the normalized request this response answers; the client drops answers that differ
	// from its current state.
	Query UsageQuery `json:"query"`
	// Totals cover the filtered jobs except agents flagged outside_run_total (the Assistant), so
	// unfiltered totals equal the run total. OutsideRunTotal sums those agents.
	Totals          UsageMetrics  `json:"totals"`
	OutsideRunTotal *UsageMetrics `json:"outside_run_total,omitempty"`
	// Catalog ignores the filters: it is the source for filter menus and model colors.
	Catalog UsageCatalog `json:"catalog"`

	Breakdown *UsageBreakdown `json:"breakdown,omitempty"`
	Jobs      *UsageJobsPage  `json:"jobs,omitempty"`
	Charts    *UsageCharts    `json:"charts,omitempty"`
}

const (
	UsageStoreOK          = "ok"
	UsageStoreUnavailable = "unavailable"
	UsageStoreAbsent      = "absent"   // legacy run: no agent-jobs.jsonl
	UsageStoreDisabled    = "disabled" // accounting is off, usage.jsonl is not read
)

// UsageHealth reports the two stores behind the response: usage.jsonl and agent-jobs.jsonl.
type UsageHealth struct {
	Usage string `json:"usage"`
	Jobs  string `json:"jobs"`
	// StartFailedJobs counts attempts that never started a process; they are not jobs and carry no time.
	StartFailedJobs int `json:"start_failed_jobs,omitempty"`
}

const (
	UsageCoverageFull    = "full"
	UsageCoveragePartial = "partial"
	UsageCoverageUnknown = "unknown"
)

// UsageCompleteness has no Tokens in time_only and no Price outside full.
type UsageCompleteness struct {
	Time   UsageCoverage  `json:"time"`
	Tokens *UsageCoverage `json:"tokens,omitempty"`
	Price  *UsageCoverage `json:"price,omitempty"`
}

// UsageCoverage counts jobs with a known and an unknown value. Reasons are machine-readable.
type UsageCoverage struct {
	Status  string   `json:"status"`
	Known   int      `json:"known"`
	Unknown int      `json:"unknown"`
	Reasons []string `json:"reasons,omitempty"`
}

// UsageQuery echoes the applied parameters after defaults and normalization. Limit is set for jobs only.
type UsageQuery struct {
	View    UsageView  `json:"view"`
	Group   UsageGroup `json:"group"`
	Scope   UsageScope `json:"scope"`
	StageID string     `json:"stage_id,omitempty"`
	Agent   string     `json:"agent,omitempty"`
	Model   string     `json:"model,omitempty"`
	Sort    UsageSort  `json:"sort"`
	Dir     UsageDir   `json:"dir"`
	Limit   int        `json:"limit,omitempty"`
}

// UsageMetrics are the numbers of one group of jobs: a row, a filtered set or the run.
type UsageMetrics struct {
	Jobs       int       `json:"jobs"`
	ActiveJobs int       `json:"active_jobs"`
	AgentTime  UsageTime `json:"agent_time"`
	UsageSpend
}

// UsageSpend holds the data that exists only in some modes. It is embedded, so Tokens and Money
// appear next to their siblings.
type UsageSpend struct {
	Tokens *UsageTokens `json:"tokens,omitempty"`
	Money  *UsageMoney  `json:"money,omitempty"`
}

// UsageTime sums the measured durations. DurationMS is null when no job has a measured duration;
// running jobs count with their elapsed time at as_of.
type UsageTime struct {
	DurationMS   *int64 `json:"duration_ms"`
	MeasuredJobs int    `json:"measured_jobs"`
	UnknownJobs  int    `json:"unknown_jobs"`
}

// UsageTokens sums the tokens of metered jobs. Counts is null when none is metered. For legacy usage
// the job counters count usage records, because such records have no job.
type UsageTokens struct {
	Counts        *UsageTokenCounts `json:"counts"`
	MeteredJobs   int               `json:"metered_jobs"`
	UnmeteredJobs int               `json:"unmetered_jobs"`
	PendingJobs   int               `json:"pending_jobs"` // running, usage not reported yet
}

// UsageTokenCounts uses the accounting convention: Total = uncached input + cache read + cache
// writes + output; ReasoningOutput is a part of Output.
type UsageTokenCounts struct {
	UncachedInput   uint64 `json:"uncached_input"`
	CacheRead       uint64 `json:"cache_read"`
	CacheWrite5m    uint64 `json:"cache_write_5m"`
	CacheWrite1h    uint64 `json:"cache_write_1h"`
	CacheWriteOther uint64 `json:"cache_write_other"`
	Output          uint64 `json:"output"`
	ReasoningOutput uint64 `json:"reasoning_output"`
	Total           uint64 `json:"total"`
}

// UsageMoney sums the price of priced jobs. EstimatedCostUSD is null when none is priced.
type UsageMoney struct {
	EstimatedCostUSD *float64 `json:"estimated_cost_usd"`
	PricedJobs       int      `json:"priced_jobs"`
	UnpricedJobs     int      `json:"unpriced_jobs"`
}

const (
	UsageAgentStage       = "stage"
	UsageAgentHelper      = "helper"
	UsageAgentRunAgent    = "run_agent"
	UsageAgentVerify      = "verify"
	UsageAgentAssistant   = "assistant"
	UsageAgentRunOverhead = "run_overhead"
	UsageAgentUnknown     = "unknown"
)

// UsageAgent is a participant of the run. ID is the stable key; Title is a label only.
type UsageAgent struct {
	ID              string `json:"id"`
	Kind            string `json:"kind"`
	Title           string `json:"title"`
	OutsideRunTotal bool   `json:"outside_run_total,omitempty"`
}

// UsageModelRef identifies a model by channel and model ID. ColorOrdinal is the order in which the
// model first appeared in the run; the client maps it to a color.
type UsageModelRef struct {
	Key          string `json:"key"`
	Channel      string `json:"channel"`
	Model        string `json:"model"`
	ColorOrdinal *int   `json:"color_ordinal"`
}

type UsageStageRef struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// UsageScopes tells which scope filters would find anything.
type UsageScopes struct {
	RunWide      bool `json:"run_wide"`
	Unattributed bool `json:"unattributed"`
}

type UsageCatalog struct {
	Stages []UsageStageRef `json:"stages"`
	Agents []UsageAgent    `json:"agents"`
	Models []UsageModelRef `json:"models"`
	Scopes UsageScopes     `json:"scopes"`
}

const (
	UsageSourceObserved = "observed" // reported by the agent stream
	UsageSourceHint     = "hint"     // taken from the configuration
	UsageSourceLegacy   = "legacy"   // recorded before the source was kept
	UsageSourceMixed    = "mixed"    // rows only: jobs differ

	UsageModelSingle  = "single"
	UsageModelMixed   = "mixed"
	UsageModelUnknown = "unknown"
)

// UsageRow is a breakdown or comparison row: one model or one agent with all its jobs.
type UsageRow struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// Agent is set in agent rows. Model is set in model rows and in agent rows with a single model;
	// it is null for the Unknown model bucket and for agents with mixed or unknown models.
	Agent       *UsageAgent    `json:"agent,omitempty"`
	Model       *UsageModelRef `json:"model"`
	ModelState  string         `json:"model_state,omitempty"` // agent rows only
	ModelSource string         `json:"model_source,omitempty"`
	// Agents is set in model rows: how many distinct agents ran this model within the query scope.
	Agents int `json:"agents,omitempty"`
	UsageMetrics
}

type UsageBreakdown struct {
	Group UsageGroup `json:"group"`
	Rows  []UsageRow `json:"rows"`
}

const (
	UsageJobRunning     = "running"
	UsageJobOK          = "ok"
	UsageJobError       = "error"
	UsageJobInterrupted = "interrupted"
	UsageJobIdleTimeout = "idle_timeout"
	UsageJobUsageLimit  = "usage_limit"
	UsageJobCancelled   = "cancelled"
	UsageJobYielded     = "yielded"
)

// UsageJob is one agent job. DurationMS is null when the end is unknown and, for a running job, the
// elapsed time at as_of. RunOffsetMS is null when the job has no place on the run timeline.
type UsageJob struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	Agent       UsageAgent     `json:"agent"`
	StageID     string         `json:"stage_id,omitempty"` // empty for run-wide jobs
	Model       *UsageModelRef `json:"model"`
	ModelSource string         `json:"model_source,omitempty"`
	Status      string         `json:"status"`
	StartedAt   time.Time      `json:"started_at"`
	RunOffsetMS *int64         `json:"run_offset_ms"`
	DurationMS  *int64         `json:"duration_ms"`
	UsageSpend
}

const (
	UsageFocusFound       = "found"
	UsageFocusFilteredOut = "filtered_out"
	UsageFocusNotFound    = "not_found"
)

// UsageFocus answers focus_job. When found, the page holds the job and Index is its 0-based
// position in the sorted, filtered list.
type UsageFocus struct {
	Status string `json:"status"`
	JobID  string `json:"job_id"`
	Index  *int   `json:"index,omitempty"`
}

// UsageJobsPage is a page of jobs in a stable order. Total counts all jobs matching the filters,
// including those of agents outside the run total.
type UsageJobsPage struct {
	Items      []UsageJob  `json:"items"`
	Total      int         `json:"total"`
	Limit      int         `json:"limit"`
	NextCursor string      `json:"next_cursor,omitempty"`
	PrevCursor string      `json:"prev_cursor,omitempty"`
	Focus      *UsageFocus `json:"focus,omitempty"`
}

// UsageCharts holds the comparison rows and a bounded timeline of the same revision.
type UsageCharts struct {
	Group      UsageGroup    `json:"group"`
	Comparison []UsageRow    `json:"comparison"`
	Timeline   UsageTimeline `json:"timeline"`
}

// UsageTimeline lists placed jobs only. Total counts them before the Limit cut; Truncated says the
// cut happened; Unplaced counts jobs that have no place on the run timeline.
type UsageTimeline struct {
	RangeStartMS int64           `json:"range_start_ms"`
	RangeEndMS   int64           `json:"range_end_ms"`
	Intervals    []UsageInterval `json:"intervals"`
	Total        int             `json:"total"`
	Limit        int             `json:"limit"`
	Truncated    bool            `json:"truncated,omitempty"`
	Unplaced     int             `json:"unplaced"`
}

// UsageInterval is one job on the run timeline; the agent and model are resolved through the catalog.
type UsageInterval struct {
	JobID       string `json:"job_id"`
	Title       string `json:"title"`
	AgentID     string `json:"agent_id"`
	ModelKey    string `json:"model_key,omitempty"`
	StageID     string `json:"stage_id,omitempty"`
	Status      string `json:"status"`
	RunOffsetMS int64  `json:"run_offset_ms"`
	DurationMS  int64  `json:"duration_ms"`
}

// UsageErrorCode is the machine-readable part of an error answer.
type UsageErrorCode string

const (
	UsageErrorInvalidParam UsageErrorCode = "invalid_param"
	UsageErrorStaleCursor  UsageErrorCode = "stale_cursor"
)

func (c UsageErrorCode) HTTPStatus() int {
	if c == UsageErrorStaleCursor {
		return http.StatusConflict
	}
	return http.StatusBadRequest
}

// UsageAnalyticsError is the JSON body of a 400 or 409 answer.
type UsageAnalyticsError struct {
	Code    UsageErrorCode `json:"code"`
	Message string         `json:"message"`
}

// ForMode returns a copy of the response restricted to what the mode may show. It never modifies
// the receiver, so a cached response can serve every mode.
func (r UsageAnalytics) ForMode(mode UsageAnalyticsMode) UsageAnalytics {
	r.Mode = mode
	r.Metrics = mode.Metrics()
	if !mode.HasTokens() {
		// time_only means accounting is off: usage.jsonl is not read at all.
		r.Health.Usage = UsageStoreDisabled
		r.Completeness.Tokens = nil
	}
	if !mode.HasMoney() {
		r.Completeness.Price = nil
	}
	r.Totals = r.Totals.forMode(mode)
	if r.OutsideRunTotal != nil {
		outside := r.OutsideRunTotal.forMode(mode)
		r.OutsideRunTotal = &outside
	}
	if r.Breakdown != nil {
		breakdown := *r.Breakdown
		breakdown.Rows = rowsForMode(breakdown.Rows, mode)
		r.Breakdown = &breakdown
	}
	if r.Jobs != nil {
		page := *r.Jobs
		page.Items = make([]UsageJob, len(r.Jobs.Items))
		for i, job := range r.Jobs.Items {
			job.UsageSpend = job.forMode(mode)
			page.Items[i] = job
		}
		r.Jobs = &page
	}
	if r.Charts != nil {
		charts := *r.Charts
		charts.Comparison = rowsForMode(charts.Comparison, mode)
		r.Charts = &charts
	}
	return r
}

func (s UsageSpend) forMode(mode UsageAnalyticsMode) UsageSpend {
	if !mode.HasTokens() {
		s.Tokens = nil
	}
	if !mode.HasMoney() {
		s.Money = nil
	}
	return s
}

func (m UsageMetrics) forMode(mode UsageAnalyticsMode) UsageMetrics {
	m.UsageSpend = m.UsageSpend.forMode(mode)
	return m
}

func rowsForMode(rows []UsageRow, mode UsageAnalyticsMode) []UsageRow {
	out := make([]UsageRow, len(rows))
	for i, row := range rows {
		row.UsageMetrics = row.forMode(mode)
		out[i] = row
	}
	return out
}
