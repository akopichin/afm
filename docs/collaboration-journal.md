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
