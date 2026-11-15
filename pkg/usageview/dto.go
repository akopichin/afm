package usageview

import "time"

const (
	StoreOK          = "ok"
	StoreUnavailable = "unavailable"
	StoreAbsent      = "absent"   // legacy run: no agent-jobs.jsonl
	StoreDisabled    = "disabled" // accounting is off, usage.jsonl is not read
)

// Health reports the two stores behind the response: usage.jsonl and agent-jobs.jsonl.
type Health struct {
	Usage string `json:"usage"`
	Jobs  string `json:"jobs"`
	// StartFailedJobs counts attempts that never started a process; they are not jobs and carry no time.
	StartFailedJobs int `json:"start_failed_jobs,omitempty"`
}

const (
	CoverageFull    = "full"
	CoveragePartial = "partial"
	CoverageUnknown = "unknown"
)

// Completeness has no Tokens in time_only and no Price outside full.
type Completeness struct {
	Time   Coverage  `json:"time"`
	Tokens *Coverage `json:"tokens,omitempty"`
	Price  *Coverage `json:"price,omitempty"`
}

// Coverage counts jobs with a known and an unknown value. Reasons are machine-readable.
type Coverage struct {
	Status  string   `json:"status"`
	Known   int      `json:"known"`
	Unknown int      `json:"unknown"`
	Reasons []string `json:"reasons,omitempty"`
}

// Metrics are the numbers of one group of jobs: a row, a filtered set or the run.
type Metrics struct {
	Jobs       int  `json:"jobs"`
	ActiveJobs int  `json:"active_jobs"`
	AgentTime  Time `json:"agent_time"`
	Spend
}

// Spend holds the data that exists only in some modes. It is embedded, so Tokens and Money
// appear next to their siblings.
type Spend struct {
	Tokens *Tokens `json:"tokens,omitempty"`
	Money  *Money  `json:"money,omitempty"`
}

// Time sums the measured durations. DurationMS is null when no job has a measured duration;
// running jobs count with their elapsed time at as_of.
type Time struct {
	DurationMS   *int64 `json:"duration_ms"`
	MeasuredJobs int    `json:"measured_jobs"`
	UnknownJobs  int    `json:"unknown_jobs"`
}

// Tokens sums the tokens of metered jobs. Counts is null when none is metered. For legacy usage
// the job counters count usage records, because such records have no job.
type Tokens struct {
	Counts        *TokenCounts `json:"counts"`
	MeteredJobs   int          `json:"metered_jobs"`
	UnmeteredJobs int          `json:"unmetered_jobs"`
	PendingJobs   int          `json:"pending_jobs"` // running, usage not reported yet
}

// TokenCounts uses the accounting convention: Total = uncached input + cache read + cache
// writes + output; ReasoningOutput is a part of Output.
type TokenCounts struct {
	UncachedInput   uint64 `json:"uncached_input"`
	CacheRead       uint64 `json:"cache_read"`
	CacheWrite5m    uint64 `json:"cache_write_5m"`
	CacheWrite1h    uint64 `json:"cache_write_1h"`
	CacheWriteOther uint64 `json:"cache_write_other"`
	Output          uint64 `json:"output"`
	ReasoningOutput uint64 `json:"reasoning_output"`
	Total           uint64 `json:"total"`
}

// Money sums the price of priced jobs. EstimatedCostUSD is null when none is priced.
type Money struct {
	EstimatedCostUSD *float64 `json:"estimated_cost_usd"`
	PricedJobs       int      `json:"priced_jobs"`
	UnpricedJobs     int      `json:"unpriced_jobs"`
}

const (
	AgentStage       = "stage"
	AgentHelper      = "helper"
	AgentRunAgent    = "run_agent"
	AgentVerify      = "verify"
	AgentAssistant   = "assistant"
	AgentRunOverhead = "run_overhead"
	AgentUnknown     = "unknown"
)

// Agent is a participant of the run. ID is the stable key; Title is a label only.
type Agent struct {
	ID              string `json:"id"`
	Kind            string `json:"kind"`
	Title           string `json:"title"`
	OutsideRunTotal bool   `json:"outside_run_total,omitempty"`
}

// ModelRef identifies a model by channel and model ID. ColorOrdinal is the order in which the
// model first appeared in the run; the client maps it to a color.
type ModelRef struct {
	Key          string `json:"key"`
	Channel      string `json:"channel"`
	Model        string `json:"model"`
	ColorOrdinal *int   `json:"color_ordinal"`
}

type StageRef struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// Scopes tells which scope filters would find anything.
type Scopes struct {
	RunWide      bool `json:"run_wide"`
	Unattributed bool `json:"unattributed"`
}

type Catalog struct {
	Stages []StageRef `json:"stages"`
	Agents []Agent    `json:"agents"`
	Models []ModelRef `json:"models"`
	Scopes Scopes     `json:"scopes"`
}

const (
	SourceObserved = "observed" // reported by the agent stream
	SourceHint     = "hint"     // taken from the configuration
	SourceLegacy   = "legacy"   // recorded before the source was kept
	SourceMixed    = "mixed"    // rows only: jobs differ

	ModelSingle  = "single"
	ModelMixed   = "mixed"
	ModelUnknown = "unknown"
)

// Row is a breakdown or comparison row: one model or one agent with all its jobs.
type Row struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// Agent is set in agent rows. Model is set in model rows and in agent rows with a single model;
	// it is null for the Unknown model bucket and for agents with mixed or unknown models.
	Agent       *Agent    `json:"agent,omitempty"`
	Model       *ModelRef `json:"model"`
	ModelState  string    `json:"model_state,omitempty"` // agent rows only
	ModelSource string    `json:"model_source,omitempty"`
	// Agents is set in model rows: how many distinct agents ran this model within the query scope.
	Agents int `json:"agents,omitempty"`
	Metrics
}

const (
	JobRunning     = "running"
	JobOK          = "ok"
	JobError       = "error"
	JobInterrupted = "interrupted"
	JobIdleTimeout = "idle_timeout"
	JobUsageLimit  = "usage_limit"
	JobCancelled   = "cancelled"
	JobYielded     = "yielded"
)

// Job is one agent job. DurationMS is null when the end is unknown and, for a running job, the
// elapsed time at as_of. RunOffsetMS is null when the job has no place on the run timeline.
type Job struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Agent       Agent     `json:"agent"`
	StageID     string    `json:"stage_id,omitempty"` // empty for run-wide jobs
	Model       *ModelRef `json:"model"`
	ModelSource string    `json:"model_source,omitempty"`
	Status      string    `json:"status"`
	StartedAt   time.Time `json:"started_at"`
	RunOffsetMS *int64    `json:"run_offset_ms"`
	DurationMS  *int64    `json:"duration_ms"`
	Spend
}

// Timeline lists placed jobs only. Total counts them before the Limit cut; Truncated says the
// cut happened; Unplaced counts jobs that have no place on the run timeline.
type Timeline struct {
	RangeStartMS int64      `json:"range_start_ms"`
	RangeEndMS   int64      `json:"range_end_ms"`
	Intervals    []Interval `json:"intervals"`
	Total        int        `json:"total"`
	Limit        int        `json:"limit"`
	Truncated    bool       `json:"truncated,omitempty"`
	Unplaced     int        `json:"unplaced"`
}

// Interval is one job on the run timeline; the agent and model are resolved through the catalog.
type Interval struct {
	JobID       string `json:"job_id"`
	Title       string `json:"title"`
	AgentID     string `json:"agent_id"`
	ModelKey    string `json:"model_key,omitempty"`
	StageID     string `json:"stage_id,omitempty"`
	Status      string `json:"status"`
	RunOffsetMS int64  `json:"run_offset_ms"`
	DurationMS  int64  `json:"duration_ms"`
}

// ForMode returns the health as the mode may show it; time_only means accounting is off and
// usage.jsonl is not read at all.
func (h Health) ForMode(mode Mode) Health {
	if !mode.HasTokens() {
		h.Usage = StoreDisabled
	}
	return h
}

// ForMode returns the completeness without the coverage the mode hides.
func (c Completeness) ForMode(mode Mode) Completeness {
	if !mode.HasTokens() {
		c.Tokens = nil
	}
	if !mode.HasMoney() {
		c.Price = nil
	}
	return c
}

// ForMode drops the tokens and money the mode hides.
func (s Spend) ForMode(mode Mode) Spend {
	if !mode.HasTokens() {
		s.Tokens = nil
	}
	if !mode.HasMoney() {
		s.Money = nil
	}
	return s
}

func (m Metrics) ForMode(mode Mode) Metrics {
	m.Spend = m.Spend.ForMode(mode)
	return m
}

func (j Job) ForMode(mode Mode) Job {
	j.Spend = j.Spend.ForMode(mode)
	return j
}

// RowsForMode returns a copy of the rows restricted to what the mode may show.
func RowsForMode(rows []Row, mode Mode) []Row {
	out := make([]Row, len(rows))
	for i, row := range rows {
		row.Metrics = row.ForMode(mode)
		out[i] = row
	}
	return out
}

// JobsForMode returns a copy of the jobs restricted to what the mode may show.
func JobsForMode(jobs []Job, mode Mode) []Job {
	out := make([]Job, len(jobs))
	for i, job := range jobs {
		out[i] = job.ForMode(mode)
	}
	return out
}
