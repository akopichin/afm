package accounting

import (
	"slices"
	"time"

	"github.com/akopichin/afm/pkg/agentjobs"
)

// FilterScope narrows the facts by where the work belongs.
type FilterScope string

const (
	FilterAll          FilterScope = "all"
	FilterStage        FilterScope = "stage" // together with Filter.StageID
	FilterRunWide      FilterScope = "run_wide"
	FilterUnattributed FilterScope = "unattributed"
)

// Group says what a breakdown row stands for.
type Group string

const (
	GroupModel Group = "model"
	GroupAgent Group = "agent"
)

// SortKey picks the column rows and jobs are ordered by.
type SortKey string

const (
	SortCost    SortKey = "cost"
	SortTokens  SortKey = "tokens"
	SortTime    SortKey = "time"
	SortJobs    SortKey = "jobs"
	SortName    SortKey = "name"
	SortModel   SortKey = "model"
	SortStarted SortKey = "started"
)

// Filter selects entries. The zero value selects everything. Agent is an agent ID and Model a model key
// (UnknownModelKey for work without one); an ID nothing carries selects nothing.
type Filter struct {
	Scope   FilterScope
	StageID string
	Agent   string
	Model   string
}

// Metrics are the numbers of a group of entries: a row, a filtered set or the run.
type Metrics struct {
	Jobs       int
	ActiveJobs int
	Time       TimeMetrics
	Tokens     TokenMetrics
	Money      MoneyMetrics
}

// TimeMetrics sums measured durations; a running job counts with its elapsed time at as_of.
// DurationMS is nil when no job has a measured duration.
type TimeMetrics struct {
	DurationMS *int64
	Measured   int
	Unknown    int
}

// TokenMetrics sums the tokens of metered entries; Counts is nil when none is metered. For usage that has
// no job the counters count usage records.
type TokenMetrics struct {
	Counts    *Tokens
	Metered   int
	Unmetered int
	Pending   int // running job, usage not reported yet
}

// MoneyMetrics sums the price of priced entries; EstimatedCostUSD is nil when none is priced.
type MoneyMetrics struct {
	EstimatedCostUSD *float64
	Priced           int
	Unpriced         int
}

// Row is a breakdown or comparison row: one model or one agent with all its jobs. Agent is set in agent
// rows. Model is set in model rows and in agent rows with a single model.
type Row struct {
	Key         string
	Label       string
	Agent       *AgentRef
	Model       *ModelRef
	ModelState  string // agent rows only
	ModelSource string
	Agents      int // model rows only: distinct agents with a job of this model in scope
	Metrics
}

// JobItem is one job. DurationMS is nil when the end is unknown and, for a running job, the elapsed time at
// as_of. RunOffsetMS is nil when the job has no place on the run timeline.
type JobItem struct {
	ID          string
	Title       string
	Agent       AgentRef
	StageID     string
	Model       *ModelRef
	ModelSource string
	Status      string
	StartedAt   time.Time
	RunOffsetMS *int64
	DurationMS  *int64
	Tokens      TokenMetrics
	Money       MoneyMetrics
}

// Timeline lists the placed jobs; Unplaced counts those with no place on the run timeline.
type Timeline struct {
	RangeStartMS int64
	RangeEndMS   int64
	Intervals    []Interval
	Total        int // placed jobs before the cut
	Limit        int
	Truncated    bool
	Unplaced     int
}

type Interval struct {
	JobID       string
	Title       string
	AgentID     string
	ModelKey    string
	StageID     string
	Status      string
	RunOffsetMS int64
	DurationMS  int64
}

// CompletenessStatus is how far a measurement can be trusted.
type CompletenessStatus string

const (
	CompletenessFull    CompletenessStatus = "full"
	CompletenessPartial CompletenessStatus = "partial"
	CompletenessUnknown CompletenessStatus = "unknown"
)

// KnownCount counts entries with a known and an unknown value; Reasons are machine codes.
type KnownCount struct {
	Status  CompletenessStatus
	Known   int
	Unknown int
	Reasons []string
}

// Completeness judges time, tokens and price independently.
type Completeness struct {
	Time   KnownCount
	Tokens KnownCount
	Price  KnownCount
}

// Selection is the filtered view of one Facts at one as_of. The totals leave out agents that sit outside the
// run total (the Assistant), so unfiltered totals equal the run's own.
type Selection struct {
	f       *Facts
	asOf    time.Time
	scope   Filter
	entries []*entry
}

// Select applies the filter. as_of fixes the elapsed time of running jobs.
func (f *Facts) Select(flt Filter, asOf time.Time) *Selection {
	s := &Selection{f: f, asOf: asOf, scope: flt}
	for i := range f.entries {
		e := &f.entries[i]
		if f.matches(e, flt) {
			s.entries = append(s.entries, e)
		}
	}
	return s
}

func (f *Facts) matches(e *entry, flt Filter) bool {
	if e.unseen() {
		// It still takes part in the filter so the completeness reason can be scoped to it.
		return f.scopeMatches(e, flt) && flt.Agent == "" && flt.Model == ""
	}
	if !f.scopeMatches(e, flt) {
		return false
	}
	if flt.Agent != "" && e.agentID != flt.Agent {
		return false
	}
	if flt.Model != "" && modelKeyOrUnknown(e) != flt.Model {
		return false
	}
	return true
}

func (f *Facts) scopeMatches(e *entry, flt Filter) bool {
	switch flt.Scope {
	case FilterStage:
		return e.stageID == flt.StageID
	case FilterRunWide:
		return e.stageID == "" && f.agents[e.agentID].Kind != AgentKindUnknown
	case FilterUnattributed:
		return e.stageID == "" && f.agents[e.agentID].Kind == AgentKindUnknown
	default:
		return true
	}
}

func modelKeyOrUnknown(e *entry) string {
	if e.model == "" {
		return UnknownModelKey
	}
	return e.model
}

// durationAt is the job's duration at asOf: measured, or the elapsed time of a running job.
func (e *entry) durationAt(asOf time.Time) *int64 {
	switch {
	case e.running:
		ms := max(asOf.Sub(e.at).Milliseconds(), 0)
		return &ms
	case e.durationMS != nil:
		ms := *e.durationMS
		return &ms
	default:
		return nil
	}
}

type tokenState int

const (
	tokensNone tokenState = iota // nothing to count (an attempt that never ran left no usage)
	tokensMetered
	tokensUnmetered
	tokensPending
)

func (e *entry) tokenState() tokenState {
	switch {
	case e.usage.present && e.usage.metered:
		return tokensMetered
	case e.usage.present:
		return tokensUnmetered
	case e.kind != entryJob:
		return tokensNone
	case e.running:
		return tokensPending
	default:
		return tokensUnmetered
	}
}

// accumulator folds entries into Metrics.
type accumulator struct {
	asOf     time.Time
	m        Metrics
	duration int64
	tokens   Tokens
	cost     float64
}

func (a *accumulator) add(e *entry) {
	if e.kind == entryJob {
		a.m.Jobs++
		if e.running {
			a.m.ActiveJobs++
		}
		if d := e.durationAt(a.asOf); d != nil {
			a.m.Time.Measured++
			a.duration += *d
		} else {
			a.m.Time.Unknown++
		}
	}
	switch e.tokenState() {
	case tokensMetered:
		a.m.Tokens.Metered++
		a.tokens = a.tokens.Add(e.usage.tokens)
		if e.usage.priced {
			a.m.Money.Priced++
			a.cost += e.usage.costUSD
		} else {
			a.m.Money.Unpriced++
		}
	case tokensUnmetered:
		a.m.Tokens.Unmetered++
	case tokensPending:
		a.m.Tokens.Pending++
	default: // tokensNone: an attempt that never ran has nothing to count
	}
}

func (a *accumulator) result() Metrics {
	m := a.m
	if m.Time.Measured > 0 {
		d := a.duration
		m.Time.DurationMS = &d
	}
	if m.Tokens.Metered > 0 {
		t := a.tokens
		m.Tokens.Counts = &t
	}
	if m.Money.Priced > 0 {
		c := a.cost
		m.Money.EstimatedCostUSD = &c
	}
	return m
}

func (s *Selection) metricsOf(entries []*entry) Metrics {
	acc := accumulator{asOf: s.asOf}
	for _, e := range entries {
		acc.add(e)
	}
	return acc.result()
}

// split separates the selection at the Assistant's namespace.
func (s *Selection) split() (inside, outside []*entry) {
	for _, e := range s.entries {
		if e.outside(s.f) {
			outside = append(outside, e)
		} else {
			inside = append(inside, e)
		}
	}
	return inside, outside
}

// Totals are the metrics of the selection without the agents outside the run total.
func (s *Selection) Totals() Metrics {
	inside, _ := s.split()
	return s.metricsOf(inside)
}

// Outside sums the agents that sit outside the run total; nil when the selection has none.
func (s *Selection) Outside() *Metrics {
	_, outside := s.split()
	if len(outside) == 0 {
		return nil
	}
	m := s.metricsOf(outside)
	return &m
}

// Rows groups the selection. Model rows leave out the Assistant, so they add up to Totals; agent rows list
// it, flagged.
func (s *Selection) Rows(group Group, sortBy SortKey, desc bool) []Row {
	type bucket struct {
		row     Row
		acc     accumulator
		entries []*entry
	}
	buckets := map[string]*bucket{}
	var order []string
	for _, e := range s.entries {
		if e.unseen() {
			continue
		}
		key, label := s.rowIdentity(e, group)
		if group == GroupModel && e.outside(s.f) {
			continue
		}
		b := buckets[key]
		if b == nil {
			b = &bucket{row: Row{Key: key, Label: label}, acc: accumulator{asOf: s.asOf}}
			buckets[key] = b
			order = append(order, key)
		}
		b.acc.add(e)
		b.entries = append(b.entries, e)
	}

	rows := make([]Row, 0, len(order))
	for _, key := range order {
		b := buckets[key]
		b.row.Metrics = b.acc.result()
		s.describeModels(&b.row, b.entries, group)
		if group == GroupAgent {
			agent := *s.f.agents[key]
			b.row.Agent = &agent
		}
		rows = append(rows, b.row)
	}
	slices.SortFunc(rows, func(a, c Row) int { return compareRows(a, c, sortBy, desc) })
	return rows
}

func (s *Selection) rowIdentity(e *entry, group Group) (key, label string) {
	if group == GroupAgent {
		return e.agentID, s.f.agents[e.agentID].Title
	}
	if e.model == "" {
		return UnknownModelKey, "Unknown"
	}
	return e.model, s.f.models[e.model].Model
}

// describeModels fills the model columns of a row from the entries behind it.
func (s *Selection) describeModels(row *Row, entries []*entry, group Group) {
	keys := map[string]bool{}
	sources := map[string]bool{}
	for _, e := range entries {
		keys[modelKeyOrUnknown(e)] = true
		if e.model != "" {
			sources[e.modelSource] = true
		}
	}
	switch len(sources) {
	case 0:
	case 1:
		row.ModelSource = firstKey(sources)
	default:
		row.ModelSource = ModelSourceMixed
	}
	if group == GroupModel {
		if row.Key != UnknownModelKey {
			ref := *s.f.models[row.Key]
			row.Model = &ref
		}
		agents := map[string]bool{}
		for _, e := range entries {
			agents[e.agentID] = true
		}
		row.Agents = len(agents)
		return
	}
	switch {
	case len(keys) == 1 && keys[UnknownModelKey]:
		row.ModelState = ModelStateUnknown
	case len(keys) == 1:
		row.ModelState = ModelStateSingle
		ref := *s.f.models[firstKey(keys)]
		row.Model = &ref
	default:
		row.ModelState = ModelStateMixed
	}
}

func firstKey(m map[string]bool) string {
	for k := range m {
		return k
	}
	return ""
}

// Jobs lists the selected jobs, the Assistant's included, in a stable order.
func (s *Selection) Jobs(sortBy SortKey, desc bool) []JobItem {
	items := make([]JobItem, 0, len(s.entries))
	for _, e := range s.entries {
		if e.kind == entryJob {
			items = append(items, s.jobItem(e))
		}
	}
	slices.SortFunc(items, func(a, c JobItem) int { return compareJobs(a, c, sortBy, desc) })
	return items
}

func (s *Selection) jobItem(e *entry) JobItem {
	acc := accumulator{asOf: s.asOf}
	acc.add(e)
	m := acc.result()
	var offset *int64
	if e.offsetMS != nil {
		v := *e.offsetMS
		offset = &v
	}
	item := JobItem{
		ID:          e.id,
		Title:       e.title,
		Agent:       *s.f.agents[e.agentID],
		StageID:     e.stageID,
		ModelSource: e.modelSource,
		Status:      e.status,
		StartedAt:   e.at,
		RunOffsetMS: offset,
		DurationMS:  e.durationAt(s.asOf),
		Tokens:      m.Tokens,
		Money:       m.Money,
	}
	if e.model != "" {
		ref := *s.f.models[e.model]
		item.Model = &ref
	}
	return item
}

// Timeline draws the placed jobs oldest first and keeps at most limit of them.
func (s *Selection) Timeline(limit int) Timeline {
	tl := Timeline{Limit: limit, Intervals: []Interval{}}
	var placed []*entry
	for _, e := range s.entries {
		if e.kind != entryJob {
			continue
		}
		if e.offsetMS == nil || e.durationAt(s.asOf) == nil {
			tl.Unplaced++
			continue
		}
		placed = append(placed, e)
	}
	slices.SortFunc(placed, func(a, c *entry) int {
		if d := *a.offsetMS - *c.offsetMS; d != 0 {
			return int(d)
		}
		return compareStartThenID(a.at, a.id, c.at, c.id)
	})
	tl.Total = len(placed)
	if len(placed) > limit {
		placed, tl.Truncated = placed[:limit], true
	}
	for i, e := range placed {
		iv := Interval{
			JobID: e.id, Title: e.title, AgentID: e.agentID, ModelKey: e.model, StageID: e.stageID,
			Status: e.status, RunOffsetMS: *e.offsetMS, DurationMS: *e.durationAt(s.asOf),
		}
		if i == 0 || iv.RunOffsetMS < tl.RangeStartMS {
			tl.RangeStartMS = iv.RunOffsetMS
		}
		tl.RangeEndMS = max(tl.RangeEndMS, iv.RunOffsetMS+iv.DurationMS)
		tl.Intervals = append(tl.Intervals, iv)
	}
	return tl
}

// Machine reasons why a figure is not complete; journal gaps use the agentjobs codes.
const (
	ReasonInterruptedJob   = "interrupted_job"
	ReasonStartFailed      = "start_failed"
	ReasonUnmeteredJob     = "unmetered_job"
	ReasonPendingUsage     = "pending_usage"
	ReasonUnpricedModel    = "unpriced_model"
	ReasonUsageUnavailable = "usage_store_unavailable"
	ReasonUsageDisabled    = "usage_disabled"
	ReasonActiveJobs       = "active_jobs"
	ReasonPendingStart     = "pending_start"
)

// Completeness judges the selection without the agents outside the run total, like Totals.
func (s *Selection) Completeness() Completeness {
	inside, _ := s.split()
	m := s.metricsOf(inside)

	var startFailed int
	for _, e := range inside {
		if e.kind == entryStartFailed {
			startFailed++
		}
	}

	timeReasons := s.f.journalReasons(s.stagesInScope())
	if m.Time.Unknown > 0 {
		timeReasons = append(timeReasons, ReasonInterruptedJob)
	}
	if startFailed > 0 {
		timeReasons = append(timeReasons, ReasonStartFailed)
	}

	tokenReasons := s.f.usageReasons()
	if m.Tokens.Unmetered > 0 {
		tokenReasons = append(tokenReasons, ReasonUnmeteredJob)
	}
	if m.Tokens.Pending > 0 {
		tokenReasons = append(tokenReasons, ReasonPendingUsage)
	}
	priceReasons := slices.Clone(tokenReasons)
	if m.Money.Unpriced > 0 {
		priceReasons = append(priceReasons, ReasonUnpricedModel)
	}

	return Completeness{
		Time:   knownCount(m.Time.Measured, m.Time.Unknown, timeReasons),
		Tokens: knownCount(m.Tokens.Metered, m.Tokens.Unmetered+m.Tokens.Pending, tokenReasons),
		Price:  knownCount(m.Money.Priced, m.Money.Unpriced+m.Tokens.Unmetered+m.Tokens.Pending, priceReasons),
	}
}

// stagesInScope are the stages whose start can predate the journal for this selection.
func (s *Selection) stagesInScope() []StageMeta {
	if s.scope.Scope == FilterStage {
		if st, ok := s.f.stageByID[s.scope.StageID]; ok {
			return []StageMeta{st}
		}
		return nil
	}
	return s.f.stages
}

// knownCount judges a measurement: complete when nothing is unknown and nothing is unproven, unknown when
// nothing is known, partial in between. A run that was recorded and has nothing yet is a proven zero.
func knownCount(known, unknown int, reasons []string) KnownCount {
	switch {
	case unknown == 0 && len(reasons) == 0:
		return KnownCount{Status: CompletenessFull, Known: known, Reasons: reasons}
	case known == 0:
		return KnownCount{Status: CompletenessUnknown, Unknown: unknown, Reasons: reasons}
	default:
		return KnownCount{Status: CompletenessPartial, Known: known, Unknown: unknown, Reasons: reasons}
	}
}

// journalReasons lists why the recorded time cannot be taken as the whole picture.
func (f *Facts) journalReasons(stages []StageMeta) []string {
	var reasons []string
	switch {
	case f.legacyJournal():
		return []string{agentjobs.GapLegacyJournal}
	case f.journal.Health.Status == agentjobs.HealthUnavailable:
		reasons = append(reasons, agentjobs.GapJobsStoreUnavailable)
	case f.journal.Health.Status == agentjobs.HealthDegraded:
		reasons = append(reasons, f.journal.Health.Reasons...)
	default:
		// A healthy journal adds no reason of its own.
	}
	for _, st := range stages {
		if slices.Contains(f.coverageGaps(st), agentjobs.GapStartedBeforeRecording) {
			reasons = append(reasons, agentjobs.GapStartedBeforeRecording)
			break
		}
	}
	return reasons
}

func (f *Facts) usageReasons() []string {
	switch f.usageState {
	case UsageStateUnavailable:
		return []string{ReasonUsageUnavailable}
	case UsageStateDisabled:
		return []string{ReasonUsageDisabled}
	default:
		return nil
	}
}
