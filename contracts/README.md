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
