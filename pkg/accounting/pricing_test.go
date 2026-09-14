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
