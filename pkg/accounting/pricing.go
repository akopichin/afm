package accounting

// RateCard is a (possibly partial) set of category rates. Pointer fields
// distinguish an unset category from an explicit zero (which disables that
// category's cost). CacheWrite is the fallback for a TTL with no specific rate.
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

// card builds a builtin RateCard. The generic CacheWrite is set to the same
// value as the 1h write rate: it's the fallback used both for a TTL bucket
// with no rate of its own (see cacheWriteRate) AND for CacheWriteOther — the
// bucket normalize.go fills with ALL cache-creation tokens when an Anthropic
// usage line doesn't break them down by TTL at all. Without this, a
// fully-priced builtin model with only CacheWriteOther tokens (no explicit
// 5m/1h split) would fail ResolvedRate.missingCategory and get flipped to
// unpriced — the opposite of what a "complete builtin card" should do.
func card(input, cacheRead, write5m, write1h, output Rate) RateCard {
	cacheWrite := write1h
	return RateCard{Input: &input, CacheRead: &cacheRead, CacheWrite: &cacheWrite, CacheWrite5m: &write5m, CacheWrite1h: &write1h, Output: &output}
}

type Resolver struct{ cfg PricingConfig }

func NewResolver(cfg PricingConfig) *Resolver { return &Resolver{cfg: cfg} }

// Resolve applies precedence: config channel/model → config model → builtin
// channel/model → builtin model. A config card is MERGED onto the builtin
// card for the same key (per-category: config wins, builtin fills any gap)
// when a builtin exists for that key — a partial override no longer silently
// zeroes the categories the user didn't mention. Returns ok=false (unpriced)
// only when nothing matches at all; a resolved-but-incomplete card (no
// builtin to fall back to, and still missing a category the caller needs) is
// caught later by ResolvedRate.missingCategory, not here.
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

// mergeWithBuiltin merges a config-supplied card onto the builtin card for
// the same key: per category, the config's *Rate wins if set, else the
// builtin's is used. If there is no builtin for this key (a user-defined /
// unknown model), the config card is returned verbatim — there's nothing to
// merge onto. Source stays "config" either way (a merged card is still
// config-sourced, just filled out); asOf is taken from the builtin when one
// was used to fill gaps, since that's the provenance of any builtin value
// the resulting cost figure might rely on.
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

// missingCategory reports whether any token category actually present in t
// (count > 0) has no rate to price it with, respecting the same fallbacks
// cost() uses: CacheWrite5m/1h fall back to the generic CacheWrite rate, and
// CacheWriteOther is priced ONLY by the generic CacheWrite rate (it has no
// TTL-specific fallback of its own). A card missing a category the
// observation never actually used is fine — only ENCOUNTERED categories must
// be covered for the record to be considered fully priced.
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
