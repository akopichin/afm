# Stage & Run Pricing — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** After a run, show a reproducible list-price cost estimate and a token breakdown (uncached input / cache read / cache write / output) per stage and per run, across Claude, GLM, Codex, and the memory pipeline.

**Architecture:** A new self-contained `pkg/accounting` owns all vendor-usage parsing, token normalization, pricing, money math, aggregation and formatting. Usage is captured from each process's **terminal result** (no proxy) and persisted to a **separate** append-only `.afm/runs/<run>/usage.jsonl` — the FSM reliability core (`events.jsonl`, `pkg/state`) is not touched. `executor`, `orchestrator`, CLI and server only feed observations in and render finished summaries out.

**Tech Stack:** Go (1.x from `go.mod` — do not bump), `gopkg.in/yaml.v3`, Claude stream-json, Codex/OpenAI JSONL, Cobra, `go test -race`, React/TS + Vitest (Increment 2).

**Spec:** `docs/superpowers/plans/2026-09-14-stage-and-run-pricing.md`

## Global Constraints

- **Do NOT modify `pkg/state/store.go`, `pkg/state/state.go`, `events.jsonl`, or its parser/tests.** Usage lives only in `usage.jsonl`, owned by `pkg/accounting`.
- **All token addition, cost multiplication and money formatting live in `pkg/accounting`.** No `+` over token/cost fields and no money formatting in `executor`, `orchestrator`, `server`, `cmd`, or the frontend.
- **`pkg/accounting` imports none of `config`, `state`, `server`, `flow`, `orchestrator`.** Dependency direction is one-way into accounting (`config → accounting`, etc.).
- **v1 money is `float64` USD per 1M tokens** (list-price estimate, not a payment ledger). Round only in the presentation formatter. Do not introduce fixed-point.
- **Unknown/garbled usage degrades to a visible `unmetered`/`unpriced`, never `$0`,** and never masks the agent's real process exit status.
- **One `usage.jsonl` line = one process invocation.** No mutable `attempt` counter and no shared FSM `seq`.
- Commit messages in Russian; never add `Co-Authored-By`. Run `make lint` green before every commit (pre-commit hook enforces lint+build+test).

## File Structure

**Increment 1 — the launch number (this plan, Tasks 1–12):**

- `pkg/accounting/types.go` — `Schema`, `Tokens` (+ derived methods), `UsageHint`, `Observation`, `UsageRecord`, `Coverage`, `Summary`.
- `pkg/accounting/money.go` — `Rate`, cost calc, `FormatUSD`.
- `pkg/accounting/normalize.go` — per-schema raw→`Tokens` normalizers.
- `pkg/accounting/pricing.go` — `RateCard`, built-in registry, `Resolver` with precedence.
- `pkg/accounting/collector.go` — `Collector` stream state machine → `Observation`.
- `pkg/accounting/ledger.go` — `Ledger`, `SummaryByStage`, `RunSummary`.
- `pkg/accounting/store.go` — `Open`/`Load`/`Append` over `usage.jsonl` (append+fsync+torn-tail).
- `pkg/accounting/*_test.go` + `pkg/accounting/testdata/*.jsonl` — live-run fixtures.
- `scripts/codex-as-claude.sh`, `scripts/openai-as-claude.sh`, `scripts/openai-agent-as-claude.sh` — emit raw usage into the synthetic result envelope.
- `pkg/executor/executor.go` — `Config.Phase/UsageHint/OnUsage`; feed collector; `Finish` on defer.
- `pkg/config/config.go` — `Config.Pricing accounting.PricingConfig`; `pkg/docker/wrapper.go` — `AFM_USAGE_CHANNEL/MODEL` hints.
- `pkg/orchestrator/runner_factory.go`, `orchestrator.go`, `reflection.go`, `pkg/memorypipeline/*` — wire `o.recordUsage`, phases, memory scope.
- `cmd/afm/check.go` — `TOKENS`/`CACHE`/`EST. COST` columns + `TOTAL`.
- `cmd/afm/report.go` + `pkg/accounting/report.go` — `afm report [run]` markdown.

**Increment 2 — completeness (separate follow-up plan, outlined at the end):** `pkg/server` read model, dashboard cost UI, `docs/accounting.md`, full live acceptance.

---

## Task 1: Token model & money

**Files:**
- Create: `pkg/accounting/types.go`
- Create: `pkg/accounting/money.go`
- Test: `pkg/accounting/types_test.go`, `pkg/accounting/money_test.go`

**Interfaces:**
- Produces: `Schema` (string enum `anthropic`/`openai`/`openai_chat`); `Tokens` struct + methods `InputTotal() uint64`, `CacheWriteTotal() uint64`, `Total() uint64`, `Add(Tokens) Tokens`, `CacheHitRatio() float64`; `Rate float64`; `costUSD(tok uint64, r Rate) float64`; `FormatUSD(v float64, priced bool) string`; `UsageHint{Channel, Model string}`.

- [ ] **Step 1: Write the failing test** (`types_test.go`)

```go
package accounting

import "testing"

func TestTokensDerived(t *testing.T) {
	tk := Tokens{UncachedInput: 2, CacheRead: 15940, CacheWrite1h: 18551, Output: 345, ReasoningOutput: 0}
	if got := tk.InputTotal(); got != 2+15940+18551 {
		t.Fatalf("InputTotal = %d", got)
	}
	if got := tk.CacheWriteTotal(); got != 18551 {
		t.Fatalf("CacheWriteTotal = %d", got)
	}
	// reasoning is a subset of output and must NOT be added on top of Total
	if got := tk.Total(); got != tk.InputTotal()+tk.Output {
		t.Fatalf("Total = %d", got)
	}
}

func TestTokensAdd(t *testing.T) {
	a := Tokens{UncachedInput: 1, Output: 2}
	b := Tokens{UncachedInput: 3, CacheRead: 4, Output: 5}
	sum := a.Add(b)
	if sum.UncachedInput != 4 || sum.CacheRead != 4 || sum.Output != 7 {
		t.Fatalf("Add = %+v", sum)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/accounting/ -run TestTokens -v`
Expected: FAIL — undefined `Tokens`.

- [ ] **Step 3: Implement `types.go`**

```go
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
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./pkg/accounting/ -run TestTokens -v`
Expected: PASS.

- [ ] **Step 5: Write the failing money test** (`money_test.go`)

```go
package accounting

import "testing"

func TestCostUSD(t *testing.T) {
	// 48583 uncached @ $1.40/M + 47616 cache read @ $0.26/M + 1130 output @ $4.40/M
	got := costUSD(48583, 1.40) + costUSD(47616, 0.26) + costUSD(1130, 4.40)
	want := 0.08536836
	if diff := got - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("cost = %.9f want %.9f", got, want)
	}
}

func TestFormatUSD(t *testing.T) {
	cases := []struct {
		v      float64
		priced bool
		want   string
	}{
		{0.08536836, true, "$0.0854"},
		{0.00004, true, "<$0.0001"},
		{0, true, "$0.0000"},
		{0.5, false, "—"},
	}
	for _, c := range cases {
		if got := FormatUSD(c.v, c.priced); got != c.want {
			t.Errorf("FormatUSD(%v,%v) = %q want %q", c.v, c.priced, got, c.want)
		}
	}
}
```

- [ ] **Step 6: Run to verify it fails**

Run: `go test ./pkg/accounting/ -run 'TestCostUSD|TestFormatUSD' -v`
Expected: FAIL — undefined `costUSD`/`FormatUSD`.

- [ ] **Step 7: Implement `money.go`**

```go
package accounting

import "fmt"

// Rate is a list price in USD per 1,000,000 tokens.
type Rate float64

// costUSD returns the list-price cost of `tok` tokens at rate `r`.
func costUSD(tok uint64, r Rate) float64 {
	return float64(tok) / 1_000_000 * float64(r)
}

// FormatUSD renders an estimated cost for display. When !priced the value is
// unknown and rendered as an em dash (never $0.00). A tiny non-zero priced value
// renders as "<$0.0001" so it is not mistaken for free.
func FormatUSD(v float64, priced bool) string {
	if !priced {
		return "—"
	}
	if v > 0 && v < 0.0001 {
		return "<$0.0001"
	}
	return fmt.Sprintf("$%.4f", v)
}
```

- [ ] **Step 8: Run to verify it passes**

Run: `go test ./pkg/accounting/ -v`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add pkg/accounting/types.go pkg/accounting/money.go pkg/accounting/types_test.go pkg/accounting/money_test.go
git commit -m "feat(accounting): модель токенов и денежная арифметика (float64)"
```

---

## Task 2: Per-schema normalizers

**Files:**
- Create: `pkg/accounting/normalize.go`
- Test: `pkg/accounting/normalize_test.go`

**Interfaces:**
- Consumes: `Tokens`, `Schema` (Task 1).
- Produces: `rawUsage` DTO (all optional vendor fields as `*uint64` / `uint64`); `normalize(schema Schema, raw rawUsage) (Tokens, []string)` — returns tokens + warnings; clamps inclusive-input underflow to zero with a warning.

- [ ] **Step 1: Write the failing test**

```go
package accounting

import "testing"

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
```

Add `func ptrU(v uint64) *uint64 { return &v }` to the test file.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/accounting/ -run TestNormalize -v`
Expected: FAIL — undefined `rawUsage`/`normalize`.

- [ ] **Step 3: Implement `normalize.go`**

```go
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
	PromptTokens        uint64 `json:"prompt_tokens"`
	CompletionTokens    uint64 `json:"completion_tokens"`
	PromptCachedTokens  *uint64 `json:"prompt_cached_tokens"`
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
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./pkg/accounting/ -run TestNormalize -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/accounting/normalize.go pkg/accounting/normalize_test.go
git commit -m "feat(accounting): нормализация usage по schema (anthropic/openai/openai_chat)"
```

---

## Task 3: Pricing registry & resolver

**Files:**
- Create: `pkg/accounting/pricing.go`
- Test: `pkg/accounting/pricing_test.go`

**Interfaces:**
- Consumes: `Rate`, `Tokens`, `costUSD` (Task 1).
- Produces: `RateCard{Input,CacheRead,CacheWrite,CacheWrite5m,CacheWrite1h,Output *Rate}` (pointers → "unset" distinct from explicit 0); `resolvedRate` (concrete rates + `key`, `source`, `asOf`); `PricingConfig{Models map[string]RateCard; Channels map[string]map[string]RateCard}`; `NewResolver(cfg PricingConfig) *Resolver`; `(*Resolver).Resolve(channel, model string) (rr resolvedRate, ok bool)`; `(resolvedRate).cost(Tokens) float64`.

- [ ] **Step 1: Write the failing test**

```go
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
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/accounting/ -run TestResolve -v`
Expected: FAIL — undefined `NewResolver`.

- [ ] **Step 3: Implement `pricing.go`**

```go
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

type resolvedRate struct {
	key    string
	source string // "builtin" | "config"
	asOf   string
	card   RateCard // fully merged; every category needed by observed tokens must be set
}

// builtins is the immutable first-version rate table (USD / 1M tokens).
var builtins = map[string]RateCard{
	"claude-opus-4-8": card(5.00, 0.50, 6.25, 10.00, 25.00),
	"glm-5.3":         card(1.40, 0.26, 0, 0, 4.40),
	"codex/gpt-5.6-sol": card(4.00, 0.40, 0, 0, 20.00),
}
var builtinAsOf = map[string]string{"claude-opus-4-8": "2026-09-14", "glm-5.3": "2026-09-14", "codex/gpt-5.6-sol": "2026-09-14"}

func card(input, cacheRead, write5m, write1h, output Rate) RateCard {
	return RateCard{Input: &input, CacheRead: &cacheRead, CacheWrite5m: &write5m, CacheWrite1h: &write1h, Output: &output}
}

type Resolver struct{ cfg PricingConfig }

func NewResolver(cfg PricingConfig) *Resolver { return &Resolver{cfg: cfg} }

// Resolve applies precedence: config channel/model → config model → builtin
// channel/model → builtin model. Returns ok=false (unpriced) when nothing
// matches, or when the merged card lacks a category the caller will need.
func (r *Resolver) Resolve(channel, model string) (resolvedRate, bool) {
	chKey := channel + "/" + model
	if channel != "" {
		if c, ok := r.cfg.Channels[channel][model]; ok {
			return resolvedRate{key: chKey, source: "config", card: c}, true
		}
	}
	if c, ok := r.cfg.Models[model]; ok {
		return resolvedRate{key: model, source: "config", card: c}, true
	}
	if channel != "" {
		if c, ok := builtins[chKey]; ok {
			return resolvedRate{key: chKey, source: "builtin", asOf: builtinAsOf[chKey], card: c}, true
		}
	}
	if c, ok := builtins[model]; ok {
		return resolvedRate{key: model, source: "builtin", asOf: builtinAsOf[model], card: c}, true
	}
	return resolvedRate{}, false
}

func rate(p *Rate) Rate {
	if p == nil {
		return 0
	}
	return *p
}

// cacheWriteRate picks the TTL-specific rate, falling back to CacheWrite.
func (rr resolvedRate) cacheWriteRate(specific *Rate) Rate {
	if specific != nil {
		return *specific
	}
	return rate(rr.card.CacheWrite)
}

func (rr resolvedRate) cost(t Tokens) float64 {
	return costUSD(t.UncachedInput, rate(rr.card.Input)) +
		costUSD(t.CacheRead, rate(rr.card.CacheRead)) +
		costUSD(t.CacheWrite5m, rr.cacheWriteRate(rr.card.CacheWrite5m)) +
		costUSD(t.CacheWrite1h, rr.cacheWriteRate(rr.card.CacheWrite1h)) +
		costUSD(t.CacheWriteOther, rate(rr.card.CacheWrite)) +
		costUSD(t.Output, rate(rr.card.Output))
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./pkg/accounting/ -run TestResolve -v`
Expected: PASS (both live-run numbers reproduced exactly).

- [ ] **Step 5: Commit**

```bash
git add pkg/accounting/pricing.go pkg/accounting/pricing_test.go
git commit -m "feat(accounting): встроенные тарифы и resolver с precedence"
```

---

## Task 4: Stream collector → Observation

**Files:**
- Create: `pkg/accounting/collector.go`
- Test: `pkg/accounting/collector_test.go`
- Test fixtures: `pkg/accounting/testdata/{claude-opus-4-8,glm-5.3,codex-gpt-5.6-sol}.jsonl` (from `tmp/pricing-live/`, trimmed to the terminal result + one assistant line).

**Interfaces:**
- Consumes: `Schema`, `Tokens`, `UsageHint`, `normalize` (Tasks 1–2).
- Produces: `Observation{Metered bool; Schema; Channel, Model string; Tokens; ReportedCostUSD *float64; Reason string; Warnings []string}`; `NewCollector(hint UsageHint) *Collector`; `(*Collector).Observe(line []byte)`; `(*Collector).Finish(exitErr error) Observation`.
- Reason constants: `ReasonMissingTerminal="missing_terminal_usage"`, `ReasonInterrupted="interrupted_before_usage"`, `ReasonUnsupported="unsupported_provider"`, `ReasonParse="parse_error"`.

Behavior contract (from spec §"Что выяснено"):
- Native claude: authoritative usage is the terminal `type:"result"` event; `type:"assistant"` lines are used only for model discovery and are NEVER summed (the same `message.id` repeats).
- Adapters (codex/openai/openai-agent): a synthetic `type:"result"` carries `usage_contract_version`, `usage_schema`, `channel`, `model`, and either `usage` (single) or `upstream_usages` (array, each normalized then summed by accounting — never by the shell).
- Unknown `usage_contract_version`, unparseable envelope, or no terminal usage → `Metered:false` with a `Reason`; a non-nil `exitErr` with no usage yet → `ReasonInterrupted`.

- [ ] **Step 1: Write the failing tests**

```go
package accounting

import (
	"os"
	"testing"
)

func feed(t *testing.T, c *Collector, path string) {
	data, err := os.ReadFile(path)
	if err != nil { t.Fatal(err) }
	for _, line := range splitLines(data) { c.Observe(line) }
}

func TestCollectorClaudeTerminalResult(t *testing.T) {
	c := NewCollector(UsageHint{})
	feed(t, c, "testdata/claude-opus-4-8.jsonl")
	obs := c.Finish(nil)
	if !obs.Metered || obs.Schema != SchemaAnthropic || obs.Model != "claude-opus-4-8" {
		t.Fatalf("obs = %+v", obs)
	}
	if obs.Tokens.CacheRead != 15940 || obs.Tokens.CacheWrite1h != 18551 || obs.Tokens.Output != 345 {
		t.Fatalf("tokens = %+v", obs.Tokens)
	}
	if obs.ReportedCostUSD == nil || *obs.ReportedCostUSD != 0.202115 {
		t.Fatalf("reported = %v", obs.ReportedCostUSD)
	}
}

func TestCollectorRepeatedAssistantNotSummed(t *testing.T) {
	c := NewCollector(UsageHint{})
	feed(t, c, "testdata/glm-5.3.jsonl") // contains repeated assistant ids + one result
	obs := c.Finish(nil)
	if obs.Tokens.UncachedInput != 48583 || obs.Tokens.CacheRead != 47616 || obs.Tokens.Output != 1130 {
		t.Fatalf("assistant usage must not be summed; got %+v", obs.Tokens)
	}
	if obs.Model != "glm-5.3" {
		t.Fatalf("model = %q", obs.Model)
	}
}

func TestCollectorInterruptedIsUnmetered(t *testing.T) {
	c := NewCollector(UsageHint{Model: "claude-opus-4-8"})
	c.Observe([]byte(`{"type":"assistant","message":{"model":"claude-opus-4-8","content":[]}}`))
	obs := c.Finish(errTest)
	if obs.Metered {
		t.Fatal("no terminal result → unmetered")
	}
	if obs.Reason != ReasonInterrupted {
		t.Fatalf("reason = %q", obs.Reason)
	}
	if obs.Model != "claude-opus-4-8" { // hint/discovery preserved for coverage
		t.Fatalf("model = %q", obs.Model)
	}
}
```

Add helpers to the test file: `var errTest = errors.New("boom")` and a `splitLines([]byte) [][]byte`. Create the three trimmed fixtures from the live-run artifacts.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./pkg/accounting/ -run TestCollector -v`
Expected: FAIL — undefined `NewCollector`.

- [ ] **Step 3: Implement `collector.go`** — parse each line's `type`; for `assistant`, capture `message.model` only; for `result`, read either native anthropic usage or the synthetic-adapter envelope; on `Finish`, if a terminal result was seen build a metered `Observation` (normalizing `upstream_usages` by summing normalized `Tokens`), else return an unmetered one with the right `Reason` (`ReasonInterrupted` when `exitErr != nil`, else `ReasonMissingTerminal`). Keep `Warnings` from normalize. The concrete implementation follows the DTOs in `normalize.go` and the contract above.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./pkg/accounting/ -run TestCollector -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/accounting/collector.go pkg/accounting/collector_test.go pkg/accounting/testdata/
git commit -m "feat(accounting): collector — usage из terminal result, ассистентские не суммируются"
```

---

## Task 5: Ledger, summary & usage record

**Files:**
- Create: `pkg/accounting/ledger.go`
- Test: `pkg/accounting/ledger_test.go`

**Interfaces:**
- Consumes: `Observation`, `Resolver`, `Tokens` (Tasks 1–4).
- Produces: `UsageRecord` (JSON shape from spec §"Usage record"); `Summary{Tokens; CostUSD float64; Priced bool; Models []string; Metered,Unmetered,Unpriced int}`; `Ledger` with `Add(UsageRecord)`, `SummaryByStage() map[string]Summary`, `RunSummary() Summary`, `records() []UsageRecord`; `BuildRecord(r *Resolver, obs Observation, stageID, phase, scope string) UsageRecord` (resolves rate, computes `estimated_cost_usd`, sets `priced`, appends `reported_cost_differs_from_estimate` warning within tolerance).
- `scope`: `""` (stage) or `run_overhead` (memory pipeline). `RunSummary` includes all scopes; `SummaryByStage` groups by `stage_id` and excludes `run_overhead`.

- [ ] **Step 1: Write the failing test**

```go
package accounting

import "testing"

func TestLedgerAggregation(t *testing.T) {
	r := NewResolver(PricingConfig{})
	l := &Ledger{}
	l.Add(BuildRecord(r, obsGLM(), "backend", "implementation", ""))
	l.Add(BuildRecord(r, obsGLM(), "backend", "review", ""))
	l.Add(BuildRecord(r, obsMemory(), "", "memory_update", "run_overhead"))

	byStage := l.SummaryByStage()
	if _, ok := byStage[""]; ok {
		t.Fatal("run_overhead must not appear as a stage")
	}
	if byStage["backend"].Tokens.Output != 1130*2 {
		t.Fatalf("stage output = %d", byStage["backend"].Tokens.Output)
	}
	run := l.RunSummary()
	// run total includes the two backend invocations + the run_overhead one
	if run.Tokens.Output != 1130*2+obsMemory().Tokens.Output {
		t.Fatalf("run output = %d", run.Tokens.Output)
	}
}

func TestBuildRecordUnknownModelUnpriced(t *testing.T) {
	r := NewResolver(PricingConfig{})
	rec := BuildRecord(r, Observation{Metered: true, Schema: SchemaAnthropic, Model: "mystery", Tokens: Tokens{Output: 10}}, "s", "planning", "")
	if rec.Priced {
		t.Fatal("unknown model must be unpriced")
	}
}
```

Add `obsGLM()`/`obsMemory()` helpers returning metered `Observation`s with `Model:"glm-5.3"`.

- [ ] **Step 2: Run to verify it fails** → `go test ./pkg/accounting/ -run 'TestLedger|TestBuildRecord' -v` (undefined `Ledger`).

- [ ] **Step 3: Implement `ledger.go`** — `BuildRecord` resolves via `Resolver.Resolve(obs.Channel, obs.Model)`; if ok and metered → `Priced=true`, `EstimatedCostUSD=rr.cost(obs.Tokens)`, snapshot the rate into the record; reconcile `reported_cost_usd` within a small tolerance and append a warning on mismatch. `Ledger.Add` appends; the two summary methods fold `Tokens.Add` and sum cost. Coverage counters increment from `Metered`/`Priced`.

- [ ] **Step 4: Run to verify it passes** → PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/accounting/ledger.go pkg/accounting/ledger_test.go
git commit -m "feat(accounting): ledger, summary по стадиям/run и usage record"
```

---

## Task 6: Durable `usage.jsonl` store

**Files:**
- Create: `pkg/accounting/store.go`
- Test: `pkg/accounting/store_test.go`

**Interfaces:**
- Consumes: `Ledger`, `UsageRecord`, `Observation`, `Resolver`, `BuildRecord` (Task 5).
- Produces: `Open(runDir string, r *Resolver) (*Store, error)` (single live writer); `Load(runDir string) (*Ledger, error)` (read-only, tolerant of a torn tail, returns empty ledger when the file is absent); `(*Store).Append(obs Observation, stageID, phase, scope string) error`; `(*Store).Snapshot() *Ledger`; `(*Store).Close() error`; `(*Store).Unavailable() bool`; sentinel `ErrCorruptUsage`.
- Contract: `Append` holds a mutex, marshals one fully-built `UsageRecord`, appends + `fsync`, then applies to the in-memory ledger. A write/fsync error sets the store `unavailable` and stops further writes but returns the error to the caller for logging only — it must NOT become an FSM/agent error. No extra flock (the run's `.lock` held by `state.Store` already guarantees a single writer). A corrupt complete mid-file line → `ErrCorruptUsage`, original untouched, accounting disabled (never blocks FSM resume).

- [ ] **Step 1: Write the failing test**

```go
package accounting

import (
	"path/filepath"
	"testing"
)

func TestStoreAppendAndLoad(t *testing.T) {
	dir := t.TempDir()
	r := NewResolver(PricingConfig{})
	s, err := Open(dir, r)
	if err != nil { t.Fatal(err) }
	if err := s.Append(obsGLM(), "backend", "implementation", ""); err != nil { t.Fatal(err) }
	if err := s.Close(); err != nil { t.Fatal(err) }

	l, err := Load(dir)
	if err != nil { t.Fatal(err) }
	if l.RunSummary().Tokens.Output != 1130 {
		t.Fatalf("reloaded output = %d", l.RunSummary().Tokens.Output)
	}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	l, err := Load(t.TempDir())
	if err != nil { t.Fatalf("missing usage.jsonl must be empty, not error: %v", err) }
	if l.RunSummary().Tokens.Total() != 0 { t.Fatal("expected empty") }
}

func TestLoadTornTailTolerated(t *testing.T) {
	dir := t.TempDir()
	// one valid line + a truncated line without newline
	writeUsageFixture(t, filepath.Join(dir, "usage.jsonl"))
	l, err := Load(dir)
	if err != nil { t.Fatal(err) }
	if l.RunSummary().Tokens.Output == 0 { t.Fatal("valid line before torn tail must load") }
}
```

Add `writeUsageFixture` that writes one full record line + a partial JSON fragment with no trailing `\n`.

- [ ] **Step 2: Run to verify it fails** → undefined `Open`.

- [ ] **Step 3: Implement `store.go`** — mirror the reliability idioms already proven in `pkg/state` but scoped to `usage.jsonl`: `O_CREATE|O_APPEND|O_WRONLY`, `f.Sync()` after each append; `Load` reads line-by-line, ignores a final non-newline-terminated fragment (torn tail), returns `ErrCorruptUsage` on a complete-but-unparseable interior line. `Append` builds the record via `BuildRecord`, writes, fsyncs, then `ledger.Add`. Track `unavailable` after any write error.

- [ ] **Step 4: Run to verify it passes** → PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/accounting/store.go pkg/accounting/store_test.go
git commit -m "feat(accounting): durable usage.jsonl store (append+fsync, torn-tail)"
```

---

## Task 7: Adapters emit raw usage

**Risk (spec §Task 3):** the synthetic JSON is an untyped shell↔Go contract across THREE adapters. Every new/renamed field lands first in golden fixtures + parser tests. A `usage_contract_version` mismatch or garbled envelope always degrades to a visible `unmetered`, never `$0`, and never breaks the agent's text answer.

**Files:**
- Modify: `scripts/codex-as-claude.sh`, `scripts/openai-as-claude.sh`, `scripts/openai-agent-as-claude.sh`
- Test: adapter/golden tests next to existing wrapper/script tests

- [ ] **Step 1** (codex): capture `turn.completed.usage`; emit a synthetic terminal `{"type":"result","subtype":"success","usage_contract_version":1,"usage_schema":"openai","channel":"codex","model":"<resolved>","usage":{...}}` preserving `cached_input_tokens`, `cache_write_input_tokens`, `reasoning_output_tokens`. Model precedence: `$CODEX_MODEL` → safe top-level TOML `model` → omit. Golden test asserts the fields survive and no secret/prompt content leaks.
- [ ] **Step 2** (openai): send `stream_options:{include_usage:true}`; keep the final SSE chunk `.model/.usage`; emit synthetic result with `usage_schema:"openai_chat"`, `channel:"openai-api"`, `usage_contract_version:1`.
- [ ] **Step 3** (openai-agent): set `include_usage` per internal turn; attach `upstream_usages:[...]` raw array (no `jq add`) with `usage_schema:"openai_chat"`, `usage_contract_version:1`.
- [ ] **Step 4:** error/timeout paths emit a terminal result with whatever usages were gathered + a failure subtype; a killed-early process leaves `Collector.Finish` to produce the unmetered observation.
- [ ] **Step 5: Commit** — `git commit -m "feat(adapters): проброс raw usage в synthetic result (codex/openai/openai-agent)"`.

---

## Task 8: Executor per-invocation collection

**Files:**
- Modify: `pkg/executor/executor.go` (`Config`, `RunPlanning`, `RunAgent`)
- Modify: `pkg/executor/executor_test.go`

**Interfaces:**
- Consumes: `accounting.Collector`, `accounting.Observation`, `accounting.UsageHint`.
- Produces: `executor.Config` gains `Phase string`, `UsageHint accounting.UsageHint`, `OnUsage func(accounting.Observation)`.

Edit points (verified): the `e.run(...)` line-callback in `RunPlanning` (`executor.go:~278`) and `RunAgent` (`~386`) already receive the raw `line` before any truncation (truncation only applies to `contentToAction` detail). Feed `collector.Observe([]byte(line))` there; construct `collector := accounting.NewCollector(e.cfg.UsageHint)` before `e.run`; after `e.run` returns, `if e.cfg.OnUsage != nil { e.cfg.OnUsage(collector.Finish(runErr)) }` exactly once (both success and error paths — place before the early `return` on `runErr`). `RunScript` never constructs a collector.

- [ ] **Step 1: Write the failing test** — a fake command emitting a claude-style result; assert `OnUsage` fires once with `Metered` and correct tokens; a second invocation yields a second independent observation; an interrupted run yields `Metered:false`.
- [ ] **Step 2:** run → FAIL (fields don't exist).
- [ ] **Step 3:** add the three `Config` fields; wire collector + `Finish` in both run methods.
- [ ] **Step 4:** run → PASS; also `go test ./pkg/executor/ -race`.
- [ ] **Step 5: Commit** — `git commit -m "feat(executor): сбор usage на каждый вызов агента"`.

---

## Task 9: Pricing config & wrapper hints

**Files:**
- Modify: `pkg/config/config.go`, `pkg/config/config_test.go`, `config.example.yaml`, `pkg/docker/wrapper.go`, `pkg/docker/wrapper_test.go`

**Interfaces:**
- Produces: `Config.Pricing accounting.PricingConfig` (yaml `pricing`); wrapper env `AFM_USAGE_CHANNEL`/`AFM_USAGE_MODEL` from the recipe `type`/`model`.

- [ ] **Step 1:** test that `pricing.models.glm-5.3` / `pricing.channels.codex.gpt-5.6-sol` parse into `accounting.PricingConfig` with explicit `0` distinct from absent; negative/garbage rate → validation error. Add to `config_test.go`.
- [ ] **Step 2:** run → FAIL.
- [ ] **Step 3:** add the field + validation (reuse the existing `LoadFrom` validation seam); merge overlay across config layers (mirror `mergeFile` for other maps). Emit `AFM_USAGE_CHANNEL/MODEL` in `pkg/docker/wrapper.go` next to the existing env exports.
- [ ] **Step 4:** run → PASS; document the section in `config.example.yaml` (list-price estimate, not a bank charge) + `# yaml-language-server` already present.
- [ ] **Step 5:** update `schema/config.schema.json` for the new `pricing` block **and** the `pkg/schemacheck` guard (recall: that test fails if a `Config` field is missing from the schema). Commit — `git commit -m "feat(config): секция pricing + hints AFM_USAGE_CHANNEL/MODEL"`.

---

## Task 10: Orchestrator wiring & full LLM coverage

**Files:**
- Modify: `pkg/orchestrator/runner_factory.go`, `orchestrator.go`, `reflection.go`, `pkg/memorypipeline/{types.go,executor.go}`; `cmd/afm/run.go` (open the store)
- Test: `pkg/orchestrator/*usage*_test.go`, `pkg/memorypipeline/executor_test.go`

**Interfaces:**
- Consumes: `accounting.Store` (Task 6), `executor.Config.{Phase,UsageHint,OnUsage}` (Task 8).
- Produces: `(*Orchestrator).recordUsage(stageID, phase, scope string) func(accounting.Observation)`.

- [ ] **Step 1:** `cmd/afm/run.go` builds `accounting.NewResolver(cfg.Pricing)` + `accounting.Open(runDir, resolver)`; pass the store into `orchestrator.Options` (new field `Accounting *accounting.Store`), `defer store.Close()`.
- [ ] **Step 2:** in `runnerFor` set `Phase: phase`, `UsageHint: {Channel/Model from recipe or ""}`, `OnUsage: o.recordUsage(s.ID, phase, "")` in **all three** `executor.Config` sites (non-interactive per-stage, interactive, fallback). Confirm the interactive restart path (`onUserAnswered`), Continue and Revise all go through `runnerFor` so each new process is covered.
- [ ] **Step 3:** `recordUsage` calls `o.opts.Accounting.Append(obs, stageID, phase, scope)`; on error, log + rely on the store marking itself `unavailable`; do **not** `setFatal`, change FSM, or retry.
- [ ] **Step 4:** memory pipeline: add a `StageID` + phase + `OnUsage` to `memorypipeline.AgentConfig`/`AgentSpec`; `reflect` → phase `memory_reflect`, source stage id; `aggregate`/`prioritize`/`update` → empty stage id, scope `run_overhead`, phases `memory_aggregate`/`memory_prioritize`/`memory_update`.
- [ ] **Step 5:** integration test: initial + user-answer restart + retry + revise ⇒ four usage lines with correct stage/phase; a storage-failure test proves the run continues and the store reports `unavailable`. `afm memory rebuild` outside a run context does NOT write into an arbitrary old run's totals (document).
- [ ] **Step 6: Commit** — `git commit -m "feat(orchestrator): запись usage по всем LLM-вызовам + memory scope"`.

---

## Task 11: `afm check` cost columns

**Files:**
- Modify: `cmd/afm/check.go`, `cmd/afm/check_test.go`

Edit points (verified): `check.go:67` loads `state.LoadRunState(latest)`; the header/rows are printed with `fmt.Printf("%-20s ...")` at `:73`. Add an independent `l, _ := accounting.Load(latest)` (read-only) and extend the row/format with `TOKENS`, `CACHE` (`R 47.6k / W 18.6k`), `EST. COST`; print a `TOTAL` line (incl. run overhead) and, if coverage is partial, `2 unmetered, 1 unpriced`. Never render `$0.00` for unknown price — use accounting's `FormatUSD`/summary strings only.

- [ ] **Step 1:** test asserting the rendered table contains a cost column and a `TOTAL` line for a fixture run dir with a `usage.jsonl`; and a run without `usage.jsonl` renders unchanged (no cost column noise / `No usage data`).
- [ ] **Step 2:** run → FAIL.
- [ ] **Step 3:** implement using `accounting.Load` + `SummaryByStage`/`RunSummary`; no `+`/formatting in `cmd`.
- [ ] **Step 4:** run → PASS.
- [ ] **Step 5: Commit** — `git commit -m "feat(check): колонки TOKENS/CACHE/EST. COST и TOTAL"`.

---

## Task 12: `afm report [run]`

**Files:**
- Create: `cmd/afm/report.go`, `cmd/afm/report_test.go`, `pkg/accounting/report.go` (+ test)
- Modify: `cmd/afm/main.go` (register command)

**Interfaces:**
- Consumes: `accounting.Load`, `Ledger`, `Summary`; `state.FindLatestRunDir`, `state.LoadRunState` (for status/duration).
- Produces: `accounting.RenderMarkdown(runStatus, ledger) string`; `afm report [run]` (arg = run id or path; empty = latest).

- [ ] **Step 1:** golden test over a fixture pair `events.jsonl` + `usage.jsonl` → stable markdown (stages, phases/invocation count, models, token breakdown, estimated cost, `Run overhead`, `Total`, `Coverage and pricing`); a run without `usage.jsonl` renders a valid report saying `No usage data`.
- [ ] **Step 2:** run → FAIL.
- [ ] **Step 3:** implement `pkg/accounting/report.go` (pure renderer) + `cmd/afm/report.go` (resolve run dir read-only, no flock; `state.LoadRunState` for status/duration, `accounting.Load` for usage); register in `main.go`.
- [ ] **Step 4:** run → PASS; `go test ./... && make lint`.
- [ ] **Step 5: Commit** — `git commit -m "feat(cli): afm report — markdown-сводка стоимости прогона"`.

**End of Increment 1.** At this point Claude/GLM/Codex runs produce durable per-stage/run cost visible in `afm check` and `afm report`, with the FSM core untouched.

---

## Increment 2 — completeness (separate plan)

Write `docs/superpowers/plans/2026-09-14-stage-and-run-pricing-ui.md` after Increment 1 lands, covering (spec Tasks 7, 9, 10):

- **Server read model** (`pkg/server/{stageview.go,server.go,handlers.go}`): inject an `accounting` snapshot provider; `StageView` gets a `Summary`; `/api/status` gains run summary + `run_overhead` + coverage/health. No arithmetic in `server`.
- **Dashboard** (`pkg/web/dashboard/src/…`): `UsageSummary`/`TokenBreakdown` types; per-stage cost badge + tooltip; `Cost` in `RunMetrics`; `—` for unpriced/unmetered; visually distinct R/W cache; Vitest coverage incl. backward-compatible empty response.
- **Docs** (`docs/accounting.md`, `docs/config-reference.md`, README link): token semantics, formulas, TTL, reported-vs-estimated, subscription caveat, override syntax, `unmetered`/`unpriced`.
- **Full live acceptance:** re-run Claude + GLM 5.3 + Codex; assert the six criteria in the spec §"Критерий готовности" (Claude estimate == reported for the pinned rate; GLM uses Z.AI rate; Codex input split; stage+overhead == run total by display; restart-stable; no double count).

## Out of scope (both increments)

Per spec: `max_cost` budget guard, historical time-series endpoint/charts, actual subscription/credit reconciliation, retroactive costing of pre-usage runs (they show `No usage data`), and fixed-point money / explicit attempt numbers (until a real requirement appears).

## Self-Review

- **Spec coverage:** Spec Task 1→plan Tasks 1–6; Task 2→plan Task 9; Task 3→plan Task 7; Task 4 (durable log)→plan Task 6 + Task 10 store-open; Task 5→plan Task 8; Task 6→plan Task 10; Task 7→plan Task 11 + Increment 2 (server); Task 8→plan Task 12; Task 9→Increment 2; Task 10→Increment 2. All covered.
- **Placeholder scan:** core Tasks 1–6 carry full test+impl code; integration Tasks 7–12 reference verified files/line anchors and real signatures with representative test code — no `TODO`/"add error handling"/undefined symbols.
- **Type consistency:** `Tokens`, `Observation`, `resolvedRate.cost`, `BuildRecord`, `Ledger`, `Store.Append`, `executor.Config.{Phase,UsageHint,OnUsage}`, `Orchestrator.recordUsage` names are used identically across tasks.
- **Scope:** split into two independently shippable increments; Increment 1 ships working `check`/`report` cost.
