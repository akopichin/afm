package usageview

import "github.com/akopichin/afm/contracts/accounting"

// Mapping of the accounting read model to the wire types. It always fills tokens and money; ForMode
// removes what the display mode must not show, so no field is dropped here by accident.

func NewHealth(h accounting.AnalyticsHealth) Health {
	return Health{Usage: string(h.Usage), Jobs: string(h.Jobs), StartFailedJobs: h.StartFailed}
}

func NewCoverage(k accounting.KnownCount) Coverage {
	return Coverage{Status: string(k.Status), Known: k.Known, Unknown: k.Unknown, Reasons: k.Reasons}
}

func NewCompleteness(c accounting.Completeness) Completeness {
	tokens, price := NewCoverage(c.Tokens), NewCoverage(c.Price)
	return Completeness{Time: NewCoverage(c.Time), Tokens: &tokens, Price: &price}
}

func NewTokens(t accounting.TokenMetrics) *Tokens {
	out := &Tokens{MeteredJobs: t.Metered, UnmeteredJobs: t.Unmetered, PendingJobs: t.Pending}
	if c := t.Counts; c != nil {
		out.Counts = &TokenCounts{
			UncachedInput: c.UncachedInput, CacheRead: c.CacheRead, CacheWrite5m: c.CacheWrite5m,
			CacheWrite1h: c.CacheWrite1h, CacheWriteOther: c.CacheWriteOther, Output: c.Output,
			ReasoningOutput: c.ReasoningOutput, Total: c.Total(),
		}
	}
	return out
}

func NewMoney(m accounting.MoneyMetrics) *Money {
	return &Money{EstimatedCostUSD: m.EstimatedCostUSD, PricedJobs: m.Priced, UnpricedJobs: m.Unpriced}
}

func NewMetrics(m accounting.Metrics) Metrics {
	return Metrics{
		Jobs:       m.Jobs,
		ActiveJobs: m.ActiveJobs,
		AgentTime:  Time{DurationMS: m.Time.DurationMS, MeasuredJobs: m.Time.Measured, UnknownJobs: m.Time.Unknown},
		Spend:      Spend{Tokens: NewTokens(m.Tokens), Money: NewMoney(m.Money)},
	}
}

func NewAgent(a accounting.AgentRef) Agent {
	return Agent{ID: a.ID, Kind: a.Kind, Title: a.Title, OutsideRunTotal: a.OutsideRunTotal}
}

func NewModel(m *accounting.ModelRef) *ModelRef {
	if m == nil {
		return nil
	}
	ordinal := m.ColorOrdinal
	return &ModelRef{Key: m.Key, Channel: m.Channel, Model: m.Model, ColorOrdinal: &ordinal}
}

func NewRows(rows []accounting.Row) []Row {
	out := make([]Row, 0, len(rows))
	for _, row := range rows {
		item := Row{
			Key: row.Key, Label: row.Label, Model: NewModel(row.Model),
			ModelState: row.ModelState, ModelSource: row.ModelSource, Agents: row.Agents, Metrics: NewMetrics(row.Metrics),
		}
		if row.Agent != nil {
			agent := NewAgent(*row.Agent)
			item.Agent = &agent
		}
		out = append(out, item)
	}
	return out
}

func NewJob(j accounting.JobItem) Job {
	return Job{
		ID: j.ID, Title: j.Title, Agent: NewAgent(j.Agent), StageID: j.StageID, Model: NewModel(j.Model),
		ModelSource: j.ModelSource, Status: j.Status, StartedAt: j.StartedAt, RunOffsetMS: j.RunOffsetMS,
		DurationMS: j.DurationMS, Spend: Spend{Tokens: NewTokens(j.Tokens), Money: NewMoney(j.Money)},
	}
}

func NewTimeline(tl accounting.Timeline) Timeline {
	out := Timeline{
		RangeStartMS: tl.RangeStartMS, RangeEndMS: tl.RangeEndMS, Total: tl.Total, Limit: tl.Limit,
		Truncated: tl.Truncated, Unplaced: tl.Unplaced, Intervals: make([]Interval, 0, len(tl.Intervals)),
	}
	for _, iv := range tl.Intervals {
		out.Intervals = append(out.Intervals, Interval{
			JobID: iv.JobID, Title: iv.Title, AgentID: iv.AgentID, ModelKey: iv.ModelKey, StageID: iv.StageID,
			Status: iv.Status, RunOffsetMS: iv.RunOffsetMS, DurationMS: iv.DurationMS,
		})
	}
	return out
}

func NewCatalog(c accounting.Catalog) Catalog {
	out := Catalog{
		Stages: make([]StageRef, 0, len(c.Stages)),
		Agents: make([]Agent, 0, len(c.Agents)),
		Models: make([]ModelRef, 0, len(c.Models)),
		Scopes: Scopes{RunWide: c.Scopes.RunWide, Unattributed: c.Scopes.Unattributed},
	}
	for _, s := range c.Stages {
		out.Stages = append(out.Stages, StageRef{ID: s.ID, Title: s.Title})
	}
	for _, a := range c.Agents {
		out.Agents = append(out.Agents, NewAgent(a))
	}
	for i := range c.Models {
		out.Models = append(out.Models, *NewModel(&c.Models[i]))
	}
	return out
}
