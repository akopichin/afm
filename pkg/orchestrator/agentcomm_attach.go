// Package orchestrator (this file, Agent Mesh Phase 6, T6.2): AttachAndSend — the ONE atomic composite for
// "attach a new mesh participant (a stage-scoped helper or a run-scoped standing role) and, in the SAME
// durable operation, open its first thread and optionally send its first message". dev-plan.md §6.7.1/§6.7.3
// name three future callers that all need this exact guarantee — the Feed attach preview, "Add run agent"
// (T6.2b, agentcomm_runagent.go) and a confirmed Side-agent action draft (T6.7) — so there is exactly one
// implementation of the composition, never three copies of it.
package orchestrator

import (
	"context"
	"errors"
	"fmt"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/redact"
	"github.com/akopichin/afm/pkg/state"
)

// AttachAndSendRequest describes one atomic attach. Scope is assistants.Scope (NOT agentcomm.Scope — the two
// packages deliberately each keep their own Scope type, see T6.1/dev-plan.md's Vocabulary table; this request
// lives in pkg/orchestrator and talks assistants.Scope because it is, first and foremost, a task-admission
// request). StageID is required and validated for ScopeStage, and must be empty for ScopeRun — there is no
// stage to validate for a standing role.
//
// There is deliberately no command/model/executor field: exactly like AttachRequest, the executor is resolved
// server-side from ProfileID alone (TestAttach_RejectsClientSuppliedCommand's guarantee, pkg/server/
// assistants_test.go) — a client cannot supply one because the struct has nowhere for it to go.
//
// Recipients extends the opened thread's member set beyond the default two (the human "user" and the newly
// attached participant) — e.g. to fold the new role into a conversation that already includes another
// colleague. Every id in Recipients must already be a resolvable participant (a known stage/helper/user id,
// or an already-attached run/side participant); an unknown one fails the WHOLE call, same as Send's own
// "a mixed recipient set where one is bad writes nothing" rule (T2.2). When FirstMessage is set, it is
// addressed to every thread member except the sender — for the common case (no extra Recipients) that is
// exactly "recipient = the new participant" alone.
type AttachAndSendRequest struct {
	OperationID  string
	ProfileID    assistants.ProfileID
	Scope        assistants.Scope // zero value = assistants.ScopeStage, same convention as assistants.Task.Scope
	StageID      string           // required for ScopeStage; must be empty for ScopeRun
	Mode         assistants.Mode
	Title        string
	Instruction  string
	FirstMessage *string // nil = no first message; the task is still created and addressable
	Recipients   []agentcomm.ParticipantID
	Actor        state.Actor
}

// AttachAndSendResult names everything AttachAndSend's one operation created. MessageID is nil when the
// request carried no FirstMessage.
type AttachAndSendResult struct {
	TaskID        assistants.TaskID
	ParticipantID agentcomm.ParticipantID
	ThreadID      agentcomm.ThreadID
	MessageID     *agentcomm.MessageID
}

// meshRunParticipantID returns the stable, PROFILE-derived participant id for a scope=run standing role —
// "run:<profileID>" per dev-plan.md's Vocabulary table. Keying it on the profile id (not the operation id,
// unlike the task/thread/message ids below) is what lets AddRunAgent's "idempotent per profile" guarantee
// (T6.2b) fall out of plain operation-id idempotency: see addRunAgentOperationID in agentcomm_runagent.go.
func meshRunParticipantID(profileID assistants.ProfileID) agentcomm.ParticipantID {
	return agentcomm.ParticipantID("run:" + string(profileID))
}

// meshThreadScopeFor maps an assistants.Scope onto the agentcomm.Scope the opened thread should carry. The
// two types' string values happen to coincide ("stage"/"run"), but this is an explicit switch rather than a
// raw cast — the two Scope types are deliberately NOT unified (dev-plan.md's Vocabulary table), and a reader
// should not have to rely on that coincidence to trust the mapping.
func meshThreadScopeFor(s assistants.Scope) agentcomm.Scope {
	if s == assistants.ScopeRun {
		return agentcomm.ScopeRun
	}
	return agentcomm.ScopeStage
}

// meshActorSenderID resolves the ParticipantID that sends AttachAndSend's optional first message. Today that
// is only ever the human (Actor.Kind == "user") — the Side agent's own mesh identity lands in T6.7, out of
// scope here — so any other actor kind is an explicit, honest error rather than a guess.
func meshActorSenderID(actor state.Actor) (agentcomm.ParticipantID, error) {
	if actor.Kind == actorKindUser {
		return meshUserParticipantID, nil
	}
	return "", fmt.Errorf("agentcomm: attach-and-send cannot send a first message for actor kind %q (only the human sends one today; the Side agent's own identity lands in T6.7)", actor.Kind)
}

// AttachAndSend implements T6.2: one ApplyRunOperation that creates the task (frozen profile snapshot), the
// mesh participant, the thread (fixed member set) and — when FirstMessage is set — the first message and its
// inbox reference. Any gate rejecting (unknown/unavailable profile, exceeded MaxTasks quota, a closing run, a
// terminal/script stage, delegation depth) writes NOTHING: Go-equivalent of admitChildren's "all or nothing"
// for a top-level attach instead of a child group.
//
// Design decisions worth making explicit (see this run's execution notes for the full reasoning):
//
//   - The new participant's mesh_participant_added effect is built BY HAND here, never via
//     ensureParticipantEffects (agentcomm_roster.go): that helper reads the CURRENT snapshot, which cannot see
//     a task this very operation has not committed yet (chicken/egg). It is still built the SAME shape
//     ensureParticipantEffects/helperParticipant would produce for a helper, or the genuinely-new descriptor
//     for a run-scoped role — one registration convention, not two. ensureParticipantEffects IS reused for
//     every member this call does NOT invent (the human "user" and any extra Recipients), because those ARE
//     resolvable from the current snapshot.
//   - MaxTasks quota: Attach (assistants.go) does not reserve against AssistantsState.QuotaReserved today —
//     only admitChildren's nested-delegation path does. CollaborationLimits.MaxTasks's own doc comment
//     ("top-level attaches plus every delegated descendant") already says a top-level attach SHOULD count;
//     AttachAndSend is the new composite dev-plan.md's own Implement line names an explicit
//     Expect{StageNotTerminal, MaxQuota} for, so it reserves 1 unit here — using the SAME existing
//     quotaReservedEffect/Preconditions.MaxQuota machinery admitChildren already established, not a new one.
func (o *Orchestrator) AttachAndSend(ctx context.Context, req AttachAndSendRequest) (AttachAndSendResult, error) {
	if !o.opts.AssistantsEnabled {
		return AttachAndSendResult{}, &state.OperationRejectedError{Code: assistants.CodeFeatureDisabled, Reason: "collaboration is disabled"}
	}
	if req.OperationID == "" {
		return AttachAndSendResult{}, errors.New("agentcomm: operation id is required")
	}

	scope := req.Scope
	if scope == "" {
		scope = assistants.ScopeStage
	}
	if !scope.Valid() {
		return AttachAndSendResult{}, fmt.Errorf("agentcomm: invalid scope %q", scope)
	}

	var targetStageID string
	switch scope {
	case assistants.ScopeStage:
		if req.StageID == "" {
			return AttachAndSendResult{}, errors.New("agentcomm: stage id is required for scope=stage")
		}
		st := o.reg().Stage(req.StageID)
		if st == nil {
			return AttachAndSendResult{}, fmt.Errorf("agentcomm: unknown stage %q", req.StageID)
		}
		if st.IsScript() {
			return AttachAndSendResult{}, fmt.Errorf("agentcomm: stage %q is a script stage — helpers are not supported there", req.StageID)
		}
		if cur := o.currentStatus(req.StageID); cur == state.StatusDone || cur == state.StatusFailed {
			return AttachAndSendResult{}, &state.OperationRejectedError{Code: assistants.CodeInvalidStatus, Reason: fmt.Sprintf("stage %q already reached %s", req.StageID, cur)}
		}
		targetStageID = req.StageID
	case assistants.ScopeRun:
		if req.StageID != "" {
			return AttachAndSendResult{}, errors.New("agentcomm: stage id must be empty for scope=run")
		}
	default:
		// Unreachable: scope.Valid() above already narrowed scope to ScopeStage or ScopeRun.
		return AttachAndSendResult{}, fmt.Errorf("agentcomm: unhandled scope %q", scope)
	}

	if o.opts.AssistantsRegistry == nil {
		return AttachAndSendResult{}, &state.OperationRejectedError{Code: assistants.CodeFeatureDisabled, Reason: "no assistant profiles configured"}
	}
	profile, ok := o.opts.AssistantsRegistry.Get(req.ProfileID)
	if !ok {
		return AttachAndSendResult{}, &state.OperationRejectedError{Code: assistants.CodeCapabilityUnavailable, Reason: fmt.Sprintf("unknown profile %q", req.ProfileID)}
	}
	view, _ := profileView(o.opts.AssistantsRegistry, req.ProfileID)
	if !view.Available {
		return AttachAndSendResult{}, &state.OperationRejectedError{Code: assistants.CodeCapabilityUnavailable, Reason: view.UnavailableReason}
	}
	// Freeze the availability verdict into the snapshot BEFORE it is handed to taskCreatedEffect — same
	// admission-time-freeze rule Attach/admitChildren already follow (see their own comments).
	profile.ReadonlyEnforcement = view.ReadonlyEnforcement

	rs := o.opts.Store.Snapshot()
	depth := assistants.ComputeDepth(nil) // top-level: AttachAndSend never has a parent task
	if rs.Assistants.Policy.Blocks(depth) {
		return AttachAndSendResult{}, &state.OperationRejectedError{Code: assistants.CodeDelegationDepthExceeded, Reason: fmt.Sprintf("max_depth=%d", rs.Assistants.Policy.MaxDepth)}
	}

	taskID := deriveTaskID(req.OperationID)
	mode := req.Mode
	if mode == "" {
		mode = assistants.ModeAdvisory
	}

	created, err := taskCreatedEffect(taskID, targetStageID, state.TaskCreatedPayload{
		Scope:                  string(scope),
		ProfileID:              string(req.ProfileID),
		ProfileSnapshot:        profile,
		Title:                  req.Title,
		Instruction:            req.Instruction,
		Mode:                   string(mode),
		Trigger:                string(assistants.TriggerOneShot),
		PolicyRev:              rs.Assistants.Policy.Revision,
		MaxDepthAtCreate:       rs.Assistants.Policy.MaxDepth,
		CreatedBy:              req.Actor.Kind,
		RootGenerationAtCreate: rs.Assistants.RootGenerations[targetStageID],
	})
	if err != nil {
		return AttachAndSendResult{}, err
	}
	effects := []state.Effect{created}

	// ScopeRun has no stage to wait on — it always starts eligible to run immediately, same as Attach's
	// parentActive branch for a top-level task under an already-running stage.
	startQueued := scope == assistants.ScopeRun || o.currentStatus(req.StageID) != state.StatusPending
	if startQueued {
		statusEff, serr := taskStatusEffect(taskID, assistants.StatusQueued, "")
		if serr != nil {
			return AttachAndSendResult{}, serr
		}
		effects = append(effects, statusEff)
	}

	var newParticipant agentcomm.Participant
	switch scope {
	case assistants.ScopeStage:
		newParticipant = agentcomm.Participant{
			ID: meshHelperParticipantID(taskID), Kind: agentcomm.KindHelper, Scope: agentcomm.ScopeStage,
			Alias: string(taskID), DisplayName: req.Title, StageID: req.StageID, Reachable: true,
		}
	case assistants.ScopeRun:
		newParticipant = agentcomm.Participant{
			ID: meshRunParticipantID(req.ProfileID), Kind: agentcomm.KindRunAgent, Scope: agentcomm.ScopeRun,
			Alias: string(req.ProfileID), DisplayName: req.Title, Reachable: true,
		}
	default:
		// Unreachable (see the earlier switch's default) — leaves newParticipant zero, caught by Valid() below.
	}
	if !newParticipant.Valid() {
		return AttachAndSendResult{}, fmt.Errorf("agentcomm: built an invalid participant for scope %q", scope)
	}
	newParticipantEff, err := meshParticipantAddedEffect(newParticipant)
	if err != nil {
		return AttachAndSendResult{}, err
	}
	effects = append(effects, newParticipantEff)

	// Every OTHER member this call does not itself invent (the human, and any extra Recipients) IS already
	// resolvable from rs — reuse ensureParticipantEffects exactly as OpenThread would, instead of a second
	// registration convention.
	knownIDs := append([]agentcomm.ParticipantID{meshUserParticipantID}, req.Recipients...)
	knownEffects, err := ensureParticipantEffects(rs, knownIDs)
	if err != nil {
		return AttachAndSendResult{}, fmt.Errorf("agentcomm: %w", err)
	}
	effects = append(effects, knownEffects...)

	members := []agentcomm.ParticipantID{meshUserParticipantID, newParticipant.ID}
	memberSet := map[agentcomm.ParticipantID]bool{meshUserParticipantID: true, newParticipant.ID: true}
	for _, r := range req.Recipients {
		if memberSet[r] {
			continue
		}
		memberSet[r] = true
		members = append(members, r)
	}
	memberStrs := make([]string, len(members))
	for i, m := range members {
		memberStrs[i] = string(m)
	}

	threadID := deriveMeshThreadID(req.OperationID)
	threadEff, err := marshalEffect("mesh_thread_opened", state.Target{ThreadID: string(threadID)},
		state.MeshThreadOpenedPayload{Title: req.Title, Scope: string(meshThreadScopeFor(scope)), Members: memberStrs})
	if err != nil {
		return AttachAndSendResult{}, err
	}
	effects = append(effects, threadEff)

	var messageID *agentcomm.MessageID
	var senderID agentcomm.ParticipantID
	if req.FirstMessage != nil {
		senderID, err = meshActorSenderID(req.Actor)
		if err != nil {
			return AttachAndSendResult{}, err
		}
		mid := deriveMeshMessageID(req.OperationID)
		messageID = &mid
		msgRecipients := make([]string, 0, len(members)-1)
		for _, m := range members {
			if m == senderID {
				continue
			}
			msgRecipients = append(msgRecipients, string(m))
		}
		body := redact.String(*req.FirstMessage, secretValuesForRedaction())
		msgEff, merr := marshalEffect("mesh_message_sent", state.Target{ThreadID: string(threadID), MessageID: string(mid)},
			state.MeshMessageSentPayload{Sender: string(senderID), Recipients: msgRecipients, Kind: string(agentcomm.MessageKindMessage), Body: body})
		if merr != nil {
			return AttachAndSendResult{}, merr
		}
		effects = append(effects, msgEff)
	}

	quotaEff, err := quotaReservedEffect(1)
	if err != nil {
		return AttachAndSendResult{}, err
	}
	effects = append(effects, quotaEff)

	var maxQuota *int
	if mt := o.opts.Config.Collaboration.Limits.MaxTasks; mt != nil {
		maxQuota = mt
	}
	expect := state.Preconditions{PolicyRevision: rs.Assistants.Policy.Revision, MaxQuota: maxQuota}
	if scope == assistants.ScopeStage {
		expect.StageNotTerminal = []string{req.StageID}
	}

	op := state.Operation{ID: req.OperationID, Kind: "attach_and_send", Actor: req.Actor, Expect: expect, Effects: effects}
	runCtx := o.runContext(ctx) // Инвариант №6: спавн не должен зависеть от HTTP request ctx.
	_, err = o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.publishNotice(bus.Event{Type: bus.EventAssistantTaskCreated, StageID: targetStageID, Data: map[string]any{
			"task_id": string(taskID), "profile_id": string(req.ProfileID),
		}})
		for _, m := range members {
			o.publishNotice(bus.Event{Type: bus.EventMeshParticipant, Data: map[string]any{"participant_id": string(m)}})
		}
		o.publishNotice(bus.Event{Type: bus.EventMeshThread, Data: map[string]any{"thread_id": string(threadID), "title": req.Title}})
		if messageID != nil {
			o.publishNotice(bus.Event{Type: bus.EventMeshMessage, Data: map[string]any{
				"message_id": string(*messageID), "thread_id": string(threadID), "sender_id": string(senderID), "kind": string(agentcomm.MessageKindMessage),
			}})
		}
		// Scheduling (and so spawning the executor) only happens when there is actual work to do: a role
		// attached with no FirstMessage must stay queued and addressable, never start its process — the
		// committed effects are identical either way (see startQueued above), only this externalWork step
		// differs on whether a message was sent.
		if startQueued && messageID != nil {
			o.maybeScheduleTask(runCtx, taskID)
		}
	})
	if err != nil {
		return AttachAndSendResult{}, err
	}

	return AttachAndSendResult{TaskID: taskID, ParticipantID: newParticipant.ID, ThreadID: threadID, MessageID: messageID}, nil
}
