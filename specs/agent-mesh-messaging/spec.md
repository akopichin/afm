# Agent mesh: messaging and delivery

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- `afm agent send/reply/inbox/ack`: thread selection, human membership, feed-origin sends.
- Delivery to turns, prompt budgets, run-agent turn results, passive messages, unread handling.
- Message admission limits and secret redaction.

Siblings: policy, roster, tokens, agent jobs — [`../agent-mesh/spec.md`](../agent-mesh/spec.md);
waits — [`../agent-mesh-waits/spec.md`](../agent-mesh-waits/spec.md), [`../agent-mesh-wait-endings/spec.md`](../agent-mesh-wait-endings/spec.md), [`../agent-mesh-yield/spec.md`](../agent-mesh-yield/spec.md); Board/Discussion —
[`../agent-mesh-board/spec.md`](../agent-mesh-board/spec.md); loops — [`../agent-mesh-loops/spec.md`](../agent-mesh-loops/spec.md).
Out of scope: helper lifecycle — [`../helpers-lifecycle/spec.md`](../helpers-lifecycle/spec.md).
IDs continue the `MESH` family (R09–R18).

## Terms
- **Handed / delivered** — a message given to an agent in a turn prompt or `inbox` read.
- **Turn result** — `auto_result` reply afm posts for a run agent's turn; passive (never wakes).
- **Waking message** — a non-passive message addressed to an agent that starts or extends its turn.

## Requirements

### MESH-R09 Send and thread selection
`send` without a thread continues the sender's open conversation with exactly those recipients (newest untitled DM for one) or opens one in the same operation; `reply --in-reply-to` uses the replied message's thread. Concurrent first sends share one thread. A request id replays the original; the same id with different content is a typed conflict.

Evidence: `engine:internal/orchestrator/agentcomm_agent_send_test.go` `TestSendFromAgent_ThreadlessDirectUsesNewestUntitledDM`, `TestSendFromAgent_ConcurrentStartsShareOneThread`, `TestSendFromAgent_ReplyWithoutThreadUsesTheRepliedMessagesThread`, `TestSendFromAgent_RetryAfterThreadClosedReplaysOriginal`; `engine:internal/orchestrator/agentcomm_admission_test.go` `TestSendFromAgent_ReusedRequestIDWithOtherMessageConflicts`

### MESH-R10 Human member of every new conversation
Agent-opened conversations, board-task messages and delegate result threads include the human, who is not addressed and whose inbox does not fill. Only agents count toward 8 members. Legacy threads without the human are continued, not split; a Discussion thread is never reused for an agent send.

Evidence: `engine:internal/orchestrator/agentcomm_human_member_test.go` `TestSendFromAgent_NewConversationIncludesTheHuman`, `TestSendFromAgent_ContinuesALegacyConversationWithoutTheHuman`, `TestSendFromAgent_DoesNotReuseADiscussionThread`; `engine:internal/state/mesh_projection.go` (MaxThreadMembers count)

### MESH-R11 Human sends from a stage feed
A human send without a thread reuses only a conversation the origin feed shows, else opens one owned by that feed; an unknown origin stage is rejected; the same request id from another feed is a conflict.

Evidence: `engine:internal/orchestrator/agentcomm_agent_send_test.go` `TestSendFromAgent_UserSendReusesOnlyAConversationTheFeedShows`, `TestSendFromAgent_ConcurrentSendsFromTwoFeedsEachShowInTheirFeed`, `TestSendFromAgent_SameRequestFromAnotherFeedIsAConflict`; `afm:cmd/afm/run_mesh_send_test.go` `TestMeshActions_SendToSetByAlias`

### MESH-R12 Inbox and delivery
`inbox` returns only messages not yet handed unless `--all`; reading marks them delivered (not for the Assistant reader), and reading does not acknowledge. A turn is numbered and its handed messages journaled before the process starts; a turn cut short before its outcome is retried with the same messages, a failed start leaves them unseen. A delivery journal failure ends the run.

Evidence: `engine:internal/orchestrator/agentcomm_delivery_test.go` `TestReadInbox_OnlyNewSkipsMessagesHandedInEarlierTurns`, `TestInboxDelivery_FailedStartKeepsMessagesUnseen`, `TestInboxDelivery_MessageArrivingAfterPromptIsNotMarked`; `engine:internal/orchestrator/agentcomm_inbox_test.go` `TestMesh_DeliveryJournalFailureEndsRun`; `engine:internal/orchestrator/agentcomm_messages_test.go` `TestMesh_AcknowledgeTransitionsReceiptAndReadingDoesNotAck`; `afm:cmd/afm/agent_test.go` `TestAgentCLI_Inbox_NewOnlyUnlessAll`; `engine:host/run_agent_mesh.go` `Inbox`

#### MESH-S04 Crash mid-delivery
- GIVEN a turn whose messages were journaled and the process crashed before its outcome
- WHEN the run resumes
- THEN the turn is retried with the same messages, without duplicating receipts

### MESH-R13 Turn prompt budget
A turn prompt carries at most 64 KiB of messages: first an interrupted turn's messages, then waking messages, then turn results; the rest waits. A requests block lists up to 20 earlier requests (~24 KiB), pending ones first, with answers, open waits or end reasons.

Evidence: `engine:internal/orchestrator/agentcomm_inbox_test.go` `TestMesh_InboxBlockStaysWithinByteBudget`, `TestMesh_InboxBudgetPutsUnfinishedThenWakingBeforePassive`; `engine:internal/orchestrator/agentcomm_continuation_test.go` `TestRequestsBlock_KeepsOpenRequestsFirst`, `TestRequestsBlock_StaysWithinItsBudget`, `TestRequestsBlock_ShowsAWaitThatEnded`

### MESH-R14 Run agent turn result
At turn end afm replies with the summary to the newest waking message, unless the agent already answered it; with no waking message and no open request it posts nothing. The reply also answers requests earlier turns yielded on. An undeliverable reply is recorded as a feed notice and the agent still goes idle; the requester's `reply_to` wait is cancelled unless another recipient can answer. An invalid run-agent result makes the agent idle with a redacted ≤2 KiB reason reply.

Evidence: `engine:internal/orchestrator/agentcomm_turn_reply_test.go` `TestTurnReply_AnswersTheWakingMessage`, `TestTurnReply_SkippedOnlyWhenTheAgentAnsweredTheWakingMessage`, `TestTurnReply_NoWakingInputNoReply`, `TestTurnReply_UndeliverableReplyFinishesTheTurnWithoutIt`; `engine:internal/orchestrator/agentcomm_turn_failure_test.go` `TestRunAgentInvalidResult_GoesIdleAndAnswersWhy`, `TestTurnReply_AnswersTheRequestAcrossAYield`, `TestTurnReply_UndeliveredAnswerEndsTheRequestersWait`

### MESH-R15 Passive messages never wake
A turn result waits in the inbox: it does not start an idle run agent, extend a stage turn, satisfy `addressed_message`, or hold the run; only a `reply_to` wait on that message is resumed by it. Passive messages have their own 200 limit.

Evidence: `engine:internal/orchestrator/agentcomm_turn_reply_test.go` `TestPassive_DoesNotWakeAnIdleRunAgent`, `TestPassive_DoesNotExtendAStageTurn`, `TestPassive_WaitingAgentIsResumedOnlyByWhatItWaitsFor`, `TestPassive_FinishedStageIsReleasedBeforeTheRunEnds`; `engine:internal/state/mesh_projection.go` `CheckInboxCapacity`

### MESH-R16 Unread messages hold a stage
A stage agent that ends a turn with unread waking messages gets an extra turn with them and a note, at most 5 times (also after restart). A stage finishing with unread messages marks them undeliverable and cancels and resumes senders' `reply_to`/`addressed_message` waits on them.

Evidence: `engine:internal/orchestrator/agentcomm_unread_test.go` `TestUnreadMessage_ExtraTurnAnswersThenCompletes`, `TestUnreadMessage_ExtraTurnsAreCapped`, `TestUnreadMessage_WaiterIsReleasedWhenStageFinishesUnread`, `TestUnreadMessage_RecoveryCompletionGivesExtraTurn`; `engine:internal/orchestrator/agentcomm_unread.go` `maxUnreadTurns`

#### MESH-S05 Cap reached
- GIVEN a stage that ignored new messages for 5 extra turns
- WHEN it ends its turn again
- THEN it completes, the messages become undeliverable and their waiters resume

### MESH-R17 Message admission and limits
Sends are all-or-nothing: an unknown, unreachable (finished), script-stage or budget-held recipient, a full inbox (200 unread), >8 recipients/members, a body over 16 KiB or a full/closed thread rejects the send and opens nothing. Limits are explicit refusals, never truncation.

Evidence: `engine:internal/orchestrator/agentcomm_limits_test.go` `TestMesh_InboxUnreadLimitProducesExplicitRefusal`, `TestMesh_FanOutRecipientLimitProducesExplicitRefusal`, `TestMesh_ThreadMemberFanOutLimitProducesExplicitRefusal`; `engine:internal/orchestrator/agentcomm_admission_test.go` `TestSendFromAgent_UnreachableRecipientOpensNothing`, `TestSendFromAgent_FullInboxOpensNothing`; `engine:internal/orchestrator/agentcomm_turn_reply_test.go` `TestSend_ToScriptStageIsRejected`; `engine:internal/orchestrator/agentcomm_budget_test.go` `TestSend_ToBudgetHeldHelperIsRefusedWithBudgetCode`; `afm:contracts/agentcomm/limits.go`

### MESH-R18 Secret redaction
Message bodies, thread titles, board texts, loop prompts and notices are redacted before commit; values shorter than 4 chars are kept.

Evidence: `engine:internal/orchestrator/agentcomm_messages_test.go` `TestMesh_SendRedactsSecretsInBody`, `TestMesh_OpenThreadRedactsSecretBeforeCommit`; `engine:internal/orchestrator/agentcomm_limits_test.go` `TestMesh_SecretsAbsentFromLiveNoticeAndNoticesJournal`, `TestMesh_RedactsSecretsOutsideTheEnvironment`

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| MESH-E02 | Unknown recipient / thread | `not_found`, nothing opened | `engine:internal/orchestrator/agentcomm_agent_send_test.go` `TestSendFromAgent_UnknownRecipientIsNotFound` |
| MESH-E03 | Finished recipient | `recipient_unreachable`-class refusal, nothing written | `engine:internal/orchestrator/agentcomm_admission_test.go` `TestAttachAndSend_FirstMessageToFinishedRecipientRejected` |
| MESH-E04 | Inbox/thread/fan-out limit | `invalid_request` naming the limit | `engine:internal/orchestrator/agentcomm_limits_test.go` |
| MESH-E05 | Reused request id, other content | typed conflict | `engine:internal/orchestrator/agentcomm_admission_test.go` `TestSend_ReusedRequestIDIsTypedConflict` |
| MESH-E07 | Recipient held by budget | budget code | `engine:internal/orchestrator/agentcomm_budget_test.go` |

## Coverage gaps and discrepancies
- **MESH-G02** (untested): 16 KiB body limit and "values shorter than 4 chars kept" are not asserted by a named test found here; check `afm:contracts/agentcomm/validate_test.go` and redact tests.
- **MESH-G03** (doc-only): "human's inbox lists the newest 1000" (`MaxHumanInbox`) — no direct test located.
