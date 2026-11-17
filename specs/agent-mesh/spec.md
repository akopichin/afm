# Agent mesh: policy, roster and identity

> Baseline: afm `462bd5d4` · afm-engine `v1.2.1` (`11bddf2c`). Status: draft baseline.

## Scope
- Collaboration policy switches (enabled, mode, side agent) as they gate mesh features.
- Roster: participants, `team:` run agents, aliases, the human member, the Assistant.
- Invocation tokens and the agent jobs journal (`agent-jobs.jsonl`).

Siblings: messaging and delivery — [`../agent-mesh-messaging/spec.md`](../agent-mesh-messaging/spec.md);
waits — [`../agent-mesh-waits/spec.md`](../agent-mesh-waits/spec.md), [`../agent-mesh-wait-endings/spec.md`](../agent-mesh-wait-endings/spec.md) (incl. owner recovery), [`../agent-mesh-yield/spec.md`](../agent-mesh-yield/spec.md);
Board, typed tasks, Discussion — [`../agent-mesh-board/spec.md`](../agent-mesh-board/spec.md);
loops — [`../agent-mesh-loops/spec.md`](../agent-mesh-loops/spec.md).
Out of scope: helper lifecycle, triggers, required gates, `afm agent spawn` — [`../helpers-lifecycle/spec.md`](../helpers-lifecycle/spec.md).
There is no `afm agent ask`; `ask` is the stage dialog command (`afm stage ask`).

## Terms
- **Participant** — durable mesh identity (stage agent, helper, run agent, Assistant, human `user`).
- **Run agent** — standing participant owned by the run (from `team:` or **Add run agent**).

## Requirements

### MESH-R01 Collaboration switch
Collaboration is ON unless `AFM_COLLABORATION` (non-empty) or config says otherwise (env > project > global > on). When off, attach, Discussion, loops, team creation and spawn are refused with `feature_disabled` / "collaboration is disabled"; plain agent messaging still works.

Evidence: `afm:contracts/config/collaboration_test.go` `TestCollaboration_EnvZeroOverridesConfigTrue`, `TestCollaboration_EmptyEnvDoesNotOverride`; `engine:internal/orchestrator/agentcomm_loop_test.go` `TestLoop_RejectedWhenAssistantsDisabled`; `engine:internal/orchestrator/team_test.go` `TestInitTeam_DisabledCollaborationAddsNothing`; `engine:internal/orchestrator/protocol_constants.go` `collaborationDisabledReason`

#### MESH-S01 Disabled collaboration
- GIVEN `AFM_COLLABORATION=0`
- WHEN an agent creates a loop
- THEN the call fails with `feature_disabled` and nothing is journaled

### MESH-R02 Collaboration mode
`collaboration.mode` is `team` (default) or `assistants`, precedence flow > project > global; an invalid value in any layer stops config loading. The run resolves it once; `assistants` turns the Board off (MESH-R60) but keeps messages, waits, run agents and helpers.

Evidence: `afm:contracts/config/collaboration_test.go` `TestCollaborationMode_InvalidLowerLayerRejectedEvenWhenOverridden`; `afm:cmd/afm/run_collaboration_test.go` `TestResolveCollaborationMode`; `afm:cmd/afm/run_board_assistants_test.go` `TestAgentSendWorksInAssistantsMode`; `afm:docs/collaboration.md`

### MESH-R03 Side agent (Assistant) toggle
The Assistant starts only if the host policy allows it and `flow.side_agent` > `AFM_SIDEAGENT` > config > default ON resolves true; it is independent of collaboration. A journal failure while starting it disables it with a warning, not the run.

Evidence: `engine:internal/engine/assistant_test.go` `TestSideAgentEnabled_Precedence`; `afm:contracts/config/config_test.go` `TestSideAgent_EnvOverridesConfig`; `afm:cmd/afm/run_collaboration_test.go` `TestResolveCollaboration_IndependentFromSideAgent`; `engine:internal/engine/open.go`

### MESH-R04 Assistant participant and `@assistant`
A freshly registered Assistant reserves `@assistant`; colliding stage/team/run-agent handles move to the first free `@assistant-N`. It keeps its stored alias on resume even when disabled, then shows unreachable and sends to it fail. Legacy `@side` is kept.

Evidence: `engine:internal/orchestrator/agentcomm_sideagent_test.go` `TestSideAgent_FreshRegistrationAnswersToAssistant`, `TestSideAgent_StageNamedAssistantMakesRoom`, `TestSideAgent_ReservationHoldsWhileUnavailable`, `TestSideAgent_LegacySideAliasReplaysWithoutConflict`

#### MESH-S02 Stage named assistant
- GIVEN a stage whose ID is `assistant` and a fresh Assistant registration
- WHEN the roster is built
- THEN the Assistant is `@assistant` and the stage gets `@assistant-2`

### MESH-R05 Roster
The roster lists the human once, stages (finished ones remain, unreachable), helpers, run agents and the Assistant, with executor and read-only enforcement. Aliases are unique in the run; a second agent with title "QA" is `@qa-2`.

Evidence: `engine:internal/orchestrator/agentcomm_test.go` `TestMesh_ListParticipantsIncludesUserStageAndHelper`, `TestMesh_TerminalStageStaysInRosterUnreachable`; `engine:internal/orchestrator/agentcomm_human_member_test.go` `TestRoster_ListsTheHumanOnce`; `engine:internal/orchestrator/agentcomm_roster_test.go` `TestRoster_ParticipantCommandAndReadonlyEnforcement`; `engine:internal/orchestrator/agentcomm_runagent_test.go` `TestAddRunAgent_SeveralAgentsOfOneProfile`

### MESH-R06 Declared team
With collaboration on, `team:` members become idle run agents in declaration order before any stage starts. On resume, saved members (keyed by `id`) are reused, only new ids are added; changed definitions keep the saved version with a `team_warning`. An unavailable profile is skipped with a warning; a journal read/write failure stops startup.

Evidence: `engine:internal/orchestrator/team_test.go` `TestInitTeam_CreatesIdleRunAgentsInDeclarationOrder`, `TestInitTeam_ResumeKeepsSavedAgents`, `TestInitTeam_UnknownProfileIsSkipped`, `TestInitTeam_StorageFailureOnAddStopsStartup`; `engine:internal/orchestrator/team_run_test.go` `TestTeam_SurvivesInterruptAndResume`; `afm:cmd/afm/run_collaboration_test.go` `TestCheckFlowTeam_UnknownProfileErrors`, `TestCheckFlowTeam_SavedMemberSkipsProfileCheck`

#### MESH-S03 Resume after Ctrl+C
- GIVEN a run that added team members `arch` and `qa`
- WHEN the run is resumed
- THEN no member is added twice

### MESH-R07 Run agents are idle standing roles
Every message addressed to a run agent starts its next turn; an idle run agent never keeps the run open, one with an unread (waking) message does. Adding a run agent is idempotent per request id and refused once the run is finalizing.

Evidence: `engine:internal/orchestrator/agentcomm_delivery_test.go` `TestShouldExit_QueuedRunAgentWithUnreadMessageHoldsRun`, `TestFinalize_AtomicWithMessageAdmission`; `engine:internal/orchestrator/agentcomm_runagent_test.go` `TestAddRunAgent_IdempotentPerRequest`, `TestAddRunAgent_RejectedWhenRunFinalizing`

### MESH-R08 Token identity
Each agent invocation gets a mesh token minted at start and revoked when it ends; the server derives the caller from the token, never from the request body. The CLI emits exactly one JSON object (stdout on success, `{error,message}` on stderr with exit 1).

Evidence: `engine:internal/orchestrator/agentcomm_tokens_test.go` `TestMesh_TokenMintResolveRevoke`, `TestMesh_EndedInvocationsLeaveNoTokens`; `afm:cmd/afm/agent_test.go` `TestAgentCLI_Send_ReachesServiceWithTokenIdentity`, `TestAgentCLI_NoDescriptor_ClearError`; `engine:host/agent.go` `runAgentCmd`

### MESH-R19 Agent jobs journal
Every AI subprocess is a job in `agent-jobs.jsonl`, keyed by participant (verify steps as `verify:<stage>#<step>`, format repair under its owner). The file starts with a header written once; one writer; torn tails are cut on open; identical repeats are ignored and conflicting ones diagnosed; records carry no argv, env or message text. An unusable journal never fails the agent turn; an injected runner writes no jobs; a turn that yielded is `yielded`.

Evidence: `afm:contracts/agentjobs/store_test.go` `TestOpenWritesHeaderOnce`, `TestOpenRepairsTornTailBeforeAppend`, `TestConflictingRepeatIsDiagnosedNotApplied`, `TestWriteFailureLatchesUnavailableWithoutLosingSnapshot`; `afm:contracts/agentjobs/safety_test.go` `TestRecordFieldsCarryNoArgvEnvOrMessageText`; `engine:internal/orchestrator/agent_jobs_test.go` `TestAgentJobs_VerifyStepsAreSeparateAgents`, `TestAgentJobs_FormatRepairBelongsToItsOwner`, `TestAgentJobs_InjectedRunnerRecordsNoJobs`, `TestAgentJobs_HelperTurnThatYieldsIsYielded`; `afm:contracts/agentjobs/AGENTS.md`

## Errors and rejections
| ID | Condition | Observable result | Evidence |
|----|-----------|-------------------|----------|
| MESH-E01 | Collaboration off: attach/loop/discussion/team | `feature_disabled`, "collaboration is disabled" | `engine:internal/orchestrator/protocol_constants.go` |
| MESH-E06 | Client claims a reserved operation id | rejected | `engine:internal/orchestrator/agentcomm_reserved_test.go` |

## Coverage gaps and discrepancies
- **MESH-G01** (ambiguous): the task brief names `afm agent ask`; no such subcommand exists (`engine:host/agent.go` `NewAgentCmd`); `ask` is `afm stage ask` (dialog domain).
- **MESH-G04** (untested): flow-level `side_agent` override has a precedence unit test but no run-level test that the Assistant is absent from the roster when off (closest: `TestSideAgent_DisabledCapabilityLeavesNoParticipant`).
