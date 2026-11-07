package server

import "github.com/akopichin/afm/contracts/accounting"

// Mapping of the accounting read model to the wire types. It always fills tokens and money; ForMode
// removes what the display mode must not show, so no field is dropped here by accident.

func usageHealth(h accounting.AnalyticsHealth) UsageHealth {
	return UsageHealth{Usage: string(h.Usage), Jobs: string(h.Jobs), StartFailedJobs: h.StartFailed}
}

func usageCoverage(k accounting.KnownCount) UsageCoverage {
	return UsageCoverage{Status: string(k.Status), Known: k.Known, Unknown: k.Unknown, Reasons: k.Reasons}
}

func usageCompleteness(c accounting.Completeness) UsageCompleteness {
	tokens, price := usageCoverage(c.Tokens), usageCoverage(c.Price)
	return UsageCompleteness{Time: usageCoverage(c.Time), Tokens: &tokens, Price: &price}
}

func usageTokens(t accounting.TokenMetrics) *UsageTokens {
	out := &UsageTokens{MeteredJobs: t.Metered, UnmeteredJobs: t.Unmetered, PendingJobs: t.Pending}
	if c := t.Counts; c != nil {
		out.Counts = &UsageTokenCounts{
			UncachedInput: c.UncachedInput, CacheRead: c.CacheRead, CacheWrite5m: c.CacheWrite5m,
			CacheWrite1h: c.CacheWrite1h, CacheWriteOther: c.CacheWriteOther, Output: c.Output,
			ReasoningOutput: c.ReasoningOutput, Total: c.Total(),
		}
	}
	return out
}

func usageMoney(m accounting.MoneyMetrics) *UsageMoney {
	return &UsageMoney{EstimatedCostUSD: m.EstimatedCostUSD, PricedJobs: m.Priced, UnpricedJobs: m.Unpriced}
}

func usageMetrics(m accounting.Metrics) UsageMetrics {
	return UsageMetrics{
		Jobs:       m.Jobs,
		ActiveJobs: m.ActiveJobs,
		AgentTime:  UsageTime{DurationMS: m.Time.DurationMS, MeasuredJobs: m.Time.Measured, UnknownJobs: m.Time.Unknown},
		UsageSpend: UsageSpend{Tokens: usageTokens(m.Tokens), Money: usageMoney(m.Money)},
	}
}

func usageAgent(a accounting.AgentRef) UsageAgent {
	return UsageAgent{ID: a.ID, Kind: a.Kind, Title: a.Title, OutsideRunTotal: a.OutsideRunTotal}
}

func usageModel(m *accounting.ModelRef) *UsageModelRef {
	if m == nil {
		return nil
	}
	ordinal := m.ColorOrdinal
	return &UsageModelRef{Key: m.Key, Channel: m.Channel, Model: m.Model, ColorOrdinal: &ordinal}
}

func usageRows(rows []accounting.Row) []UsageRow {
	out := make([]UsageRow, 0, len(rows))
	for _, row := range rows {
		item := UsageRow{
			Key: row.Key, Label: row.Label, Model: usageModel(row.Model),
			ModelState: row.ModelState, ModelSource: row.ModelSource, Agents: row.Agents, UsageMetrics: usageMetrics(row.Metrics),
		}
		if row.Agent != nil {
			agent := usageAgent(*row.Agent)
			item.Agent = &agent
		}
		out = append(out, item)
	}
	return out
}

func usageJob(j accounting.JobItem) UsageJob {
	return UsageJob{
		ID: j.ID, Title: j.Title, Agent: usageAgent(j.Agent), StageID: j.StageID, Model: usageModel(j.Model),
		ModelSource: j.ModelSource, Status: j.Status, StartedAt: j.StartedAt, RunOffsetMS: j.RunOffsetMS,
		DurationMS: j.DurationMS, UsageSpend: UsageSpend{Tokens: usageTokens(j.Tokens), Money: usageMoney(j.Money)},
	}
}

func usageTimeline(tl accounting.Timeline) UsageTimeline {
	out := UsageTimeline{
		RangeStartMS: tl.RangeStartMS, RangeEndMS: tl.RangeEndMS, Total: tl.Total, Limit: tl.Limit,
		Truncated: tl.Truncated, Unplaced: tl.Unplaced, Intervals: make([]UsageInterval, 0, len(tl.Intervals)),
	}
	for _, iv := range tl.Intervals {
		out.Intervals = append(out.Intervals, UsageInterval{
			JobID: iv.JobID, Title: iv.Title, AgentID: iv.AgentID, ModelKey: iv.ModelKey, StageID: iv.StageID,
			Status: iv.Status, RunOffsetMS: iv.RunOffsetMS, DurationMS: iv.DurationMS,
		})
	}
	return out
}

func usageCatalog(c accounting.Catalog) UsageCatalog {
	out := UsageCatalog{
		Stages: make([]UsageStageRef, 0, len(c.Stages)),
		Agents: make([]UsageAgent, 0, len(c.Agents)),
		Models: make([]UsageModelRef, 0, len(c.Models)),
		Scopes: UsageScopes{RunWide: c.Scopes.RunWide, Unattributed: c.Scopes.Unattributed},
	}
	for _, s := range c.Stages {
		out.Stages = append(out.Stages, UsageStageRef{ID: s.ID, Title: s.Title})
	}
	for _, a := range c.Agents {
		out.Agents = append(out.Agents, usageAgent(a))
	}
	for i := range c.Models {
		out.Models = append(out.Models, *usageModel(&c.Models[i]))
	}
	return out
}
