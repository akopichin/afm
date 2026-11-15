package server

import (
	"net/http"
	"time"

	"github.com/akopichin/afm/pkg/usageview"
)

// Wire contract of GET /api/usage/analytics; the dashboard parses testdata/usage-analytics, keep them in sync.
// Everywhere null means unknown and 0 means measured zero. Money and tokens are optional objects that
// ForMode drops, so a mode that hides them has no such key at any depth.

// UsageAnalyticsMode is the display mode the server computed from accounting.enabled and show_money.
type UsageAnalyticsMode = usageview.Mode

const (
	UsageModeFull     = usageview.ModeFull
	UsageModeNoMoney  = usageview.ModeNoMoney
	UsageModeTimeOnly = usageview.ModeTimeOnly
)

// UsageMetric is a quantity the client can compare and sort by.
type UsageMetric = usageview.Metric

const (
	UsageMetricCost      = usageview.MetricCost
	UsageMetricAgentTime = usageview.MetricAgentTime
	UsageMetricTokens    = usageview.MetricTokens
)

type (
	UsageView  string
	UsageGroup string
	UsageScope string
	UsageSort  = usageview.Sort
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

	UsageSortCost    = usageview.SortCost
	UsageSortTokens  = usageview.SortTokens
	UsageSortTime    = usageview.SortTime
	UsageSortJobs    = usageview.SortJobs
	UsageSortName    = usageview.SortName
	UsageSortModel   = usageview.SortModel
	UsageSortStarted = usageview.SortStarted

	UsageDirAsc  UsageDir = "asc"
	UsageDirDesc UsageDir = "desc"
)

type (
	UsageHealth       = usageview.Health
	UsageCompleteness = usageview.Completeness
	UsageCoverage     = usageview.Coverage
	UsageMetrics      = usageview.Metrics
	UsageSpend        = usageview.Spend
	UsageTime         = usageview.Time
	UsageTokens       = usageview.Tokens
	UsageTokenCounts  = usageview.TokenCounts
	UsageMoney        = usageview.Money
	UsageAgent        = usageview.Agent
	UsageModelRef     = usageview.ModelRef
	UsageStageRef     = usageview.StageRef
	UsageScopes       = usageview.Scopes
	UsageCatalog      = usageview.Catalog
	UsageRow          = usageview.Row
	UsageJob          = usageview.Job
	UsageTimeline     = usageview.Timeline
	UsageInterval     = usageview.Interval
)

const (
	UsageStoreOK          = usageview.StoreOK
	UsageStoreUnavailable = usageview.StoreUnavailable
	UsageStoreAbsent      = usageview.StoreAbsent
	UsageStoreDisabled    = usageview.StoreDisabled

	UsageCoverageFull    = usageview.CoverageFull
	UsageCoveragePartial = usageview.CoveragePartial
	UsageCoverageUnknown = usageview.CoverageUnknown

	UsageAgentStage       = usageview.AgentStage
	UsageAgentHelper      = usageview.AgentHelper
	UsageAgentRunAgent    = usageview.AgentRunAgent
	UsageAgentVerify      = usageview.AgentVerify
	UsageAgentAssistant   = usageview.AgentAssistant
	UsageAgentRunOverhead = usageview.AgentRunOverhead
	UsageAgentUnknown     = usageview.AgentUnknown

	UsageSourceObserved = usageview.SourceObserved
	UsageSourceHint     = usageview.SourceHint
	UsageSourceLegacy   = usageview.SourceLegacy
	UsageSourceMixed    = usageview.SourceMixed

	UsageModelSingle  = usageview.ModelSingle
	UsageModelMixed   = usageview.ModelMixed
	UsageModelUnknown = usageview.ModelUnknown

	UsageJobRunning     = usageview.JobRunning
	UsageJobOK          = usageview.JobOK
	UsageJobError       = usageview.JobError
	UsageJobInterrupted = usageview.JobInterrupted
	UsageJobIdleTimeout = usageview.JobIdleTimeout
	UsageJobUsageLimit  = usageview.JobUsageLimit
	UsageJobCancelled   = usageview.JobCancelled
	UsageJobYielded     = usageview.JobYielded
)

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

type UsageBreakdown struct {
	Group UsageGroup `json:"group"`
	Rows  []UsageRow `json:"rows"`
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
	r.Health = r.Health.ForMode(mode)
	r.Completeness = r.Completeness.ForMode(mode)
	r.Totals = r.Totals.ForMode(mode)
	if r.OutsideRunTotal != nil {
		outside := r.OutsideRunTotal.ForMode(mode)
		r.OutsideRunTotal = &outside
	}
	if r.Breakdown != nil {
		breakdown := *r.Breakdown
		breakdown.Rows = usageview.RowsForMode(breakdown.Rows, mode)
		r.Breakdown = &breakdown
	}
	if r.Jobs != nil {
		page := *r.Jobs
		page.Items = usageview.JobsForMode(page.Items, mode)
		r.Jobs = &page
	}
	if r.Charts != nil {
		charts := *r.Charts
		charts.Comparison = usageview.RowsForMode(charts.Comparison, mode)
		r.Charts = &charts
	}
	return r
}
