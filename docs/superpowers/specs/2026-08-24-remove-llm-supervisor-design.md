# Removing the LLM supervisor

## Context

afm has two independent mechanisms for running a stage "autonomously"
(single-shot, skills-driven, no plan.md/implementation/review cycle):

1. **Static `agents: [auto]` track** — a stage explicitly declares `agents:
   [auto]` in flow.yaml. No LLM call decides this; it's a hardcoded routing
   choice, validated at parse time (mutually exclusive with `supervisor:
   true` and `script:`).
2. **LLM supervisor** — a stage sets `supervisor: true`; before it starts,
   afm calls an LLM (`pkg/orchestrator/supervisor.Supervisor.EvaluateStage`)
   to decide whether the stage *should* run autonomously or fall back to the
   normal planning → implementation → review cycle.

Practice has shown the LLM supervisor adds a call, a config surface, and a
failure mode (`EvaluateStage` erroring, falling back) without providing
value the static `agents: [auto]` track doesn't already cover more simply
and more predictably. This spec covers removing it cleanly, leaving the
static `auto` track fully intact.

Non-goal: this does not touch `agents: [auto]`, `runAutonomousAgent`, or the
`autonomous.flag` file mechanism — those are shared infrastructure the
static track keeps using unchanged.

## Investigation findings (why this is safe)

- `startWithSupervisor` (`pkg/orchestrator/supervisor_track.go`) is called
  from exactly 3 places (`scheduling.go:139`, `recovery.go:160,191`), all
  gated on `stage.NeedsPlanning()` being true. It calls
  `DetermineStagePhases`, which — with no supervisor configured
  (`!s.Supervisor || o.supervisor == nil`) — returns immediately with
  `base := AgentTypesToStrings(s.Agents)`. `startWithSupervisor`'s `else`
  branch never inspects `base`'s contents beyond the single sentinel check
  `len(phases)==1 && phases[0]==phaseAutonomous`; it unconditionally calls
  `o.runPlanningAgent(ctx, s)`. So with the LLM path removed,
  `startWithSupervisor` collapses to a literal pass-through:
  `func (o *Orchestrator) startWithSupervisor(ctx, s) { o.runPlanningAgent(ctx, s) }`
  — redundant, not just simplifiable. The 3 call sites can call
  `o.runPlanningAgent` directly instead.
- `flow.Stage.Agents == ["auto"]` never reaches `startWithSupervisor` at all
  (routed via `activateAutoStage`/`tryActivatePrePlanned` instead), so
  deleting the LLM path cannot affect the static auto track's dispatch.
- `EvSupervisorApproved` (FSM event) is triggered from exactly one call site
  (inside `startWithSupervisor`'s now-dead LLM-approved branch) and appears
  in exactly one transition-table row (`Planning -> Ready`) and one test
  case. Safe to delete outright — no other code depends on that specific
  event reaching that transition (the normal `EvPlanReady` path is separate
  and untouched).
- `pkg/flow.ParseFile` uses non-strict `yaml.Unmarshal` (no
  `KnownFields(true)`). Removing the `Supervisor`/`SupervisorPrompt` struct
  fields from `Stage` means an old flow.yaml with `supervisor: true` simply
  has that key silently ignored by the primary decode — the stage runs its
  normal `agents:`-track. No parse error, no crash. (We add an explicit
  warning on top of this — see Backward compatibility below.)

## Scope of removal

- `pkg/orchestrator/supervisor/` package, in full (`Supervisor`,
  `EvaluateStage`, `EvaluationResult`, `AgentTypesToStrings`,
  `NewSupervisor`).
- `pkg/orchestrator/supervisor_track.go`, in full (`DetermineStagePhases`,
  `startWithSupervisor`, `logSupervisorDecision`).
- `flow.Stage.Supervisor` / `SupervisorPrompt` fields and their validation
  rules (incompatibility checks vs. `agents:[auto]` and `script:`);
  `flow.Flow.SupervisorCommand`.
- `config.SupervisorConfig` / `config.Config.Supervisor` and its merge
  logic; `Options.SupervisorRunner` field on the orchestrator; the
  unconditional `supervisorRunner := executor.New(...)` construction and
  `resolveSupervisorCommand` in `cmd/afm/run.go`.
- FSM: `bus.EvSupervisorApproved` constant and its transition-table row.
- Bus: `bus.EventSupervisorDecision` event type.
- Server read path for historical supervisor data: `supervisor.jsonl`
  reconstruction in `pkg/server/events_handler.go` and `pkg/server/handlers.go`
  (full removal, not "read old data" compat — see Backward compatibility).
- Supervisor-specific tests: `pkg/orchestrator/supervisor/supervisor_test.go`,
  `pkg/orchestrator/supervisor_orchestrator_test.go`,
  `pkg/orchestrator/integration_supervisor_test.go`, plus the supervisor-only
  cases inside `pkg/orchestrator/bus/fsm_test.go`, `pkg/flow/flow_test.go`,
  `pkg/flow/marshal_test.go`, `pkg/config/config_test.go`,
  `pkg/docker/wrapper_test.go`, `pkg/executor/debug_test.go` (only the
  supervisor-labeled cases in each mixed file — the rest of each file is
  untouched).

## Explicitly preserved

- `autonomous.flag`, `isAutonomousStage`, `runAutonomousAgent`,
  `activateAutoStage` — all unchanged. These belong to the static `agents:
  [auto]` track, which the LLM supervisor never owned.
- `flow.Stage.IsAuto()` and all `agents:[auto]`-specific tests
  (`pkg/flow/auto_test.go`, scenario/integration tests seeding
  `autonomous.flag` for auto stages) — untouched.
- All non-supervisor dispatch logic in `scheduling.go` / `recovery.go`
  (`tryActivatePrePlanned`, `startReadyStages`, `resumeStageAtStatus`, etc.)
  — untouched except the 3 call-site edits below.

## Call-site changes

`scheduling.go:139`, `recovery.go:160`, `recovery.go:191` each currently
spawn `o.startWithSupervisor`. Each becomes a direct spawn of
`o.runPlanningAgent` — same `SpawnAgent(ctx, s, ...)` call shape, only the
target function changes. No change to the surrounding CAS/guard logic in
any of the three call sites.

## Backward compatibility

1. **Historical run data** (`supervisor.jsonl`, `EventSupervisorDecision`
   entries in old `events.jsonl`): the server-side read/reconstruction code
   is deleted outright. Opening an old run's dashboard simply shows no
   supervisor-decision entries for it — not an error, not a 500; the rest of
   that run's JSON is independent and parses normally. Chosen over
   preserving a read-only compat path because a full clean removal is
   simplest, and the affected records (a per-stage LLM decision log) are a
   diagnostic nicety, not load-bearing run history.
2. **Old flow.yaml files with `supervisor: true` / `supervisor_prompt:` /
   `supervisor_command:`**: after the struct fields are removed, the
   default non-strict YAML decode silently drops these keys and the stage
   runs its normal `agents:`-track — identical to today's behavior when
   `o.supervisor == nil`. On top of that silent-safe default,
   `flow.ParseFile` gets an explicit non-fatal warning: a second decode pass
   of the same YAML bytes into a generic structure, checking each stage (and
   the flow root, for `supervisor_command`) for these now-unknown keys, and
   printing a `WARN` to stderr naming the stage id and the stray key(s) if
   found. This never fails parsing — it's purely informational, so a user
   with a leftover `supervisor: true` in an old flow.yaml gets a clear
   signal about what changed instead of silently-different-but-unexplained
   behavior.

## Testing

- Delete supervisor-only test files outright; remove supervisor-only test
  cases from mixed files (leaving everything else in those files untouched).
- `pkg/flow/flow.go` has no existing precedent for a non-fatal parse
  warning (no `log.Printf`/stderr writes anywhere in the file today) — this
  is the first one. Print via `fmt.Fprintf(os.Stderr, "WARN: ...")`,
  matching the `WARN:` prefix convention already used elsewhere in the
  codebase (e.g. `pkg/orchestrator/dialog_poller.go`'s `log.Printf("WARN:
  ...")` calls), for consistency across afm's own warning output.
- Add a new test locking in the `ParseFile` warning: a flow.yaml with a
  stage-level `supervisor: true` and/or `supervisor_prompt:` (and a
  flow-level `supervisor_command:`) parses successfully with no error, the
  resulting `Stage`/`Flow` structs have no trace of those values, and the
  warning naming the affected stage id and key is observed by capturing
  `os.Stderr` around the `ParseFile` call (redirect to a pipe/temp file,
  restore after).
- Full `go test ./...` must pass with zero regressions in the remaining
  tracks: static `agents:[auto]`, script stages, pre-planned
  (`plan != ""`) stages, and the standard planning → implementation →
  review cycle. This is the acceptance bar for "nothing degraded."
- `AGENTS.md` gets a pass to remove supervisor mentions; the existing
  `agents:[auto]` section already contrasts itself against
  "supervisor/LLM-supervisor" — that wording is corrected since there is no
  longer an LLM-supervisor to contrast against.

## Out of scope

- No CLI flag, env var, or config migration tooling is added — this is a
  pure deletion, not a deprecation cycle with a transition period.
- No changes to `agents: [auto]` behavior, semantics, or tests beyond what's
  needed to confirm it still passes untouched.
