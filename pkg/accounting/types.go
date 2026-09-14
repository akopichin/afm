// Package accounting owns all vendor-usage parsing, token normalization,
// pricing, money math, aggregation and formatting for afm. It imports no other
// afm package (config/state/server/flow depend on it, never the reverse).
package accounting

// Schema identifies the vendor token-count convention a raw usage blob follows.
type Schema string

const (
	SchemaAnthropic  Schema = "anthropic"   // claude stream-json result.usage
	SchemaOpenAI     Schema = "openai"      // Codex turn.completed.usage (inclusive input)
	SchemaOpenAIChat Schema = "openai_chat" // Chat Completions usage (inclusive prompt)
)

// Tokens is the normalized, provider-independent breakdown. All fields are
// disjoint EXCEPT ReasoningOutput, which is a subset of Output (never summed on
// top of it).
type Tokens struct {
	UncachedInput   uint64 `json:"uncached_input"`
	CacheRead       uint64 `json:"cache_read"`
	CacheWrite5m    uint64 `json:"cache_write_5m"`
	CacheWrite1h    uint64 `json:"cache_write_1h"`
	CacheWriteOther uint64 `json:"cache_write_other"`
	Output          uint64 `json:"output"`
	ReasoningOutput uint64 `json:"reasoning_output,omitempty"`
}

func (t Tokens) CacheWriteTotal() uint64 { return t.CacheWrite5m + t.CacheWrite1h + t.CacheWriteOther }
func (t Tokens) InputTotal() uint64      { return t.UncachedInput + t.CacheRead + t.CacheWriteTotal() }
func (t Tokens) Total() uint64           { return t.InputTotal() + t.Output }

func (t Tokens) CacheHitRatio() float64 {
	in := t.InputTotal()
	if in == 0 {
		return 0
	}
	return float64(t.CacheRead) / float64(in)
}

func (t Tokens) Add(o Tokens) Tokens {
	return Tokens{
		UncachedInput:   t.UncachedInput + o.UncachedInput,
		CacheRead:       t.CacheRead + o.CacheRead,
		CacheWrite5m:    t.CacheWrite5m + o.CacheWrite5m,
		CacheWrite1h:    t.CacheWrite1h + o.CacheWrite1h,
		CacheWriteOther: t.CacheWriteOther + o.CacheWriteOther,
		Output:          t.Output + o.Output,
		ReasoningOutput: t.ReasoningOutput + o.ReasoningOutput,
	}
}

// UsageHint is a non-authoritative channel/model hint from the wrapper env or
// recipe; a model id discovered in the stream always wins over it.
type UsageHint struct {
	Channel string
	Model   string
}
