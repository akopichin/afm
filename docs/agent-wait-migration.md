# Migrate coordinator waits for spawned helpers

This guide is for external flows whose stage coordinator starts one-shot helpers
with `afm agent spawn`. It changes the coordinator's instructions and saved
state; it does not change a flow's `flow.yaml` or an existing run's journal.

## Update the coordinator

1. Save the `task_id` and **spawn request `message_id`** from each spawn receipt.
   The task ID is for `helper-result`; the message ID identifies the reply to
   wait for.
2. For one helper, call
   `afm agent wait --condition reply_to --message-id <spawn_message_id>` once.
   For several independent helpers whose results are
   all required, register one `reply_to_all` batch with their spawn message IDs.
   Save the wait or batch ID and end the turn when the receipt says `yield`.
3. If a helper asks a callback question, reply to **that callback message ID**.
   End the turn again. The original spawn wait remains pending and will wake the
   coordinator when the helper answers or can no longer answer.
4. On the terminal continuation, read `afm agent wait-status` for outcomes and
   `afm agent helper-result --task-id <task_id>` for every helper's committed
   status, verdict, findings and limitations. A cancelled or failed wait is not
   an approval. Continue to the stage's completion marker only after the
   required work and checks actually passed.

Remove the old pattern of repeatedly registering a threadless
`addressed_message` wait for each helper. That condition still means “the next
message addressed to me”; it does not identify which helper request finished,
and a passive terminal result does not satisfy it. Keep `addressed_message`
where the flow intentionally waits for an unrelated incoming message, such as
a typed Board task notification.

## Recovery and compatibility

An equivalent repeat of a **single pending** wait now returns the original
`wait_id` with `already_pending: true`; it still returns `status: yield`, so the
coordinator should end its turn. It does not extend the original deadline.
This deduplication does not combine batch waits or repair duplicate wait IDs
already stored in an older run.

A fresh completion marker while waits remain pending produces a durable
`completion_marker_pending_wait` notice; the stage stays **Awaiting colleague**.
The run owner can [list and cancel selected waits](collaboration.md#duplicate-waits-and-owner-recovery)
with the separately delivered owner token. Cancellation resumes normal stage
processing; it cannot substitute for a passing helper result or verification.

Use a build that includes the owner wait recovery API and `already_pending`
receipt before adopting these instructions. Older AFM versions may reject a
helper's callback wait as a cycle and lack owner recovery. For an existing
parked run, inspect its actual waits and helper results before cancelling
anything; the new behavior does not reinterpret its historical events.
