package accounting

import "fmt"

// rawUsage is the union of vendor usage fields we read. Pointers distinguish
// "absent" from "zero" for fields whose absence changes normalization.
type rawUsage struct {
	// Anthropic
	InputTokens              uint64  `json:"input_tokens"`
	CacheReadInputTokens     uint64  `json:"cache_read_input_tokens"`
	CacheCreationInputTokens uint64  `json:"cache_creation_input_tokens"`
	Ephemeral5mInputTokens   *uint64 `json:"ephemeral_5m_input_tokens"`
	Ephemeral1hInputTokens   *uint64 `json:"ephemeral_1h_input_tokens"`
	// OpenAI / Codex (input inclusive of cached)
	CachedInputTokens     *uint64 `json:"cached_input_tokens"`
	CacheWriteInputTokens *uint64 `json:"cache_write_input_tokens"`
	ReasoningOutputTokens *uint64 `json:"reasoning_output_tokens"`
	// OpenAI Chat Completions
	PromptTokens       uint64  `json:"prompt_tokens"`
	CompletionTokens   uint64  `json:"completion_tokens"`
	PromptCachedTokens *uint64 `json:"prompt_cached_tokens"`
	// Shared
	OutputTokens uint64 `json:"output_tokens"`
}

func deref(p *uint64) uint64 {
	if p == nil {
		return 0
	}
	return *p
}

// normalize converts a raw vendor usage blob into normalized Tokens plus any
// warnings (e.g. inclusive-input underflow). It never returns an error: bad
// counters clamp to zero and surface as warnings.
func normalize(schema Schema, r rawUsage) (Tokens, []string) {
	var warn []string
	switch schema {
	case SchemaAnthropic:
		// input_tokens is already uncached; cache read/creation are separate.
		w1h := deref(r.Ephemeral1hInputTokens)
		w5m := deref(r.Ephemeral5mInputTokens)
		other := uint64(0)
		if r.CacheCreationInputTokens > w1h+w5m {
			other = r.CacheCreationInputTokens - w1h - w5m
		}
		return Tokens{
			UncachedInput:   r.InputTokens,
			CacheRead:       r.CacheReadInputTokens,
			CacheWrite5m:    w5m,
			CacheWrite1h:    w1h,
			CacheWriteOther: other,
			Output:          r.OutputTokens,
			ReasoningOutput: deref(r.ReasoningOutputTokens),
		}, warn
	case SchemaOpenAI:
		cached := deref(r.CachedInputTokens)
		write := deref(r.CacheWriteInputTokens)
		uncached := int64(r.InputTokens) - int64(cached) - int64(write)
		if uncached < 0 {
			warn = append(warn, fmt.Sprintf("inclusive input underflow: input=%d cached=%d write=%d", r.InputTokens, cached, write))
			uncached = 0
		}
		return Tokens{
			UncachedInput:   uint64(uncached),
			CacheRead:       cached,
			CacheWriteOther: write,
			Output:          r.OutputTokens,
			ReasoningOutput: deref(r.ReasoningOutputTokens),
		}, warn
	case SchemaOpenAIChat:
		cached := deref(r.PromptCachedTokens)
		uncached := int64(r.PromptTokens) - int64(cached)
		if uncached < 0 {
			warn = append(warn, fmt.Sprintf("inclusive prompt underflow: prompt=%d cached=%d", r.PromptTokens, cached))
			uncached = 0
		}
		return Tokens{
			UncachedInput: uint64(uncached),
			CacheRead:     cached,
			Output:        r.CompletionTokens,
		}, warn
	default:
		return Tokens{}, []string{"unknown usage schema: " + string(schema)}
	}
}
