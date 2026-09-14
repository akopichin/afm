package accounting

import "testing"

func TestResolvePrecedenceAndCost(t *testing.T) {
	r := NewResolver(PricingConfig{}) // builtins only
	rr, ok := r.Resolve("", "claude-opus-4-8")
	if !ok {
		t.Fatal("opus should be priced by builtin")
	}
	got := rr.cost(Tokens{UncachedInput: 2, CacheRead: 15940, CacheWrite1h: 18551, Output: 345})
	if diff := got - 0.202115; diff > 1e-6 || diff < -1e-6 {
		t.Fatalf("opus cost = %.6f want 0.202115", got)
	}
}

func TestResolveGLMBuiltin(t *testing.T) {
	r := NewResolver(PricingConfig{})
	rr, ok := r.Resolve("", "glm-5.3")
	if !ok {
		t.Fatal("glm should be priced")
	}
	got := rr.cost(Tokens{UncachedInput: 48583, CacheRead: 47616, Output: 1130})
	if diff := got - 0.08536836; diff > 1e-6 || diff < -1e-6 {
		t.Fatalf("glm cost = %.8f want 0.08536836", got)
	}
}

func TestResolveUnknownIsUnpriced(t *testing.T) {
	r := NewResolver(PricingConfig{})
	if _, ok := r.Resolve("", "totally-unknown"); ok {
		t.Fatal("unknown model must be unpriced")
	}
}

// TestResolvePartialOverrideMergesOntoBuiltin verifies FINDING 1: overriding
// only the input rate of a known builtin model must NOT zero out the
// categories the user didn't mention — cache_read/output must still use the
// builtin's own rates, and the resulting cost must reflect that merge (not
// treat the omitted categories as free).
func TestResolvePartialOverrideMergesOntoBuiltin(t *testing.T) {
	six := Rate(6.0)
	cfg := PricingConfig{Models: map[string]RateCard{
		"claude-opus-4-8": {Input: &six}, // only input overridden
	}}
	r := NewResolver(cfg)
	rr, ok := r.Resolve("", "claude-opus-4-8")
	if !ok {
		t.Fatal("partially-overridden builtin model should still resolve")
	}
	if rr.card.Input == nil || *rr.card.Input != six {
		t.Fatalf("expected overridden input rate 6.0, got %v", rr.card.Input)
	}
	// Same token shape as TestResolvePrecedenceAndCost, but now input is 6.0
	// (config) while cache_read/cache_write_1h/output stay at the builtin's
	// 0.50/10.00/25.00.
	tok := Tokens{UncachedInput: 2, CacheRead: 15940, CacheWrite1h: 18551, Output: 345}
	got := rr.cost(tok)
	want := costUSD(tok.UncachedInput, 6.0) +
		costUSD(tok.CacheRead, 0.50) +
		costUSD(tok.CacheWrite1h, 10.00) +
		costUSD(tok.Output, 25.00)
	if diff := got - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("merged cost = %.9f want %.9f", got, want)
	}
}

// TestResolveUnknownModelPartialOverrideMissingCategory verifies FINDING 1's
// completeness rule: a config-only card (no builtin to fall back to) that
// omits a category the tokens actually use resolves ok=true (the key
// matched), but ResolvedRate.missingCategory must flag it so BuildRecord can
// mark the record unpriced rather than silently costing the omitted category
// at $0.
func TestResolveUnknownModelPartialOverrideMissingCategory(t *testing.T) {
	four := Rate(4.0)
	cfg := PricingConfig{Models: map[string]RateCard{
		"my-custom-model": {Input: &four}, // output rate never set, no builtin either
	}}
	r := NewResolver(cfg)
	rr, ok := r.Resolve("", "my-custom-model")
	if !ok {
		t.Fatal("config-only model should resolve")
	}
	if !rr.missingCategory(Tokens{UncachedInput: 10, Output: 5}) {
		t.Fatal("expected missingCategory=true: output tokens present but no output rate configured")
	}
	if rr.missingCategory(Tokens{UncachedInput: 10}) {
		t.Fatal("expected missingCategory=false: only input tokens present, and input is configured")
	}
}

func TestResolveChannelOverride(t *testing.T) {
	zero := Rate(0)
	four := Rate(4.0)
	twenty := Rate(20.0)
	cfg := PricingConfig{Channels: map[string]map[string]RateCard{
		"codex": {"gpt-5.6-sol": {Input: &four, CacheWrite: &zero, Output: &twenty}},
	}}
	r := NewResolver(cfg)
	if _, ok := r.Resolve("codex", "gpt-5.6-sol"); !ok {
		t.Fatal("channel override should resolve")
	}
}
