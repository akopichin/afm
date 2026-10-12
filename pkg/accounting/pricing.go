package accounting

// RateCard is a set of category rates. Pointer fields distinguish an unset category from an explicit zero
// (which disables that category's cost).
type RateCard struct {
	Input        *Rate `yaml:"input"`
	CacheRead    *Rate `yaml:"cache_read"`
	CacheWrite   *Rate `yaml:"cache_write"`
	CacheWrite5m *Rate `yaml:"cache_write_5m"`
	CacheWrite1h *Rate `yaml:"cache_write_1h"`
	Output       *Rate `yaml:"output"`
}

// PricingConfig is the config overlay (see pkg/config). Empty = builtins only.
type PricingConfig struct {
	Models   map[string]RateCard            `yaml:"models"`
	Channels map[string]map[string]RateCard `yaml:"channels"`
}

type ResolvedRate struct {
	key    string
	source string // "builtin" | "config"
	asOf   string
	card   RateCard // fully merged; every category needed by observed tokens must be set
}

// builtins is the immutable first-version rate table (USD / 1M tokens).
var builtins = map[string]RateCard{
	"claude-opus-4-8":   card(5.00, 0.50, 6.25, 10.00, 25.00),
	"glm-5.3":           card(1.40, 0.26, 0, 0, 4.40),
	"codex/gpt-5.6-sol": card(4.00, 0.40, 0, 0, 20.00),
}
var builtinAsOf = map[string]string{"claude-opus-4-8": "2026-09-14", "glm-5.3": "2026-09-14", "codex/gpt-5.6-sol": "2026-09-14"}

// card builds a builtin RateCard.
func card(input, cacheRead, write5m, write1h, output Rate) RateCard {
	cacheWrite := write1h
	return RateCard{Input: &input, CacheRead: &cacheRead, CacheWrite: &cacheWrite, CacheWrite5m: &write5m, CacheWrite1h: &write1h, Output: &output}
}

type Resolver struct{ cfg PricingConfig }

func NewResolver(cfg PricingConfig) *Resolver { return &Resolver{cfg: cfg} }

// Resolve applies precedence: config channel/model → config model → builtin channel/model → builtin model.
func (r *Resolver) Resolve(channel, model string) (ResolvedRate, bool) {
	chKey := channel + "/" + model
	if channel != "" {
		if c, ok := r.cfg.Channels[channel][model]; ok {
			return mergeWithBuiltin(chKey, c), true
		}
	}
	if c, ok := r.cfg.Models[model]; ok {
		return mergeWithBuiltin(model, c), true
	}
	if channel != "" {
		if c, ok := builtins[chKey]; ok {
			return ResolvedRate{key: chKey, source: "builtin", asOf: builtinAsOf[chKey], card: c}, true
		}
	}
	if c, ok := builtins[model]; ok {
		return ResolvedRate{key: model, source: "builtin", asOf: builtinAsOf[model], card: c}, true
	}
	return ResolvedRate{}, false
}

// mergeWithBuiltin merges a config-supplied card onto the builtin card for the same key: per category, the
// config's *Rate wins if set, else the builtin's is used.
func mergeWithBuiltin(key string, cfgCard RateCard) ResolvedRate {
	b, ok := builtins[key]
	if !ok {
		return ResolvedRate{key: key, source: "config", card: cfgCard}
	}
	merged := RateCard{
		Input:        firstNonNil(cfgCard.Input, b.Input),
		CacheRead:    firstNonNil(cfgCard.CacheRead, b.CacheRead),
		CacheWrite:   firstNonNil(cfgCard.CacheWrite, b.CacheWrite),
		CacheWrite5m: firstNonNil(cfgCard.CacheWrite5m, b.CacheWrite5m),
		CacheWrite1h: firstNonNil(cfgCard.CacheWrite1h, b.CacheWrite1h),
		Output:       firstNonNil(cfgCard.Output, b.Output),
	}
	return ResolvedRate{key: key, source: "config", asOf: builtinAsOf[key], card: merged}
}

// firstNonNil returns a if set, else b — used to let a config-supplied rate
// win over the builtin's for the same category.
func firstNonNil(a, b *Rate) *Rate {
	if a != nil {
		return a
	}
	return b
}

func rate(p *Rate) Rate {
	if p == nil {
		return 0
	}
	return *p
}

// cacheWriteRate picks the TTL-specific rate, falling back to CacheWrite.
func (rr ResolvedRate) cacheWriteRate(specific *Rate) Rate {
	if specific != nil {
		return *specific
	}
	return rate(rr.card.CacheWrite)
}

// missingCategory checks rates for used categories, with generic cache-write fallbacks; CacheWriteOther
// needs a generic rate.
func (rr ResolvedRate) missingCategory(t Tokens) bool {
	if t.UncachedInput > 0 && rr.card.Input == nil {
		return true
	}
	if t.CacheRead > 0 && rr.card.CacheRead == nil {
		return true
	}
	if t.CacheWrite5m > 0 && rr.card.CacheWrite5m == nil && rr.card.CacheWrite == nil {
		return true
	}
	if t.CacheWrite1h > 0 && rr.card.CacheWrite1h == nil && rr.card.CacheWrite == nil {
		return true
	}
	if t.CacheWriteOther > 0 && rr.card.CacheWrite == nil {
		return true
	}
	if t.Output > 0 && rr.card.Output == nil {
		return true
	}
	return false
}

func (rr ResolvedRate) cost(t Tokens) float64 {
	return costUSD(t.UncachedInput, rate(rr.card.Input)) +
		costUSD(t.CacheRead, rate(rr.card.CacheRead)) +
		costUSD(t.CacheWrite5m, rr.cacheWriteRate(rr.card.CacheWrite5m)) +
		costUSD(t.CacheWrite1h, rr.cacheWriteRate(rr.card.CacheWrite1h)) +
		costUSD(t.CacheWriteOther, rate(rr.card.CacheWrite)) +
		costUSD(t.Output, rate(rr.card.Output))
}
