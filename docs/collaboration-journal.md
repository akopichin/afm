# Collaboration journal compatibility policy

Collaboration (helpers working on a stage) writes control records to a run's
durable `events.jsonl` journal, alongside stage transitions. This document
describes the compatibility policy for those records.

## What's promised

- **A new afm reads old runs.** Opening a run that predates collaboration (no
  collaboration records at all) always works: there is nothing to interpret, so
  there is nothing that can be incompatible.
- **A new afm reads old collaboration records.** Once collaboration records exist,
  afm commits to reading every `schema_version`/`kind` combination it has ever
  shipped. Dropping support for an old record shape is a breaking change, not a
  routine release.
- **Older binaries cannot read collaboration journals.** A release from before
  collaboration existed has no reader for these control records.

## What's NOT promised

- **An old afm reading a new journal is not promised.** If a run was produced by a
  newer afm that added a record `kind` or bumped a `schema_version` an older
  binary has never seen, the older binary is not expected to make sense of it.
  Point releases within the same major collaboration schema generation are
  expected to stay forward-readable in practice, but that is not a contract —
  only the "new reads old" direction above is.

## How an unknown record is handled

An unrecognized **required** `kind` or `schema_version` on a collaboration control
record is an **explicit incompatibility error**, never a silent skip. Silently
ignoring a record whose meaning the binary doesn't understand risks acting on a
tree of tasks that is missing pieces it doesn't know it's missing (for example: a
required child task recorded under a `kind` this binary can't parse would silently
stop blocking root-stage completion). Concretely:

- Decoding a collaboration record with a `kind` outside the set this binary knows →
  stop and report incompatibility (e.g. `afm check`/`afm run` fail loudly with the
  offending `kind` and `schema_version` named), not "pretend the record wasn't
  there."
- This mirrors the discriminator rule in the assistant **result** envelope itself
  (`schema/assistant-result.schema.json`): `kind` is a required field, and an
  unknown or missing `kind` is rejected rather than guessed at from which other
  fields happen to be present.
- Optional, additive metadata on an otherwise-recognized record kind/version (a
  new, non-required field) may be ignored by an older reader — that is the
  ordinary, unremarkable kind of forward compatibility every `events.jsonl`
  consumer already relies on. The strict rule above is specifically about the
  *discriminators* (`kind`, `schema_version`), not about every field on a record.

## Conversation topic edits

Editing or clearing a Team conversation topic commits a
`mesh_thread_title_updated` effect in an operation. Its payload contains the
replacement `title` (empty to clear) and `expected_title_seq`, the previous
topic revision. The effect's journal sequence becomes the new revision. Replay
checks that revision and stores the display-title override separately from the
original `mesh_thread_opened` descriptor, which remains unchanged. A topic edit
also advances the thread metadata cursor, so a stage feed can see the new title
without a new message; it does not change message order.

This is a new control kind, not an additive field on `mesh_thread_opened`.
Current afm can replay older journals with no topic edits. An older binary that
does not recognize `mesh_thread_title_updated` cannot replay a journal containing
it: the unknown kind produces the incompatibility error described above. Use a
binary that knows this kind to resume such a run.

## Board task records

A run started by an afm with [typed board tasks](collaboration.md#typed-tasks)
begins its journal with `mesh_board_protocol_set` (`protocol: "typed_v1"`),
written before any agent starts. It is recorded only for a fresh run: a resumed
run whose journal has no such record stays a legacy run, and its `task/` board
entries keep the free-text convention.

Each typed task change commits one or more effects in an operation:
`mesh_board_task_created`, `mesh_board_task_assigned` (a claim is an assignment
with `start: true`), `mesh_board_task_started`, `mesh_board_task_progressed`,
`mesh_board_task_completed`, `mesh_board_task_released`,
`mesh_board_task_reopened` and `mesh_board_task_edited`. The target names the
task key and the participant making the change; every payload carries the
`expected_version` it was based on. An assignment to another participant
commits in the same operation as the message that tells it; a coordinator notice
does too, unless it was skipped. A retry under the same request id is answered
from the committed operation, so it never applies twice.

Replay applies the same rights and state rules as a live change, so it rebuilds
exactly the tasks the run had. Format rules — the key's charset and length, text
sizes, the number and location of artifact references, required texts — are
checked only when a new record is admitted, never on replay: a journal written
before such a rule was tightened still replays. The key format is checked only
on create, so an existing task stays changeable.

These are new control kinds: an older binary cannot replay a journal that
contains them and reports the incompatibility described above.

## Additive fields

Fields added to an existing record kind are optional: a journal written before a
field existed simply lacks it, and afm reads the record with the field's zero
value.

- `auto_result` on `mesh_message_sent` and on `discussion_message` (a boolean,
  omitted when false) marks a run agent's turn result that afm posted as a reply
  on the agent's behalf. Such a message waits in the recipient's inbox but never
  wakes it. A message from an older journal has no `auto_result` and is an
  ordinary message that wakes its recipient, exactly as before.
- `readonly_enforcement` on the `task_status_changed` record that starts a
  helper turn (`os` or `none`, omitted when empty) is the read-only guarantee
  the environment gave that turn. It can differ from the guarantee recorded
  when the task was admitted: a task admitted on the host under the Codex
  sandbox and resumed in a container has none. The roster shows a finished
  task with the value of its last turn; a task from an older journal shows the
  admission-time value.
- `origin_stage` on `mesh_thread_opened` (a stage id, omitted when empty) is the
  stage the conversation was opened from: the sending agent's stage, the stage
  whose feed the human opened it in, or the stage of a Discussion thread. The
  first open decides it: a later open of the same thread with another (or no)
  `origin_stage` is still a no-op, so two agents of different stages starting
  the same conversation at once, or a post re-opening a Discussion thread from an
  older journal, do not conflict. A thread from an older journal has no
  `origin_stage` and shows in a stage's feed by its members alone.
- `origin_stage` on `mesh_message_sent` (a stage id, omitted when empty) is the
  stage feed the human wrote a message from without naming a thread. It records
  the request's intent so that a retry under the same request id from another
  feed is rejected as a conflict instead of replaying the first send. Projection
  ignores it; a message from an older journal has none.
- `recipient_reply_only` on `mesh_wait_registered` (a boolean, omitted when
  false) is set on every new `reply_to` wait: only a reply from a recipient of
  the awaited message answers it. A `reply_to` wait from an older journal has no
  `recipient_reply_only` and keeps its old rule, so any reply answers it, the
  human's included. Replay resolves its waits exactly as they were resolved
  when the journal was written.
- `batch_id` on `mesh_wait_registered` (omitted for single waits) groups the
  existing `reply_to` wait records of one `reply_to_all` call. One `mesh_wait`
  operation commits one `mesh_wait_registered` effect per request, each with its
  own wait ID and a shared batch ID and optional deadline. Each wait retains
  its own projected status and reason; later resolutions use the existing wait
  records. Replay restores the group and each item's outcome in registration
  order; no separate aggregate wait record or cache is written. Older records
  without `batch_id` remain single waits.
- `peer_waits` on a stage transition into `awaiting_peer` (a list of wait ids,
  omitted when empty) names the waits the parked stage's continuation depends
  on: the waits its last turn ended for. Separate waits are alternatives: one
  answered or ended wait can continue the stage. Waits with the same `batch_id`
  form one unit, which continues the stage only after every item has answered,
  expired, or been cancelled. A message that should wake the stage can also
  continue it; replies to an unfinished batch do not wake it early. A park from
  an older journal, or one for unread messages, has no `peer_waits`; recovery
  then uses the earlier rule.
