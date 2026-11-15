// Package usageview is the wire form of the accounting read model shared by the HTTP API and the
// offline report, so both show the same fields and apply the same display-mode hiding.
package usageview

// Everywhere null means unknown and 0 means measured zero. Money and tokens are optional objects that
// ForMode drops, so a mode that hides them has no such key at any depth.

// Mode is the display mode computed from accounting.enabled and show_money.
type Mode string

const (
	ModeFull     Mode = "full"
	ModeNoMoney  Mode = "no_money"
	ModeTimeOnly Mode = "time_only"
)

// ModeFor maps the accounting display switches to the mode. Hiding accounting leaves the
// agent time, which is measured regardless of accounting.
func ModeFor(accountingEnabled, showMoney bool) Mode {
	switch {
	case !accountingEnabled:
		return ModeTimeOnly
	case showMoney:
		return ModeFull
	default:
		return ModeNoMoney
	}
}

func (m Mode) HasMoney() bool  { return m == ModeFull }
func (m Mode) HasTokens() bool { return m != ModeTimeOnly }

// Metric is a quantity the client can compare and sort by.
type Metric string

const (
	MetricCost      Metric = "cost"
	MetricAgentTime Metric = "agent_time"
	MetricTokens    Metric = "tokens"
)

// Metrics lists the metrics available in the mode, in the order the client shows them.
func (m Mode) Metrics() []Metric {
	metrics := make([]Metric, 0, 3)
	if m.HasMoney() {
		metrics = append(metrics, MetricCost)
	}
	metrics = append(metrics, MetricAgentTime)
	if m.HasTokens() {
		metrics = append(metrics, MetricTokens)
	}
	return metrics
}

// Sort is a key rows or jobs are ordered by.
type Sort string

const (
	SortCost    Sort = "cost"
	SortTokens  Sort = "tokens"
	SortTime    Sort = "time" // agent time in breakdown rows, duration in jobs
	SortJobs    Sort = "jobs"
	SortName    Sort = "name"
	SortModel   Sort = "model"
	SortStarted Sort = "started"
)

// AllowsSort reports whether the mode has the data the sort key orders by; the endpoint answers
// 400 for a sort the mode cannot serve.
func (m Mode) AllowsSort(sort Sort) bool {
	switch sort {
	case SortCost:
		return m.HasMoney()
	case SortTokens:
		return m.HasTokens()
	default:
		return true
	}
}

// OrClosed keeps an unset or unknown mode from exposing more than the least permissive one.
func (m Mode) OrClosed() Mode {
	switch m {
	case ModeFull, ModeNoMoney, ModeTimeOnly:
		return m
	default:
		return ModeTimeOnly
	}
}
