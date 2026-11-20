---
name: afm-check-evidence
description: Assign, execute and reuse checks in an AFM development flow without duplicating suites, losing exit codes or weakening acceptance gates. Use for coordinators, writers and reviewers handling test evidence.
---

# Check ownership and evidence

The calling flow and project instructions define required checks and budgets.
This skill governs their execution and evidence; it never removes a required
check or treats a timeout as success.

## Assign once

- Give each check one owner for the candidate inputs and environment. Writers
  run affected tests; reviewers inspect the actual change and existing evidence,
  adding only a discriminating repro for a concrete unresolved concern. Broad
  suites, builds and linters belong to the declared stage gate.
- Before assigning work, reconcile completed commands and active jobs, not just
  what participants were instructed to run. Hand reviewers the writer's actual
  evidence, including checks completed before new guidance arrived. Never assert
  that a check was not run without inspecting the record.
- Record command/selector, input identity, environment, owner, actual exit code,
  duration and log path in the flow's ledger. Assign missing checks and combined
  integration coverage; do not run the same check concurrently on the same inputs.
- Reuse a successful result only for matching inputs, contract/check group where
  applicable, and a confirmed unchanged toolchain/dependency environment. HEAD
  alone does not identify a dirty tree. Update the environment identity when it
  changes. Inspect check-generated edits before accepting the candidate.
- Declare normalizing generators/builds in a group's `prepare` commands when
  they rewrite tracked outputs. Run helper prepare, inspect the changes, then
  freeze/review the candidate before broad checks. Preparation is not test evidence.
- Scoped `input_paths` may reuse evidence across unrelated edits only after review
  confirms the complete dependency closure (code, configuration, assets and build
  inputs). Default to the whole tree when uncertain; final checks stay unscoped.
- For mixed stacks, declare dependencies per implement/docs command instead of
  tying every check to one whole-tree receipt. Include shared interfaces, manifests,
  lockfiles, generators and tool configuration. The helper reuses successful commands
  with matching content, command, contract and environment, retaining original logs;
  a stale group receipt does not mean every command must execute again. Final gates
  remain whole-tree. Do not narrow scopes after a failure just to reuse old evidence.
- Keep stateful commands serial. Parallelize only independent read-only checks with
  disjoint resources and a bounded worker count; builds, formatters and shared fixture
  tests need their existing ordering. More workers do not fix repeated I/O.

## Bound experiments

Use the calling flow's repeat, review and time budgets. Otherwise default to one
focused run, with race checking when required. Repetition needs a named flake or
concurrency hypothesis, an exact selector and a finite budget; it is not a
substitute for a deterministic reproduction. Prefer controlled gates or fake
clocks to blind stress loops. Preserve real shutdown guards and assertions.

Mutation testing needs an explicit coverage question, a small bounded mutant set
and one assigned owner, normally the tests reviewer. Consume existing results;
repeat only when changed inputs invalidate them or a new finding needs a specific
extra mutant. Do not prescribe mutation testing for every change.

Before another expensive batch, read the incremental AFM inbox and latest brief.
Apply changed guidance before starting new work, and report already-running
checks. Do not poll logs/processes or create no-op monitoring jobs every few
seconds. Bound commands and clean up only owned processes using their saved
identity, never name patterns.

## Wait for checks

Await the original tool job/session ID and collect its terminal exit code. For a
shell child, save its PID and use `wait "$check_pid"` in the same shell. If only a
result file is available, use a finite deadline and validate that the receipt
belongs to the intended invocation and candidate inputs; an old receipt does not
prove that an active job finished.

Never wait with `pgrep -f "helper.py checks"`: the pattern occurs in the waiting
shell's own command line, so the loop can match itself forever. For a one-shot
process diagnostic, use `pgrep -f "[h]elper.py checks"` to avoid that self-match.
Process-name matches are not completion evidence or a way to select cleanup PIDs.

Before writing `execution_summary.md` or the final reply, finish and reap owned
background checks and stop obsolete monitoring jobs by their tool job ID or saved
PID. Collect their terminal status; leftover background jobs can keep the agent
process alive after its final answer and prevent the stage from completing.

## Keep evidence honest

Capture the command's exit code before filtering output; the success of tail,
grep or tee is not the command's result. Keep credentials out of argv and logs.
Preserve failing evidence and partial findings if a budget expires, and name the
missing evidence rather than granting PASS.

A green retry does not establish an environmental failure or diagnose a flake.
Separate product/test lifecycle defects from contention and infrastructure with
evidence. Do not raise timeouts, weaken assertions or exclude tests merely to get
green. Diagnostic selectors do not replace required unfiltered gates.

Reviews return concrete blockers together, without speculative polish. Further
rounds need a named unresolved defect, remaining checks and a revised budget;
renaming the task or reviewer does not reset the repair count. Recompute budgets
when scope grows. Failures in several sites of one lifecycle invariant call for
diagnosis of that invariant before another series of local guards.
