package accounting

import (
	"encoding/json"
	"reflect"
	"testing"
)

func ratePtr(v Rate) *Rate { return &v }

func fullCard() RateCard {
	return RateCard{Input: ratePtr(1), Output: ratePtr(2), CacheRead: ratePtr(0.1), CacheWrite: ratePtr(1.25)}
}

func TestCheckExplicitRates(t *testing.T) {
	zeroes := RateCard{Input: ratePtr(0), Output: ratePtr(0), CacheRead: ratePtr(0), CacheWrite: ratePtr(0)}
	tests := []struct {
		name string
		cfg  PricingConfig
		hint UsageHint
		want RateCheck
	}{
		{"builtin only", PricingConfig{}, UsageHint{Model: "claude-opus-4-8"},
			RateCheck{RateCheckMissing, "claude-opus-4-8", []string{"input", "output", "cache_read", "cache_write"}}},
		{"unknown model without card", PricingConfig{}, UsageHint{Model: "nope"},
			RateCheck{RateCheckMissing, "nope", []string{"input", "output", "cache_read", "cache_write"}}},
		{"partial config card is not completed from builtin",
			PricingConfig{Models: map[string]RateCard{"claude-opus-4-8": {Input: ratePtr(5), Output: ratePtr(25)}}},
			UsageHint{Model: "claude-opus-4-8"},
			RateCheck{RateCheckMissing, "claude-opus-4-8", []string{"cache_read", "cache_write"}}},
		{"no cache_write", PricingConfig{Models: map[string]RateCard{"m": {Input: ratePtr(1), Output: ratePtr(1), CacheRead: ratePtr(1)}}},
			UsageHint{Model: "m"}, RateCheck{RateCheckMissing, "m", []string{"cache_write"}}},
		{"explicit zero", PricingConfig{Models: map[string]RateCard{"m": zeroes}}, UsageHint{Model: "m"},
			RateCheck{Status: RateCheckComplete, Key: "m"}},
		{"builtin key with four fields", PricingConfig{Models: map[string]RateCard{"claude-opus-4-8": fullCard()}},
			UsageHint{Model: "claude-opus-4-8"}, RateCheck{Status: RateCheckComplete, Key: "claude-opus-4-8"}},
		{"channel card wins over model card",
			PricingConfig{
				Models:   map[string]RateCard{"m": fullCard()},
				Channels: map[string]map[string]RateCard{"ch": {"m": {Input: ratePtr(1)}}},
			},
			UsageHint{Channel: "ch", Model: "m"},
			RateCheck{RateCheckMissing, "ch/m", []string{"output", "cache_read", "cache_write"}}},
		{"empty model", PricingConfig{Models: map[string]RateCard{"m": fullCard()}}, UsageHint{},
			RateCheck{Status: RateCheckModelUnknown}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CheckExplicitRates(tc.cfg, tc.hint); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func observation(model string, tok Tokens) Observation {
	return Observation{Model: model, Metered: true, Tokens: tok}
}

func TestBuildRecord_ExplicitFlag(t *testing.T) {
	tok := Tokens{UncachedInput: 1000, Output: 1000}
	builtinRec := BuildRecord(NewResolver(PricingConfig{}), observation("claude-opus-4-8", tok), "s", "p", "")
	if builtinRec.Rate == nil || builtinRec.Rate.Explicit {
		t.Fatalf("builtin price must not be explicit: %+v", builtinRec.Rate)
	}
	cfg := PricingConfig{Models: map[string]RateCard{"m": fullCard()}}
	rec := BuildRecord(NewResolver(cfg), observation("m", tok), "s", "p", "")
	if rec.Rate == nil || !rec.Rate.Explicit {
		t.Fatalf("complete config card must be explicit: %+v", rec.Rate)
	}
}

func TestBuildRecord_ConfigCardCoversTTLViaGenericCacheWrite(t *testing.T) {
	cfg := PricingConfig{Models: map[string]RateCard{"m": fullCard()}}
	rec := BuildRecord(NewResolver(cfg), observation("m", Tokens{CacheWrite1h: 1_000_000}), "s", "p", "")
	if !rec.Priced || rec.EstimatedCostUSD != 1.25 {
		t.Fatalf("generic cache_write must price the TTL: priced=%v cost=%v", rec.Priced, rec.EstimatedCostUSD)
	}
}

func TestBuildRecord_BuiltinTTLRateKeptForBuiltinKey(t *testing.T) {
	c := fullCard()
	c.CacheWrite = ratePtr(7)
	cfg := PricingConfig{Models: map[string]RateCard{"claude-opus-4-8": c}}
	rec := BuildRecord(NewResolver(cfg), observation("claude-opus-4-8", Tokens{CacheWrite1h: 1_000_000}), "s", "p", "")
	if !rec.Rate.Explicit || rec.EstimatedCostUSD != 10 {
		t.Fatalf("1h tokens use builtin 10, not generic 7: explicit=%v cost=%v", rec.Rate.Explicit, rec.EstimatedCostUSD)
	}
}

func TestBuildRecord_ConfigTTLBeatsGenericCacheWrite(t *testing.T) {
	c := fullCard()
	c.CacheWrite1h = ratePtr(3)
	cfg := PricingConfig{Models: map[string]RateCard{"m": c}}
	rec := BuildRecord(NewResolver(cfg), observation("m", Tokens{CacheWrite1h: 1_000_000}), "s", "p", "")
	if rec.EstimatedCostUSD != 3 {
		t.Fatalf("config TTL rate must win: cost=%v", rec.EstimatedCostUSD)
	}
}

func TestRateSnapshot_OldLineReadsAsNotExplicit(t *testing.T) {
	var rec UsageRecord
	line := `{"record_version":1,"rate":{"key":"k","source":"config","input":1,"cache_read":1,"cache_write_5m":1,"cache_write_1h":1,"output":1}}`
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Rate.Explicit {
		t.Fatal("legacy record must read as not explicit")
	}
}

func TestBuildRecord_PartialConfigCardIsNotExplicit(t *testing.T) {
	cfg := PricingConfig{Models: map[string]RateCard{
		"claude-opus-4-8": {Input: ratePtr(5), Output: ratePtr(25)},
	}}
	rec := BuildRecord(NewResolver(cfg), observation("claude-opus-4-8", Tokens{UncachedInput: 1000, Output: 1000}), "s", "p", "")
	if !rec.Priced || rec.Rate == nil || rec.Rate.Source != "config" {
		t.Fatalf("partial card must still price from config: %+v", rec)
	}
	if rec.Rate.Explicit {
		t.Fatal("partial config card completed from builtin must not be explicit")
	}
	got := SpendByScope([]UsageRecord{rec}, nil)["run"]
	if got.NotExplicit != 1 || got.MoneyNanoUSD != 0 {
		t.Errorf("got %+v, want NotExplicit=1 and no money", got)
	}
}

func TestStore_ExplicitRateSurvivesReload(t *testing.T) {
	dir := t.TempDir()
	cfg := PricingConfig{Models: map[string]RateCard{"m": fullCard()}}
	s, err := Open(dir, NewResolver(cfg))
	if err != nil {
		t.Fatal(err)
	}
	obs := observation("m", Tokens{UncachedInput: 1_000_000, Output: 1_000_000})
	if err := s.Append(obs, "backend", "implementation", ""); err != nil {
		t.Fatal(err)
	}
	live := SpendByScope(s.Snapshot().Records(), nil)["run"]
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	l, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	recs := l.Records()
	if len(recs) != 1 || recs[0].Rate == nil || !recs[0].Rate.Explicit {
		t.Fatalf("reloaded record must stay explicit: %+v", recs)
	}
	reloaded := SpendByScope(recs, nil)["run"]
	if reloaded.MoneyNanoUSD == 0 || reloaded != live {
		t.Errorf("reloaded spend %+v, want the live spend %+v with money", reloaded, live)
	}
}
