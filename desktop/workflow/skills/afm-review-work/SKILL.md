---
name: afm-review-work
description: Perform a bounded read-only consultation or independent review in an AFM helper or verify turn, using actual changes and evidence and returning the injected result contract. Not an implementation or coordination skill.
---

# Bounded independent review

The assignment selects the question/perspective, candidate, reviewed files,
criteria, evidence, check budget and expected result. Review that scope against
the approved requirements and project rules. Inspect actual changed/new code and
integration points, not only summaries. Do not broaden the task into speculative
polish or demand ceremonial reviews that the risk policy did not select.

Use the assigned check-evidence skill and the completed-command ledger before
running anything. Existing successful evidence needs no duplicate suite, build,
lint, stress or mutation pass. When a concrete unresolved concern needs an
experiment, use a bounded targeted reproduction within the granted scope. If a
required check is missing, identify it rather than silently running every suite.

## Threshold for findings across perspectives

On the first pass, inspect the assigned perspective across the approved scope.
Report confirmed critical or major findings together. Critical means a reachable
failure with severe consequences such as data loss, unauthorized access or an
unusable product. Major means a supported scenario produces a materially wrong
result, a required behavior is missing, or an explicit acceptance gate fails.
For tests, identify the regression that would escape; for design, docs or UI,
identify the supported task or contract that breaks. Apply the same threshold
to baseline defects when they prevent acceptance of this candidate.

For each blocker give the trigger, affected criterion or supported behavior,
expected and observed consequence, and evidence at a concrete location. A traced
execution path or a contradiction in the actual artifact can establish impact.
Check callers, existing safeguards and approved design decisions first. A
preference, hypothetical extension, naming nit or unrelated historical issue
does not warrant a finding, repair or escalation. Enforce mandatory checks as
specified; record missing required evidence as incomplete under the injected
contract. A green check alone does not establish scenario coverage.

For scoped check evidence, inspect changed inputs and their dependencies.
Reuse unaffected observations; request a broader check only when the change
can invalidate them or the approved acceptance contract requires it.

Use the existing ledger. Merge reports of the same cause and impact across
perspectives into one repair. On a correction pass, verify the original finding
and affected integration paths; raise a new blocker only with evidence that the
repair introduced or exposed a material defect there. Reopen a disposition only
with new evidence. When required checks are satisfied and no blockers remain,
return the successful outcome with an empty findings list and end the review.

Stay read-only: no product edits, commits, native subagents, delegation, nested
orchestration or process-name kills. Helpers and verify agents have no managed
browser; use the author's captured evidence. Run permitted repros only where the
execution boundary allows them. A read-only prompt is not OS isolation. If you
cannot write scratch files, return the findings directly for the coordinator to
save; do not invent artifact paths or seek broader permissions.

Before an expensive batch and final delivery, read updated brief/inbox guidance
when AFM messaging is available. Ask only a necessary scoped callback question,
using the granted mesh tools and a finite wait; do not wait for a future assignment
or acknowledgement of the finished result. Once answered or optional work is
retired, reconcile your own waits via granted recovery capabilities. Never borrow
operator/other-agent credentials or edit runtime state to clear a wait.

Return ONE final result using the contract injected into this invocation (helper
and verify contracts may differ). An empty findings list is valid for a completed
review. Acknowledgements and partial replies are not final deliverables. Preserve
completed findings and use the contract's inconclusive outcome for incomplete
work; cancellation, timeout and missing access do not become PASS. Do not create
a stage completion marker or a new request merely to deliver your result.
