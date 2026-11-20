# Temporary stage helpers

Spawn only specialists selected by the calling flow's risk policy. Each is a
bounded read-only consultation/review, not an implementation writer. Instructions
must name the perspective, exact question, owned review scope, baseline/current
diff, criteria, prior findings/hypotheses, evidence paths, brief revision, allowed
repros and check/review budget. No managed browser, project edits, commits, native
subagents, delegation, nested orchestration or process-name kills. Read-only is a
task restriction, not a claim of OS isolation in every execution environment.

Helpers and verify agents do NOT inherit the stage's skills or library. Include
the exact review/check skill paths supplied by the flow in their instruction and
require reading them before work. Never assume a profile's name loads a skill;
missing skills or unavailable tools must be reported, not silently substituted.

Save a distinct operation ID BEFORE each intentional task, round and stage attempt:

```sh
afm agent spawn --profile <profile-id> --title <purpose/task/round> --instruction <bounded-task> --request-id <saved-operation-id>
```

Immediately persist task_id, participant_id and message_id from the receipt, plus
logical task, perspective, round and status. Spawn already created the canonical
request; do not send it again. On an uncertain response, reconcile durable records
and retry the SAME operation ID within that attempt. A new attempt after Revise
must not reuse an invalidated helper. Respect configured quotas/depth/task limits;
a denial is not permission to create a run agent, broaden access or skip review.

Batch independent helpers before one reply_to_all wait window of at most 300
seconds; follow [health checks](health.md) between windows even for long review
budgets. An expired window does not cancel the helper or consume its review budget.
Inspect each helper's status and recent granted logs/activity every window; recover
confirmed terminal failures without waiting for the full assignment deadline.
After resolution,
read `afm agent helper-result --task-id <task_id>` for every current-stage helper.
The inbox reply is only a summary: inspect committed status, verdict, findings and
limitations. A ready wait or reply alone does not prove completed analysis. Save
needed findings in the stage report and IDs/dispositions in its ledger; later
stages consume those artifacts, not another stage's helper-result API.

Require one final result under AFM's injected output contract, with findings admitted by
`afm-review-work` and their evidence. Apply its threshold when merging results
and deciding whether a repair or another review round is warranted. No acknowledgement/partial final replies. Helpers may not
be able to write scratch files: let them return evidence in the result and have
the coordinator save artifacts. A completed helper is one-shot. A later task or
correction gets a new helper linked to the same work item and earlier findings;
this never resets the defect's repair budget. Stop after requested evidence.

## Retire unfinished advisory work

Before completing a stage, inspect your OWN temporary helpers as well as your
waits. A helper in waiting_peer is unfinished even if the main deliverable was
accepted. Answer any outstanding callback addressed to you with a linked reply
to that question's message ID.

For an obsolete optional advisory task, send ONE closure instruction in its
existing conversation: stop analysis, retire only its own obsolete waits and
return an honest final result with existing findings/limitations, inconclusive
when incomplete. Include these closure expectations in the original brief. Do
not create another request/review or wait for acknowledgement merely for cleanup.
Required helpers and selected reviews still needed by the plan must produce
accepted evidence; advisory mode alone does not permit discarding them.

After a bounded wait on the existing task/request, check committed terminal
status. Sent, delivered, ACK or Board done does not establish termination. An
ordinary message may not wake a helper parked on a different wait; never forge
another participant's reply. Force-cancel only through an explicitly granted
owner-scoped helper cancellation capability if it actually exists. Do not assume
a CLI command exists or use the dashboard/operator API as a bypass. If graceful
closure fails and the capability is absent, report task/wait IDs and the required
operator action. Preserve findings/reason/cleanup outcome in the stage report.
