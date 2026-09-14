package accounting

import (
	"encoding/json"
	"fmt"
)

// Reason codes for an unmetered Observation (Metered=false).
const (
	// ReasonMissingTerminal means the agent exited cleanly but no terminal
	// result line ever carried usage.
	ReasonMissingTerminal = "missing_terminal_usage"
	// ReasonInterrupted means the process was killed/errored before a
	// terminal result line arrived.
	ReasonInterrupted = "interrupted_before_usage"
	// ReasonUnsupported means a terminal result line was seen, but its
	// usage_contract_version or usage_schema isn't one this build knows.
	ReasonUnsupported = "unsupported_provider"
	// ReasonParse means a terminal result line was seen but its usage
	// envelope didn't parse into a recognizable shape.
	ReasonParse = "parse_error"
)

// usageContractVersion is the only synthetic adapter-envelope version this
// build understands (see docs/superpowers/plans/2026-09-14-stage-and-run-pricing.md).
const usageContractVersion = 1

// Observation is what a finished agent process reports to the accounting
// pipeline: normalized tokens plus enough provenance to price and audit them
// later. When Metered is false, Tokens is zero and Reason explains why no
// usable usage was found.
type Observation struct {
	Metered         bool
	Schema          Schema
	Channel         string
	Model           string
	Tokens          Tokens
	ReportedCostUSD *float64
	Reason          string
	Warnings        []string
}

// Collector reduces one agent process's stream-json lines, observed in
// order, to a single Observation. It is not safe for concurrent use.
type Collector struct {
	hint UsageHint

	// discoveredModel comes from `assistant` lines' message.model — the ONLY
	// thing those lines are used for. Their usage is never summed: the same
	// message.id repeats as a turn streams.
	discoveredModel string

	// terminal is true once a valid terminal result envelope was parsed;
	// later lines are ignored (only the first terminal result counts).
	terminal     bool
	schema       Schema
	channel      string
	model        string
	tokens       Tokens
	reportedCost *float64
	warnings     []string

	// failReason is set when a terminal `result` line was seen but rejected
	// (bad envelope / unsupported contract version or schema). It takes
	// precedence over Finish's generic interrupted/missing reasons — a known
	// diagnosis beats a guess from the exit code.
	failReason string
}

// NewCollector starts a collector seeded with a non-authoritative hint; a
// model/channel discovered in the stream always wins over it.
func NewCollector(hint UsageHint) *Collector {
	return &Collector{hint: hint}
}

// envelopeMessage carries just enough of an `assistant` line's message to
// discover the model in use; usage on this line is deliberately not modeled
// here, since it must never be summed.
type envelopeMessage struct {
	Model string `json:"model"`
}

// envelope is the union of fields read off any stream-json line: native
// claude (assistant/result) or an adapter's synthetic result.
type envelope struct {
	Type string `json:"type"`

	// assistant (native claude): model discovery only, usage is ignored.
	Message *envelopeMessage `json:"message"`

	// result, native claude: authoritative usage + vendor-reported cost.
	Usage        json.RawMessage `json:"usage"`
	TotalCostUSD *float64        `json:"total_cost_usd"`

	// result, adapter synthetic envelope (this contract; Task 7 emits it).
	UsageContractVersion *int              `json:"usage_contract_version"`
	UsageSchema          string            `json:"usage_schema"`
	Channel              string            `json:"channel"`
	Model                string            `json:"model"`
	UpstreamUsages       []json.RawMessage `json:"upstream_usages"`
}

// anthropicCacheCreation is the nested TTL breakdown under a claude usage
// object's cache_creation key.
type anthropicCacheCreation struct {
	Ephemeral5mInputTokens *uint64 `json:"ephemeral_5m_input_tokens"`
	Ephemeral1hInputTokens *uint64 `json:"ephemeral_1h_input_tokens"`
}

// anthropicUsageWire mirrors the actual wire shape of a claude stream-json
// usage object: the 5m/1h cache-write breakdown is nested under
// cache_creation, unlike the flat fields rawUsage exposes to normalize().
type anthropicUsageWire struct {
	InputTokens              uint64                  `json:"input_tokens"`
	CacheReadInputTokens     uint64                  `json:"cache_read_input_tokens"`
	CacheCreationInputTokens uint64                  `json:"cache_creation_input_tokens"`
	OutputTokens             uint64                  `json:"output_tokens"`
	CacheCreation            *anthropicCacheCreation `json:"cache_creation"`
}

func (w anthropicUsageWire) toRawUsage() rawUsage {
	r := rawUsage{
		InputTokens:              w.InputTokens,
		CacheReadInputTokens:     w.CacheReadInputTokens,
		CacheCreationInputTokens: w.CacheCreationInputTokens,
		OutputTokens:             w.OutputTokens,
	}
	if w.CacheCreation != nil {
		r.Ephemeral5mInputTokens = w.CacheCreation.Ephemeral5mInputTokens
		r.Ephemeral1hInputTokens = w.CacheCreation.Ephemeral1hInputTokens
	}
	return r
}

// Observe feeds one stream-json line. Lines that aren't JSON objects, aren't
// a recognized type, or arrive after a terminal result was already parsed
// are ignored.
func (c *Collector) Observe(line []byte) {
	if c.terminal {
		return
	}
	var env envelope
	if err := json.Unmarshal(line, &env); err != nil {
		return // not every line is JSON we care about (e.g. plain stdout)
	}
	switch env.Type {
	case "assistant":
		if env.Message != nil && env.Message.Model != "" {
			c.discoveredModel = env.Message.Model
		}
	case "result":
		c.observeResult(env)
	default:
		// system/stream_event/user/etc.: nothing this collector needs.
	}
}

func (c *Collector) observeResult(env envelope) {
	if env.UsageContractVersion != nil {
		c.observeSyntheticResult(env)
		return
	}
	c.observeNativeResult(env)
}

// observeNativeResult handles a real claude stream-json terminal result: the
// authoritative usage for the whole turn, plus the vendor's own cost figure
// (captured only as a diagnostic, never used as our cost).
func (c *Collector) observeNativeResult(env envelope) {
	if len(env.Usage) == 0 {
		c.failReason = ReasonParse
		return
	}
	var wire anthropicUsageWire
	if err := json.Unmarshal(env.Usage, &wire); err != nil {
		c.failReason = ReasonParse
		return
	}
	tokens, warn := normalize(SchemaAnthropic, wire.toRawUsage())
	c.commit(SchemaAnthropic, tokens, warn, env.TotalCostUSD)
}

// observeSyntheticResult handles the adapter contract this task defines:
// a terminal result naming its own schema/channel/model explicitly, with
// either a single `usage` blob or an `upstream_usages` array. Each raw usage
// is normalized independently and summed HERE — never by the shell adapter.
func (c *Collector) observeSyntheticResult(env envelope) {
	if *env.UsageContractVersion != usageContractVersion {
		c.failReason = ReasonUnsupported
		c.warnings = append(c.warnings, fmt.Sprintf("unsupported usage_contract_version: %d", *env.UsageContractVersion))
		return
	}
	schema := Schema(env.UsageSchema)
	if schema != SchemaOpenAI && schema != SchemaOpenAIChat && schema != SchemaAnthropic {
		c.failReason = ReasonUnsupported
		c.warnings = append(c.warnings, "unsupported usage_schema: "+env.UsageSchema)
		return
	}

	blobs := env.UpstreamUsages
	if len(blobs) == 0 && len(env.Usage) > 0 {
		blobs = []json.RawMessage{env.Usage}
	}
	if len(blobs) == 0 {
		c.failReason = ReasonParse
		return
	}

	var tokens Tokens
	var warn []string
	for _, blob := range blobs {
		var raw rawUsage
		if err := json.Unmarshal(blob, &raw); err != nil {
			c.failReason = ReasonParse
			return
		}
		tk, w := normalize(schema, raw)
		tokens = tokens.Add(tk)
		warn = append(warn, w...)
	}
	c.channel = env.Channel
	c.model = env.Model
	c.commit(schema, tokens, warn, nil)
}

// commit records a successfully parsed terminal result. reportedCost is the
// vendor's own figure (native claude only) — diagnostic, never our cost.
func (c *Collector) commit(schema Schema, tokens Tokens, warn []string, reportedCost *float64) {
	c.terminal = true
	c.schema = schema
	c.tokens = tokens
	c.reportedCost = reportedCost
	c.warnings = append(c.warnings, warn...)
}

// Finish closes the collector out. exitErr is the agent process's own exit
// error (nil on a clean exit) — it's consulted only when no terminal result
// was ever parsed, to distinguish "never showed up" from "got interrupted".
func (c *Collector) Finish(exitErr error) Observation {
	model := c.model
	if model == "" {
		model = c.discoveredModel
	}
	if model == "" {
		model = c.hint.Model
	}
	channel := c.channel
	if channel == "" {
		channel = c.hint.Channel
	}

	if c.terminal {
		return Observation{
			Metered:         true,
			Schema:          c.schema,
			Channel:         channel,
			Model:           model,
			Tokens:          c.tokens,
			ReportedCostUSD: c.reportedCost,
			Warnings:        c.warnings,
		}
	}

	reason := c.failReason
	if reason == "" {
		if exitErr != nil {
			reason = ReasonInterrupted
		} else {
			reason = ReasonMissingTerminal
		}
	}
	return Observation{
		Metered:  false,
		Channel:  channel,
		Model:    model,
		Reason:   reason,
		Warnings: c.warnings,
	}
}
