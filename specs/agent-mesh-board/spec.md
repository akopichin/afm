# Agent mesh: Board, typed tasks and Discussion

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- Board facts (`afm agent board get/publish`) and `board_key` waits.
- Board protocol of a run (typed vs legacy) and typed tasks (`afm agent task …`, `/api/*/task(s)`).
- `assistants`-mode Board switch-off.
- Stage Discussion posts, mentions, interrupt and their mesh mirror.

Siblings: [`../agent-mesh/spec.md`](../agent-mesh/spec.md) (policy, roster),
[`../agent-mesh-messaging/spec.md`](../agent-mesh-messaging/spec.md), [`../agent-mesh-waits/spec.md`](../agent-mesh-waits/spec.md).
Helper attach from the feed — [`../helpers-lifecycle/spec.md`](../helpers-lifecycle/spec.md). IDs: `MESH` R60–R71.

## Terms
- **Board fact** — versioned `key → status` entry authored by one participant.
- **Typed run** — run whose journal starts with `mesh_board_protocol_set` `typed_v1`; otherwise **legacy**.
- **Assignment** — a participant's active slot on a typed task: `assigned` → `in_progress` → `done`.
- **Manager** — the user and the task's coordinator (explicit, else its creator).

## Requirements

### MESH-R60 Board off in `assistants` mode
In `assistants` mode every Board operation (read, publish, typed tasks, `board_key` waits, incl. retries of committed requests) is refused with `feature_disabled` "Board is unavailable in assistants mode" before reading or writing anything; HTTP routes answer 404 after authentication/origin checks, the CLI exits 1. Other waits and messaging still work; agent prompts omit Board instructions and notices.

Evidence: `engine:internal/orchestrator/agentcomm_board_access_test.go` `TestBoardOperationsRefusedInAssistantsMode`, `TestBoardRetryOfCommittedRequestRefusedInAssistantsMode`, `TestNonBoardWaitsStillRegisterInAssistantsMode`; `afm:cmd/afm/run_board_assistants_test.go` `TestBoardRoutesRefusedInAssistantsMode`, `TestBoardRoutesKeepAuthenticationInAssistantsMode`, `TestAgentBoardCLIRefusedInAssistantsMode`, `TestAgentMailboxHidesBoardNoticeInAssistantsMode`

### MESH-R61 Board facts
Publishing creates version 1 or updates with `--expected-version`; an older version is `stale_revision`, and only the key's author may update it. Publishing wakes pending `board_key` waits; retries under the same request id stay idempotent.

Evidence: `engine:internal/orchestrator/agentcomm_board_test.go` `TestMesh_PublishFirstVersionThenStaleRevisionRejected`, `TestMesh_PublishRejectsUnauthorizedNonOwner`, `TestMesh_PublishWakesPendingBoardWait`, `TestMesh_PublishRetryAfterResolvingWaitStaysIdempotent`

### MESH-R62 Board protocol per run
A fresh run records `typed_v1` before any agent starts; a resumed run without it stays legacy forever. Typed task operations are refused in a legacy run; in a typed run `task/…` keys are typed-only (`board publish` refused, `board get` omits them), in a legacy run they are ordinary facts.

Evidence: `engine:internal/orchestrator/agentcomm_board_tasks_test.go` `TestBoardTaskDisabledForLegacyRun`; `engine:internal/orchestrator/agentcomm_board_tasks_messages_test.go` `TestPublishRejectsTaskNamespaceInTypedRun`, `TestPublishAllowsTaskNamespaceInLegacyRun`; `afm:docs/collaboration-journal.md` (Board task records)

### MESH-R63 Typed task state and rights
Task status derives from active assignments (Todo / In progress / Done); one active assignment per participant per task. Rights: create — anyone; assign/release/reopen/edit — a manager; claim — any agent when no active assignment (assign+start in one change); start/progress/done — the assignment's own participant. Every change except create needs `--expected-version`; stale → `stale_revision`. The actor comes only from the token, never the body.

Evidence: `engine:internal/orchestrator/agentcomm_board_tasks_test.go` `TestBoardTaskServiceLifecycle`, `TestBoardTaskActorFromRequestOnly`, `TestBoardTaskConcurrentClaimsOneWinner`, `TestBoardTaskEditClearsFields`; `afm:contracts/agentcomm/board_task_test.go`; `engine:internal/state/mesh_board_tasks_test.go`

#### MESH-S60 Concurrent claims
- GIVEN an unassigned task at version 1
- WHEN two agents claim it with `--expected-version 1` at once
- THEN exactly one claim wins; the other gets `stale_revision`

### MESH-R64 Typed task validation
Keys are `task/` + letters/digits/`.`/`_`/`-`, ≤200 bytes (checked on create only); artifact refs must point into `artifacts/`; texts are redacted, a key containing a secret is refused. Format rules apply at admission, never on replay. Assistant, passive helpers and stages awaiting helpers cannot be assignees; the user is never an assignee.

Evidence: `engine:internal/orchestrator/agentcomm_board_tasks_messages_test.go` `TestBoardTaskRedactsSecrets`, `TestBoardTaskRejectsKeyWithSecret`, `TestBoardTaskRejectsSideAgentAssignee`; `engine:internal/orchestrator/agentcomm_board_tasks_test.go` `TestBoardTaskDoneClearsArtifactRefs`; `afm:contracts/agentcomm/board_task.go`

### MESH-R65 Assignment messages and coordinator notices
Assigning to someone else sends the assignee a message in the same commit; if it cannot be delivered the assignment is refused (rechecked at commit). Claims and self-assignments send nothing. Done/release/reopen by a non-coordinator notifies the coordinator in a new conversation of its own (opened anew when the old one is full); an undeliverable notice is skipped (`notice_skipped`) and the change still commits; a user coordinator gets none. Replay never duplicates notices.

Evidence: `engine:internal/orchestrator/agentcomm_board_tasks_messages_test.go` `TestBoardTaskAssignSendsMessageAtomically`, `TestBoardTaskAssignRejectsUndeliverable`, `TestBoardTaskAssignRecheckedAtCommit`, `TestBoardTaskSelfAssignAndClaimNoMessage`, `TestBoardTaskDoneNotifiesCoordinator`, `TestBoardTaskNoticeSkippedStillCommits`, `TestBoardTaskNoNoticeWhenCoordinatorIsUser`, `TestBoardTaskReplayNoDuplicateNotice`, `TestBoardTaskCoordinatorNoticeOpensOwnThreadWhenConversationFull`

#### MESH-S61 Coordinator waits for done
- GIVEN a coordinator created and assigned a task, then waited `addressed_message` without thread
- WHEN the assignee runs `task done`
- THEN the coordinator notice wakes the coordinator

### MESH-R66 Retries and receipts
Every task change needs a `request_id` (the CLI generates one); repeating it returns the original receipt even if the task changed or the alias moved; the same id with a different change is refused. Journal write or receipt-read failure ends the run.

Evidence: `engine:internal/orchestrator/agentcomm_board_tasks_test.go` `TestBoardTaskRetryReturnsOriginalReceipt`, `TestBoardTaskRequiresRequestID`, `TestBoardTaskRetryAfterAliasMovedToAnotherParticipant`, `TestBoardTaskConcurrentDuplicateGetsTheReceipt`, `TestBoardTaskJournalWriteFailureEndsRun`; `afm:cmd/afm/agent_task_test.go` `TestAgentTaskCLI_MutationsMapFlags`, `TestAgentCLI_SendAndPublishTakeRequestID`

### MESH-R67 Tasks never complete by text
Only `task done` completes an assignment; replies, acks and turn results never do. Release/reopen do not stop or message the executor. Board tasks never hold a stage or the run open.

Evidence: `engine:internal/orchestrator/AGENTS.md` (Board state never from free text); `engine:internal/orchestrator/agentcomm_left_unanswered_run_test.go` `TestLeftUnanswered_BoardTaskIncident`; `afm:docs/collaboration.md` (Typed tasks)

### MESH-R68 Board tasks in prompts
In a typed run each agent prompt carries `<your_board_tasks>` (≤20 active assignments, oldest first, with assignment id, version, progress, marked as data) and typed instructions; a legacy run has no block and legacy instructions.

Evidence: `engine:internal/orchestrator/agentcomm_board_tasks_prompt_test.go` `TestBoardTaskPrompt_ParticipantSeesItsAssignmentID`, `TestBoardTaskPrompt_ListsAtMostTwentyTasks`, `TestBoardTaskPrompt_LegacyRunHasNoTaskBlockAndLegacyInstructions`

### MESH-R69 Legacy human board posts
In a legacy run a human post notifies up to 16 working/waiting agents with one message each (recorded with the task); finished or full recipients are skipped and the post still succeeds. Status comes from `in progress:` / `done:` report keys `<task>/<alias>`.

Evidence: `engine:internal/orchestrator/agentcomm_human_member_test.go` `TestPublish_NoticeConversationIncludesTheHuman`; `afm:contracts/agentcomm/limits.go` `MaxBoardNotify`; `afm:docs/collaboration.md` (Legacy runs)

### MESH-R70 Discussion posts and mirror
A Discussion post never changes stage FSM. It is committed together with its mirror in the stage's mesh thread (same message id) in one operation; the round limit is enforced before any write; an unmirrorable author still writes the discussion message. Requires collaboration enabled.

Evidence: `engine:internal/orchestrator/agentcomm_discussion_test.go` `TestDiscussion_MirroredIntoMeshThreadWithSameID`, `TestDiscussion_RoundLimitEnforcedBeforeMeshWrite`, `TestDiscussion_SecondPostReusesTheSameThread`, `TestDiscussion_UnmirrorableAuthorStillWritesDiscussionMessage`; `engine:internal/orchestrator/discussion.go` `PostDiscussionMessage`

### MESH-R71 Mentions, interrupt, finished stages
Mentions deliver to the mentioned participant and mirror its reply; unknown or unreachable mentions are rejected. An interrupt intent is durable: it restarts a live agent with the question, goes into the first prompt when none is live, survives a crash, and loses to a concurrent Pause. A human post wakes a stage waiting for a colleague; posts to a finished stage are recorded and marked unanswered/undeliverable.

Evidence: `engine:internal/orchestrator/discussion_chat_test.go` `TestDiscussion_MentionDeliversAndMirrorsMentionedReply`, `TestDiscussion_UnknownMentionRejected`, `TestDiscussion_InterruptRestartsLiveAgentWithQuestion`, `TestDiscussion_CrashBeforeInterruptStillAsksAfterRestart`, `TestDiscussion_InterruptLosesToConcurrentPause`, `TestDiscussion_HumanPostWakesStageWaitingForColleague`, `TestDiscussion_FinishedStageMarksUnansweredHumanPosts`; `engine:internal/orchestrator/discussion_mention_reach_test.go` `TestDiscussion_UnreachableMentionIsRejected`

#### MESH-S62 Interrupt with live agent
- GIVEN a stage agent mid-turn
- WHEN the human posts with interrupt
- THEN the agent is restarted with the question; after a crash before the interrupt it is still asked on restart

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| MESH-E60 | Board op in `assistants` mode | `feature_disabled`; HTTP 404; CLI exit 1 | `TestBoardOperationsRefusedInAssistantsMode` |
| MESH-E61 | Stale `expected_version` | `stale_revision` | `TestMesh_PublishFirstVersionThenStaleRevisionRejected` |
| MESH-E62 | Non-author publish / non-manager change | `actor_not_allowed` | `TestMesh_PublishRejectsUnauthorizedNonOwner` |
| MESH-E63 | Typed op in legacy run | refused | `TestBoardTaskDisabledForLegacyRun` |
| MESH-E64 | Assignment undeliverable | assignment refused | `TestBoardTaskAssignRejectsUndeliverable` |
| MESH-E65 | Discussion over round limit | rejected, nothing written | `TestDiscussion_RoundLimitEnforcedBeforeMeshWrite` |
| MESH-E66 | Discussion with collaboration off | `feature_disabled` | `engine:internal/orchestrator/discussion.go` |

## Coverage gaps and discrepancies
- **MESH-G60** (untested): `create --assign-to` combined with an agent's foreign `--coordinator` → `actor_not_allowed`, nothing created (docs) — no named test located.
- **MESH-G61** (doc-only): legacy status derivation from `in progress:` / `done:` report markers is a dashboard convention; no engine test asserts it.
- **MESH-G62** (doc-only): same-origin check is not an authentication boundary; task rights are a convention for cooperating agents.
