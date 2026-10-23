# Agent prompt loops: persistence and scheduling

This change adds recurring prompts to existing Agent Mesh participants. It
does not change whole-flow `repeat`. User-facing syntax, scope and API examples
are in [Repeating prompts](../../loops.md).

## Ownership and admission

The dashboard and agent CLI share `CreateLoop`, `ListLoops` and `StopLoop` in
the orchestrator Service. Adapters supply the human or authenticated token
actor. Creation resolves aliases to participant IDs once. The human names a
target; an agent may omit it to target itself. Only run agents and stage helpers
are eligible targets. The Feed restricts helper suggestions to its own stage.

A loop is owned by its creator for visibility and stopping rights, but follows
its target for lifetime. A helper-created loop aimed at a run agent can outlive
the creator. The human sees and stops all loops. Agents see and stop their own.

At most 16 schedules occupy slots. A reversible end, such as a failed task,
keeps its slot because Retry can reactivate it. A permanent end or explicit
stop releases the slot. Creation cannot start with an ended or unreachable
target. Prompts are redacted before persistence and bounded below the message
body limit to leave room for the visible tick header.

## Durable facts

`loop_created`, `loop_ticked` and `loop_stopped` are required journal effects.
Creation retains the creator, resolved target, interval, redacted prompt,
optional stage bound, origin Feed and dedicated thread ID. Its operation ID
determines the loop key; creation order determines the short ID. The operation
commit time anchors the schedule.

A delivered slot uses deterministic operation/message IDs derived from loop
key and slot. Thread creation, accepted message receipt, slot advancement and
an optional helper status restart are one atomic run operation. The store
precondition rechecks current eligibility, cursor and helper status. A failed
CAS skips the slot. Storage failure ends the run through its fatal path.

The projection derives implicit end reasons from current flow, stage, target
and thread state. It does not append end or skipped-slot records. The next
slot to evaluate is process-local: recovery intentionally starts at the first
future slot without catching up missed work.

## Scheduler lifetime and pause

One goroutine belongs to `Orchestrator.Run`. It uses the shared clock seam and
a wake signal for create/stop; timers are stopped when interrupted. Shutdown
cancels and joins this scheduler before agent and Store cleanup. Dormant loops
do not participate in the decision to keep a flow open.

Each loop's slots remain fixed to its creation anchor. Busy, held, paused and
unread recipients skip slots; a durable mesh wait is ready for ordinary wake
delivery. A late scheduler attempts only the latest due slot. The next-tick
view describes a scheduled opportunity, not a promised delivery.

`flowPauseMu` spans tick admission and commit. Checking the published pause
marker alone is insufficient: PauseFlow holds this lock while pausing stages
and publishes its marker at the end. This preserves the existing outer pause
lock / inner Store lock order.

## Helper and watcher races

Successful one-shot helpers and resting watchers reuse their existing task and
saved profile on a tick. Failed/cancelled tasks and superseded generations do
not restart. Every required helper's own unresolved gate is checked, including
when a different helper is the first root blocker. A loop cannot silently
replace a human Revise/Retry/waiver decision.

A watcher collector can already have popped a checkpoint while its task still
appears resting. A loop skips an already Busy collector. A loop restart claims
Busy before scheduling its turn, and normal turn settlement releases it.

There remains a competing ordering where a checkpoint commits while the loop
restart wins the watcher's fire CAS. An already committed checkpoint cannot
go back to the collector's pending queue: its cursor cannot be committed
twice. Instead `commitAndFire` registers a fire debt and claims Busy under the
binding's `fireMu` before reading status. Either the fire path observes an
already resting task and retries, or the competing turn's settlement sees the
debt and retries it. Both consumers use the same lock. A losing CAS must never
clear Busy while the competing turn is still active.

The debt is process-local, like the watcher binding. A crash after checkpoint
persistence but before its queued fire can still lose that watcher pass. This
predates loops; durable checkpoint reconciliation is a separate follow-up.
Checkpoint content also remains outside the helper prompt, as before this
change. Neither limitation weakens the atomic loop-message/slot contract.

## Conversation isolation

Each loop derives a dedicated thread and opens it on its first tick. Ordinary
new sends exclude loop threads from reuse so unrelated messages cannot consume
their budget or look like unread ticks. Explicit replies remain ordinary mesh
replies. At the existing 200-message cap the loop ends instead of rotating
threads, reserving space for a tick and its answer before admission.

Only a tick tied to its saved creator, target and thread may self-address.
Ordinary self-sends remain rejected. Automatic turn replies skip a self-tick
or a tick from a creator that is no longer reachable, preventing reply loops
and repeated delivery failures after a helper creator finishes.

## Feed boundary and validation

Only a leading `/loop` token, allowing leading whitespace, enters command
mode. `/loops` stays normal text. Command mode bypasses recipients, attach,
reply and Interrupt paths. Revise rejects the command before invoking its
normal handler. The first target mention is resolved; later mentions stay in
the prompt. Create and stop use the existing retryable request-ID mechanism.

Tests cover projection replay, idempotency, ownership, slot timing/recovery,
atomic delivery, required helper gates, watcher/CAS races, pause activation,
CLI/HTTP contracts, parser boundaries and composer routing. The live scenario
checks real recurring replies across `hold` and `handoff`, listing from a done
Feed, stopping from another Feed, reload persistence and final script execution.
The receipt regression also covers a valid stage whose optional name is empty.
