package accounting

import "testing"

func ptrU(v uint64) *uint64 { return &v }

func TestNormalizeAnthropic(t *testing.T) {
	raw := rawUsage{
		InputTokens:              2,
		CacheReadInputTokens:     15940,
		CacheCreationInputTokens: 18551,
		Ephemeral1hInputTokens:   ptrU(18551),
		Ephemeral5mInputTokens:   ptrU(0),
		OutputTokens:             345,
	}
	tk, warn := normalize(SchemaAnthropic, raw)
	if tk.UncachedInput != 2 || tk.CacheRead != 15940 || tk.CacheWrite1h != 18551 || tk.CacheWrite5m != 0 || tk.CacheWriteOther != 0 || tk.Output != 345 {
		t.Fatalf("tokens = %+v", tk)
	}
	if len(warn) != 0 {
		t.Fatalf("warnings = %v", warn)
	}
}

func TestNormalizeOpenAIInclusive(t *testing.T) {
	// Codex: input_tokens is inclusive of cached; uncached = input - cached - write
	raw := rawUsage{InputTokens: 22005, CachedInputTokens: ptrU(10752), CacheWriteInputTokens: ptrU(0), OutputTokens: 8, ReasoningOutputTokens: ptrU(0)}
	tk, warn := normalize(SchemaOpenAI, raw)
	if tk.UncachedInput != 22005-10752 || tk.CacheRead != 10752 || tk.Output != 8 {
		t.Fatalf("tokens = %+v", tk)
	}
	if len(warn) != 0 {
		t.Fatalf("warnings = %v", warn)
	}
}

func TestNormalizeOpenAIUnderflowClamps(t *testing.T) {
	raw := rawUsage{InputTokens: 100, CachedInputTokens: ptrU(150), OutputTokens: 1}
	tk, warn := normalize(SchemaOpenAI, raw)
	if tk.UncachedInput != 0 {
		t.Fatalf("expected clamp to 0, got %d", tk.UncachedInput)
	}
	if len(warn) == 0 {
		t.Fatal("expected an underflow warning")
	}
}
