# The event log

The state of every run is written to an append-only event log,
`.afm/runs/<run>/events.jsonl` — this is the **single source of truth**. Every FSM
transition (stage started, plan ready, approved, done, failed, …) is appended as one
JSON line and `fsync`'d before control returns. `state.json` sitting next to it is a
**derived cache**; read paths (`afm check`, run lookup) reconstruct state by replaying
the log, not by trusting the snapshot.

This is what makes long runs reliable:

- **Resume from the exact point.** If a run is interrupted (crash, `Ctrl+C`, killed
  container), the next `afm run` replays the log, skips completed stages, and restarts
  interrupted ones — see [Stage lifecycle → Resume](../stage-lifecycle.md#resume-on-restart).
- **Durable intent.** Approve/revise/retry are committed to the log (with fsync) *before*
  the command returns, so a crash immediately after approval doesn't lose the intent —
  recovery continues from the correct state.
- **Cross-process safety via flock.** While `afm run` is active it holds an exclusive
  `flock` on `<run>/.lock` for the whole run. A concurrent CLI `approve`/`retry`/`revise`
  from another process fails with a clear "run is locked" message instead of corrupting
  the live log. The lock is released by the OS on process exit — a crashed run leaves no
  stuck lock. `afm check` is read-only and never blocked.
- **Non-destructive replay.** A truncated tail (a crash mid-append) is safely dropped. A
  corrupt *complete* line in the middle of the log is quarantined into
  `events.jsonl.corrupt-<ts>` rather than overwritten — afm never destructively
  truncates the log.
- **Stable start time.** A run's start time is the time of the log's first record
  of any kind — a stage transition, a run event or a control record such as the
  board task protocol marker — so a live run, a resumed run and `afm check` all
  report the same start.
- **Stage time is derived, not stored.** The per-stage time shown by the
  [dashboard](../dashboard.md#stage-time) adds no event type. It is a projection
  of the same records: every record of any kind (a transition, a control record or
  a run event) closes the interval a stage was in and opens the next one, at the
  record's own time, rounded the same way live and on replay so both give identical
  figures. A resumed run does not count the gap between runs. After a crash (a log
  with no terminal run event) **Run elapsed** and the stage intervals stop at the
  last record written before the next start. A stage that started before the log had run events, or that was restored from a
  migrated `state.json`, has no recorded past, so it is reported as incomplete
  rather than as `0s`. An offline CLI transition made on a crashed run
  is also a record, so it moves that boundary.
- **Unique run ids.** `<run-name>-<timestamp>-<rand4hex>`, where `<run-name>` is
  the `--worktree` name when supplied and the flow name otherwise. The random suffix
  avoids collisions even within the same second.

Because everything an agent received and did is on disk (prompts under `--debug`, tool
actions in `<phase>.jsonl`/`.log`, transitions in `events.jsonl`), a run is fully
auditable after the fact.
