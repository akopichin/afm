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
- `peer_waits` on a stage transition into `awaiting_peer` (a list of wait ids,
  omitted when empty) names the waits the parked stage's continuation depends
  on: the waits its last turn ended for. The stage continues when one of them is
  answered or ends, or when a message that should wake it arrives. A park from
  an older journal, or one for unread messages, has no `peer_waits`; recovery
  then uses the earlier rule.
