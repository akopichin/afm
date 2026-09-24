# Cost accounting

afm records the token usage of every agent invocation and turns it into an
**estimated** dollar cost — visible from the CLI (`afm check`, `afm report`)
and live in the dashboard (header tile, per-stage rail figure, Cost tab).
Nothing here is a real invoice: it is a transparent, reproducible **list-price
estimate**, computed entirely from tokens afm observed and rates afm knows
about.

## Where the numbers come from

Every agent process (`claude`, a GLM/Codex/DeepSeek shim, …) ends its
stream-json output with a terminal `result` line carrying a usage object.
afm's `pkg/accounting` package reads exactly that terminal line — never the
intermediate `assistant` messages, which repeat as a turn streams and would
double-count — normalizes it into a common `Tokens` shape, resolves a price
per category, and appends one record to `.afm/runs/<run>/usage.jsonl`. That
file is the single source of truth: `afm check`, `afm report`, and the
dashboard's `/api/status` all read (or are fed from) it, so they can never
disagree.

## Token schema semantics

Different providers report usage differently; afm normalizes all of them
into one `Tokens` shape before pricing:

```go
type Tokens struct {
    UncachedInput   uint64
    CacheRead       uint64
    CacheWrite5m    uint64
    CacheWrite1h    uint64
    CacheWriteOther uint64
    Output          uint64
    ReasoningOutput uint64 // subset of Output, never added on top of it
}
```

- **Anthropic** (`claude` stream-json `result.usage`) — `input_tokens` is
  already **exclusive** of cache: cache-read and cache-creation tokens are
  reported separately. Cache creation nests a `cache_creation` object with
  `ephemeral_5m_input_tokens`/`ephemeral_1h_input_tokens`; anything left over
  after subtracting those two from `cache_creation_input_tokens` lands in
  `CacheWriteOther` (a provider result that reports total cache-write tokens
  without a TTL breakdown).
- **OpenAI / Codex** (`turn.completed.usage`) — `input_tokens` is
  **inclusive** of cache: cached and cache-write tokens are subsets of it, so
  afm computes `uncached_input = max(input_tokens - cached_input_tokens -
  cache_write_input_tokens, 0)`. If the subtraction would go negative (an
  inconsistent upstream count), it clamps to zero and the record carries an
  `inclusive input underflow: …` warning instead of silently producing a
  wrong number. Codex has no TTL concept, so its cache-write tokens land in
  `CacheWriteOther`.
- **OpenAI Chat Completions** (`prompt_tokens`/`completion_tokens`) — same
  inclusive-input shape as Codex, one level simpler: `prompt_tokens` is
  inclusive, `prompt_cached_tokens` is its cached subset, `completion_tokens`
  is the output (already inclusive of any reasoning tokens).

`ReasoningOutput` is purely informational — it is a subset of `Output` and is
never summed on top of it in `Total()`/cost.

## The cost formula

Cost is computed per category, at list-price rates in **USD per 1,000,000
tokens**:

```text
cost = uncached_input × rate.input
     + cache_read     × rate.cache_read
     + cache_write_5m × rate.cache_write_5m  (falls back to rate.cache_write)
     + cache_write_1h × rate.cache_write_1h  (falls back to rate.cache_write)
     + cache_write_other × rate.cache_write
     + output         × rate.output
```

An invocation is priced only if **every** token category it actually used
(count > 0) resolves to a rate — a rate table that's missing a category the
tokens use makes the whole invocation `unpriced`, not a total that silently
prices the missing category at `$0`. An explicit rate of `0` is different
from a missing rate: it's a deliberate "this category is free", and prices
that category as `$0.0000` normally.

### Cache write TTL (5m / 1h)

Anthropic caches are billed per TTL: a 5-minute ephemeral cache write and a
1-hour one carry different rates. `cache_write_5m`/`cache_write_1h` in a rate
card are the TTL-specific rates; the generic `cache_write` rate is the
fallback used both when a specific TTL rate isn't set AND for
`cache_write_other` (a cache-creation figure with no TTL breakdown at all,
either because the provider doesn't report one or the upstream schema
doesn't have the concept).

## Reported vs. estimated cost

Native `claude` results carry the vendor's own `total_cost_usd`. afm records
it (`reported_cost_usd`) purely as a **diagnostic** — it is never used as
afm's own cost figure, and never overrides the computed estimate. If the two
disagree by more than one cent (`costToleranceUSD`), the record is tagged
with the warning `reported_cost_differs_from_estimate`, and `afm report`'s
coverage appendix lists every such mismatch (reported vs. estimated) so a
human can look closer — rounding, a stale local rate table, or genuinely
different vendor billing are all plausible causes.

## The subscription caveat

**Estimated cost is a list-price estimate, not a bank charge.** A Claude Max
or Codex/Work subscription bills a flat fee regardless of token volume;
credits, negotiated enterprise pricing, and gateway markups are all invisible
to afm. The number afm shows answers "what would this have cost at list
price, token for token" — useful for comparing stages/runs/models against
each other, not for reconciling an actual invoice. This is also why the
dashboard and CLI both call the figure **"Est. cost"**, never a bare `$X`
with no qualifier.

## `pricing:` overrides

The built-in rate table covers a small, deliberately conservative set of
models (see `pkg/accounting/pricing.go`). Anything else — an internal model,
a renamed SKU, a channel with different billing than the same model's public
API — needs an explicit override in `config.yaml`:

```yaml
pricing:
  models:
    my-internal-model:
      input: 3.00          # USD / 1M tokens
      cache_read: 0.30
      cache_write: 5.00     # fallback for any TTL without its own rate
      cache_write_5m: 4.00
      cache_write_1h: 6.00
      output: 15.00
  channels:
    codex-work:              # channel/model override — more specific than models.*
      gpt-5.6-sol:
        cache_write: 0       # this channel doesn't bill cache writes
```

Precedence, most to least specific:

1. `pricing.channels.<channel>.<model>` in config
2. `pricing.models.<model>` in config
3. the built-in `<channel>/<model>` rate
4. the built-in `<model>` rate
5. no match at all → `unpriced`

**A config override merges onto a matching builtin, category by category** —
if the built-in table already has a rate card for that exact key, an
override that only sets `output` fills every other category (`input`,
`cache_read`, …) from the builtin instead of leaving them unset. This means
a narrow, one-line override doesn't accidentally turn a fully-priced builtin
model into a partially-unpriced one. An override for a key with no matching
builtin (a genuinely unknown model) is used as-is — there's nothing to merge
onto.

A negative rate anywhere in `pricing:` fails config validation at load time.

## Coverage: `unmetered` / `unpriced` / `partial`

Two independent things can go wrong with a given invocation, and afm names
them differently on purpose:

- **`unmetered`** — no usage was ever recorded for it at all: the agent
  exited before a terminal result line arrived, the process was interrupted,
  or the terminal envelope used a schema/contract version this build doesn't
  understand. There are no tokens and no cost to show — not `$0`, an
  explicit unknown (`—`).
- **`unpriced`** — usage *was* recorded (real token counts exist), but no
  rate could be resolved for the model/channel, or the resolved rate is
  missing a category the tokens actually used. The tokens are known and
  shown; the cost is not.

These roll up into a **`coverage`** verdict per stage/run, computed from
invocation *counts*, never from the money value:

| coverage | meaning | example |
|---|---|---|
| `full` | every metered invocation was priced | `$0.3995` |
| `partial` | at least one priced invocation, plus at least one `unmetered`/`unpriced` gap | `$0.3995+` |
| `none` | not a single invocation was priced | `—` |

The `+` suffix on `partial` is a promise: the shown total is a lower bound,
not the whole story — some invocations that happened aren't in it. An
explicit zero rate is a real priced result (`full`, `$0.0000`), not a gap.

Every gap is also visible individually: `afm report`'s "Coverage and
pricing" section lists unmetered/unpriced invocations grouped by
stage/phase/channel/model with a count, and the dashboard's Cost tab shows
the same grouped list as its coverage line (e.g. `codex/gpt-5.6-sol —
unpriced — add a rate in pricing:`).

## Health vs. coverage — two different axes

These are easy to conflate and answer different questions; afm keeps them
strictly separate everywhere (CLI, `/api/status`, dashboard):

- **`coverage`** — *"can I price the invocations I have?"* (`full` /
  `partial` / `none`, from counts, as above). A perfectly healthy run
  routinely has `partial`/`none` coverage — an unpriced Codex model isn't a
  failure, it just needs a `pricing:` entry.
- **`health`** — *"is the accounting writer itself working, and did the
  ledger load cleanly?"* (`ok` / `unavailable`). Once a live write to
  `usage.jsonl` fails, the store latches `unavailable` for the rest of that
  process; the dashboard shows the last good snapshot plus an amber
  "cost may be incomplete" banner. `health` makes **no claim of historical
  completeness** — a crash between a lost write and a restart can't be
  detected after the fact, so reopening the same run after a restart shows
  `health: ok` again (the loadable prefix is intact) even though a write was
  known to have failed in the previous process. `ok` means "nothing is
  currently known to be wrong", not "provably complete".

A run can be `health: ok` with `coverage: partial` (normal — some model just
isn't priced yet) or `health: unavailable` with `coverage: full` (the priced
prefix is fine, but afm knows further writes are failing). Neither axis
implies the other.

## Where cost shows up

**CLI:**

- `afm check` — an `EST. COST` column per stage plus a `TOTAL` line; a
  coverage note (`2 unmetered, 1 unpriced`) when the total isn't `full`.
- `afm report [run]` — a full markdown report: per-stage token/cost table,
  a `Run overhead` section (end-of-run pipelines like agent memory, which
  aren't attributed to any stage), a grand `Total`, and a `Coverage and
  pricing` appendix (rate provenance, unmetered/unpriced invocations,
  reported-vs-estimated mismatches). If `usage.jsonl` is missing, the report
  still renders the stage table with a `No usage data` note; if it's corrupt
  or unreadable, it renders a self-contained blockquote explaining why (no
  invented totals) and exits with a non-zero status.

**Dashboard:**

- **Header "Est. cost" tile** — a `RunMetrics` entry showing the run total;
  always a button that opens the Cost tab; an amber marker whenever there's
  a coverage gap or `health: unavailable`.
- **Per-stage rail figure** — a quiet right-aligned figure next to each
  running/finished stage in the stage list, shown once that stage has at
  least one priced record.
- **Cost tab** — a permanent workspace view: the same per-stage/overhead/
  total table as `afm report`, with each row expandable into a token-mix bar
  (cache-read / cache-write / output / uncached) and the full figure
  breakdown, plus the same coverage line naming exactly which
  model/channel needs a `pricing:` entry.

All three CLI and dashboard totals are computed from the same
`pkg/accounting` helpers (`Coverage`, `DisplayCost`, `CoverageIssue`) — they
are byte-identical for the same run, by construction, not by convention.

## Turning the display off

Cost is shown by default. To hide it everywhere — the dashboard tile/tab/rail,
`afm check`'s cost columns, and `afm report` — set:

```yaml
accounting:
  enabled: false
```

or, without touching config, the environment variable:

```bash
AFM_ACCOUNTING=0 afm run       # hide
AFM_ACCOUNTING=1 afm report    # show on demand, even when config disables it
```

Priority is **env `AFM_ACCOUNTING` > config > default on** (the same shape as
`AFM_FILE_BROWSER`): `AFM_ACCOUNTING=1/true` force-shows, `=0/false`
force-hides, empty/unset lets the config decide.

This is a **display switch only** — collection into
`.afm/runs/<run>/usage.jsonl` keeps running regardless. The data is still on
disk, so switching display back on reveals past runs too:
`AFM_ACCOUNTING=1 afm report <run>` renders a run recorded while the display
was off. With the display off, the dashboard omits every cost field from
`/api/status` (no tile/tab/rail), `afm check` falls back to its pre-accounting
columns, and `afm report` prints nothing to stdout plus a one-line hint on
stderr, exiting `0`.

## Showing tokens without money: `accounting.show_money`

Accounting has two independent display axes. `accounting.enabled` (above) turns
the whole surface on or off; `accounting.show_money` decides whether the
monetary (`$`) figures appear **when accounting is on**. Tokens are always
shown; money is hidden by default.

```yaml
accounting:
  show_money: true   # also show the Est. cost / $ figures (default: false)
```

or, without touching config:

```bash
AFM_ACCOUNTING_SHOW_MONEY=1 afm report   # show money on demand
AFM_ACCOUNTING_SHOW_MONEY=0 afm check    # tokens only
```

Priority is **env `AFM_ACCOUNTING_SHOW_MONEY` > config > default off**
(`=1/true` force-shows money, `=0/false` force-hides, empty/unset lets the
config decide). Like `enabled`, this is presentation only — the estimated cost
is still computed and recorded in `usage.jsonl`, so turning money back on
reveals it for past runs too.

With money **off** (the default) but accounting on:

- **Dashboard** — a new **Est. tokens** header tile (compact `1.2M`/`345K`
  format) is shown, placed before Est. cost; the **Est. cost** tile, the Cost
  tab's `Cost` column, and the per-stage rail's `$` figure are all hidden.
- **`afm check`** — drops the `EST. COST` column but keeps `TOKENS`/`CACHE`.
- **`afm report`** — omits every `$`/Est. Cost figure while still printing all
  token output (per-stage, overhead, totals, and the coverage appendix, which
  still notes reported-vs-estimated mismatches by fact even without the
  numbers).

With money on, everything described earlier under "Where cost shows up" applies
as written.
