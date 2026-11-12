# AFM contracts

This independently versioned public Go module supplies the values shared by AFM,
its adapters, and the private execution engine. It does not depend on the AFM
application or the private engine, and its tests require no private credentials.

`state`, `control`, `events`, `graph`, `activity`, `agent`, and `assistant` contain
snapshot, command, event, and error types. `flow` and `config.Execution` describe
execution inputs. Snapshots are values; there is no engine Store or writer handle
in these contracts. `agentapi` provides the common agent protocol adapter used by
both AFM and embedding hosts.

Compatibility packages retain the shared configuration, accounting, browser,
Docker, lifecycle-hook, and filesystem utilities used by both repositories.
They are public code, not a promise that the private engine is independently
buildable from this module. Orchestration, agent execution, state mutation and
replay remain in `afm-engine/internal`.

Run `GOWORK=off go test -race -timeout 15m ./...` to validate this module on its own.
JSON Schemas are under `schema/`; stage status values live in `state/statuses.go`.
Keep these sources and the AFM dashboard generator in sync when contracts change.

For a nested-module release, the tag prefix is `contracts/` (for example
`contracts/v0.1.0`). A commit-derived pseudo-version can also be pinned. Publish
the public contracts revision before an engine version that requires it. Local
unpublished revisions can be tested with a workspace or a local module proxy;
that does not make them downloadable from GitHub.

## Budgets

Spend limits are declared with an optional `budget` block (`tokens`, `money`,
`stop_policy` in YAML; the money field is named `money_usd` in JSON). The contracts only define values, parsing, attribution and error
codes; enforcement, journaling and the read model live in the engine.

Public API map:

- `budget`: `Spec` (value type, `==` comparable; `Policy` normalizes to `hard`),
  `ParseTokens` (`500k`, `1.5m`), `ParseMoney` (`5$`), `ScopeID` (`run`,
  `stage:<id>`, `task:<id>`), `Override` (`none`/`limit`/`unlimited`), `ResolveBase`,
  `Effective`, `Spend`, `Evaluate`, and the engine/HTTP DTOs (`View`, `UpdateRequest`,
  `ResumeRequest`, `UpdateResult`, `ExportResult`, `Notice`, hold reasons, actions).
  The package imports only the standard library and `yaml.v3`.
- Declarations: `flow.Flow.Budget`, `flow.Stage.Budget`, team members, stage assistants,
  `config.ProfileConfig.Budget` and `assistants.Profile.Budget`. Creation requests carry
  it in `control.AttachRequest`, `AttachAndSendRequest`, `AddRunAgentRequest` and
  `graph.StageDraft`. `assistants.Task` has no budget, and requests whose parameters are
  chosen by an agent (spawn, delegate, proposals) cannot set one.
- `accounting`: `CheckExplicitRates` (`complete`/`missing`/`model_unknown`),
  `RateSnapshot.Explicit`, `ScopesFor` (the single attribution rule) and `SpendByScope`.
  `config.Config.UsageHint` resolves the channel/model of an agent command.
- Errors: `assistants.CodeSpendLimitReached` (HTTP 409) and `CodeRatesMissing` (422)
  flow through the existing operation-rejection pipeline.

Compatibility rules:

- New fields are pointers with `omitempty`, so flows, profiles and tasks without a budget
  serialize exactly as before; usage records without `explicit` read as `false`.
- Money is accumulated and compared in integer nano-USD; the warning threshold is 80%.
  Limits are capped at 2^53-1 tokens and $1e9.
- A money budget is enforced only for models whose config rate card sets `input`, `output`,
  `cache_read` and `cache_write`; built-in prices never satisfy it. Built-in cache-write TTL
  rates (`cache_write_5m`/`cache_write_1h`) may still refine the cost. Prices and analytics
  output are unchanged.
- An explicit budget on a declaration replaces the profile default as a whole; an override
  replaces the effective limit as a whole.
- `schema/` copies must match `contracts/schema/` (`scripts/sync-schemas.py`).

Publish this revision (suggested tag `contracts/v0.2.0`) before an engine version that
uses it.
