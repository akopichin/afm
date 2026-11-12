package budget

import "math"

const nanoPerUSD = 1e9

// Spend is the accumulated usage of one scope. Money is integer nano-USD so sums do not depend on order.
// Counters say how many records could not contribute to a figure, so "unknown" is never silently zero.
type Spend struct {
	Tokens       uint64 `json:"tokens"`
	MoneyNanoUSD int64  `json:"money_nano_usd"`
	Records      int    `json:"records"`
	Unmetered    int    `json:"unmetered,omitempty"`
	Unpriced     int    `json:"unpriced,omitempty"`
	NotExplicit  int    `json:"not_explicit,omitempty"`
}

// Add returns the sum of two spends; money saturates so an overflow still reads as "limit reached".
func (s Spend) Add(o Spend) Spend {
	return Spend{
		Tokens:       addSaturatedUint(s.Tokens, o.Tokens),
		MoneyNanoUSD: addSaturated(s.MoneyNanoUSD, o.MoneyNanoUSD),
		Records:      s.Records + o.Records,
		Unmetered:    s.Unmetered + o.Unmetered,
		Unpriced:     s.Unpriced + o.Unpriced,
		NotExplicit:  s.NotExplicit + o.NotExplicit,
	}
}

// MoneyUSD converts the nano-USD sum for display.
func (s Spend) MoneyUSD() float64 { return float64(s.MoneyNanoUSD) / nanoPerUSD }

// TokensExact reports that no record lacked token counts.
func (s Spend) TokensExact() bool { return s.Unmetered == 0 }

// MoneyExact reports that every record was metered, priced and priced from explicit rates.
func (s Spend) MoneyExact() bool { return s.Unmetered == 0 && s.Unpriced == 0 && s.NotExplicit == 0 }

// MoneyNanoUSD converts USD to integer nano-USD; negative and non-finite input counts as zero.
func MoneyNanoUSD(usd float64) int64 {
	if math.IsNaN(usd) || usd <= 0 {
		return 0
	}
	n := math.Round(usd * nanoPerUSD)
	if n >= math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(n)
}

// Level is how close a metric is to its limit.
type Level string

const (
	LevelNone    Level = ""
	LevelOK      Level = "ok"
	LevelWarning Level = "warning"
	LevelReached Level = "reached"
)

// Metric names a limited quantity.
type Metric string

const (
	MetricTokens Metric = "tokens"
	MetricMoney  Metric = "money"
)

// Evaluation is the per-metric level against a limit; a metric without a limit has LevelNone.
type Evaluation struct {
	Tokens Level `json:"tokens,omitempty"`
	Money  Level `json:"money,omitempty"`
}

// Worst returns the highest level across metrics.
func (e Evaluation) Worst() Level {
	if e.Tokens == LevelReached || e.Money == LevelReached {
		return LevelReached
	}
	if e.Tokens == LevelWarning || e.Money == LevelWarning {
		return LevelWarning
	}
	if e.Tokens == LevelOK || e.Money == LevelOK {
		return LevelOK
	}
	return LevelNone
}

// Evaluate compares spend with a limit: reached at spent >= limit, warning from 80% (spent >= limit - limit/5).
func Evaluate(limit Spec, spent Spend) Evaluation {
	var e Evaluation
	if limit.Tokens > 0 {
		e.Tokens = level(limit.Tokens, spent.Tokens)
	}
	if limit.MoneyUSD > 0 {
		e.Money = level(max(MoneyNanoUSD(limit.MoneyUSD), 1), max(spent.MoneyNanoUSD, 0))
	}
	return e
}

func level[T uint64 | int64](limit, spent T) Level {
	switch {
	case spent >= limit:
		return LevelReached
	case spent >= limit-limit/5:
		return LevelWarning
	}
	return LevelOK
}

func addSaturated(a, b int64) int64 {
	if b > 0 && a > math.MaxInt64-b {
		return math.MaxInt64
	}
	if b < 0 && a < math.MinInt64-b {
		return math.MinInt64
	}
	return a + b
}

func addSaturatedUint(a, b uint64) uint64 {
	if a > math.MaxUint64-b {
		return math.MaxUint64
	}
	return a + b
}
