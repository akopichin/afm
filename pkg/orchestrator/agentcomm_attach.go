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
	"slices"
	"strings"
	"unicode"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
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

// meshRunParticipantID returns a scope=run role's participant id — "run:<taskID>", keyed on the task like a
// helper's "helper:<taskID>", so several run agents of the SAME profile ("new1" and "new2", both UX experts)
// are distinct participants. Idempotency of one "Add run agent" submission is the caller's request id.
func meshRunParticipantID(taskID assistants.TaskID) agentcomm.ParticipantID {
	return agentcomm.ParticipantID(meshRunPrefix + string(taskID))
}

// runAgentAlias is the base of the short @-name a run agent answers to: its title as a handle ("New QA" →
// "new-qa"), else the profile id. uniqueAlias makes it unique within the run.
func runAgentAlias(title string, profileID assistants.ProfileID) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-':
			b.WriteRune(r)
		case unicode.IsSpace(r):
			b.WriteByte('-')
		default:
		}
	}
	alias := strings.Trim(b.String(), "-")
	for strings.Contains(alias, "--") {
		alias = strings.ReplaceAll(alias, "--", "-")
	}
	if alias != "" {
		return alias
	}
	return string(profileID)
}

// uniqueAlias returns base, or base-2, base-3, … — the first that no participant in roster answers to, so
// an @name always names exactly one participant.
func uniqueAlias(roster []agentcomm.Participant, base string) string {
	taken := func(alias string) bool {
		return slices.ContainsFunc(roster, func(p agentcomm.Participant) bool { return strings.EqualFold(p.Alias, alias) })
	}
	alias := base
	for n := 2; taken(alias); n++ {
		alias = fmt.Sprintf("%s-%d", base, n)
	}
	return alias
}

// aliasTaken is the admission check for a new participant's alias, run under the commit's lock: two agents
// added at the same moment must not both get the same @name.
func aliasTaken(rs state.RunState, alias string) error {
	for _, p := range rs.Assistants.Mesh.Participants {
		if strings.EqualFold(p.Alias, alias) {
			return attachRejected(assistants.CodeStaleRevision, "the name @%s was just taken by another participant — add the agent again", alias)
		}
	}
	return nil
}

// newTaskParticipantID is the mesh identity a newly attached task gets: "run:<taskID>" for a run agent,
// "helper:<taskID>" for a helper on a stage.
func newTaskParticipantID(scope assistants.Scope, taskID assistants.TaskID) agentcomm.ParticipantID {
	if scope == assistants.ScopeRun {
		return meshRunParticipantID(taskID)
	}
	return meshHelperParticipantID(taskID)
}

// attachThreadMembers is the member set of an attach's first thread: the human, the new participant, then
// every extra recipient once.
func attachThreadMembers(newParticipant agentcomm.ParticipantID, recipients []agentcomm.ParticipantID) []agentcomm.ParticipantID {
	members := []agentcomm.ParticipantID{meshUserParticipantID, newParticipant}
	for _, r := range recipients {
		if !slices.Contains(members, r) {
			members = append(members, r)
		}
	}
	return members
}

// replayedAttachAndSend answers an AttachAndSend whose request id already committed, from the committed
// operation: its task, its thread's exact member set and its first message (or its absence) must all be what
// req asks for, otherwise the request id was reused for another request — a conflict.
func (o *Orchestrator) replayedAttachAndSend(op state.CommittedOperation, treq topLevelTaskRequest, req AttachAndSendRequest) (AttachAndSendResult, error) {
	taskID, err := o.replayedTopLevelTask(op, treq)
	if err != nil {
		return AttachAndSendResult{}, err
	}
	res := AttachAndSendResult{TaskID: taskID, ParticipantID: newTaskParticipantID(normalizeScope(req.Scope), taskID), ThreadID: deriveMeshThreadID(req.OperationID)}

	var thread state.MeshThreadOpenedPayload
	threadTarget, found, err := op.Effect("mesh_thread_opened", &thread)
	if err != nil {
		return AttachAndSendResult{}, err
	}
	want := attachThreadMembers(res.ParticipantID, req.Recipients)
	got := make([]agentcomm.ParticipantID, len(thread.Members))
	for i, m := range thread.Members {
		got[i] = agentcomm.ParticipantID(m)
	}
	if !found || agentcomm.ThreadID(threadTarget.ThreadID) != res.ThreadID || !slices.Equal(sortedMemberSet(got), sortedMemberSet(want)) {
		return AttachAndSendResult{}, state.OperationIDConflictError(req.OperationID)
	}

	var msg state.MeshMessageSentPayload
	msgTarget, sent, err := op.Effect("mesh_message_sent", &msg)
	if err != nil {
		return AttachAndSendResult{}, err
	}
	if sent != (req.FirstMessage != nil) || (sent && !o.sameRedactedText(msg.Body, *req.FirstMessage)) {
		return AttachAndSendResult{}, state.OperationIDConflictError(req.OperationID)
	}
	if sent {
		mid := agentcomm.MessageID(msgTarget.MessageID)
		res.MessageID = &mid
	}
	return res, nil
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
	return "", attachRejected(assistants.CodeActorNotAllowed, "only the human sends an attach's first message, not actor kind %q", actor.Kind)
}

// attachRejected is a typed, correctable AttachAndSend rejection — the API answers with its code's 4xx and the
// reason, not a bare 500.
func attachRejected(code assistants.ErrorCode, format string, args ...any) error {
	return &state.OperationRejectedError{Code: code, Reason: fmt.Sprintf(format, args...)}
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
//   - The task itself is admitted by admitTopLevelTask, shared with Attach (assistants.go): one place for the
//     stage/profile/depth rules, defaults, the initial status and the MaxTasks quota reservation
//     (CollaborationLimits.MaxTasks: "top-level attaches plus every delegated descendant").
func (o *Orchestrator) AttachAndSend(ctx context.Context, req AttachAndSendRequest) (AttachAndSendResult, error) {
	if !o.opts.AssistantsEnabled {
		return AttachAndSendResult{}, &state.OperationRejectedError{Code: assistants.CodeFeatureDisabled, Reason: "collaboration is disabled"}
	}
	if req.OperationID == "" {
		return AttachAndSendResult{}, errors.New("agentcomm: operation id is required")
	}
	treq := topLevelTaskRequest{
		OperationID: req.OperationID, ProfileID: req.ProfileID, Scope: req.Scope, StageID: req.StageID,
		Title: req.Title, Instruction: req.Instruction, Mode: req.Mode, Actor: req.Actor,
	}
	if op, committed, err := o.opts.Store.CommittedOperation(req.OperationID, opKindAttachAndSend); err != nil || committed {
		if err != nil {
			return AttachAndSendResult{}, err
		}
		return o.replayedAttachAndSend(op, treq, req)
	}

	rs := o.opts.Store.Snapshot()
	task, err := o.admitTopLevelTask(rs, treq)
	if err != nil {
		return AttachAndSendResult{}, err
	}
	scope, taskID, title := task.Scope, task.ID, task.Title
	targetStageID := req.StageID
	effects := slices.Clone(task.Effects)

	newParticipant := agentcomm.Participant{
		ID: newTaskParticipantID(scope, taskID), Kind: agentcomm.KindHelper, Scope: agentcomm.ScopeStage,
		Alias: string(taskID), DisplayName: title, StageID: req.StageID, Reachable: true,
	}
	if scope == assistants.ScopeRun {
		newParticipant = agentcomm.Participant{
			ID: newParticipant.ID, Kind: agentcomm.KindRunAgent, Scope: agentcomm.ScopeRun,
			Alias: uniqueAlias(o.liveRoster(rs), runAgentAlias(title, req.ProfileID)), DisplayName: title, Reachable: true,
		}
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

	members := attachThreadMembers(newParticipant.ID, req.Recipients)
	memberStrs := make([]string, len(members))
	for i, m := range members {
		memberStrs[i] = string(m)
	}

	threadID := deriveMeshThreadID(req.OperationID)
	threadEff, err := marshalEffect("mesh_thread_opened", state.Target{ThreadID: string(threadID)},
		state.MeshThreadOpenedPayload{Title: title, Scope: string(meshThreadScopeFor(scope)), Members: memberStrs})
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
		body := o.RedactSecrets(*req.FirstMessage)
		msgEff, merr := marshalEffect("mesh_message_sent", state.Target{ThreadID: string(threadID), MessageID: string(mid)},
			state.MeshMessageSentPayload{Sender: string(senderID), Recipients: msgRecipients, Kind: string(agentcomm.MessageKindMessage), Body: body})
		if merr != nil {
			return AttachAndSendResult{}, merr
		}
		effects = append(effects, msgEff)
	}

	expect := task.Expect
	expect.Check = func(cur *state.RunState) error {
		if scope == assistants.ScopeRun {
			if err := aliasTaken(*cur, newParticipant.Alias); err != nil {
				return err
			}
		}
		if req.FirstMessage != nil {
			// The first message also goes to every extra member — the same reachability rule as Send.
			return rejectUnreachableRecipients(*cur, req.Recipients)
		}
		return nil
	}

	op := state.Operation{ID: req.OperationID, Kind: opKindAttachAndSend, Actor: req.Actor, Expect: expect, Effects: effects}
	runCtx := o.runContext(ctx) // Инвариант №6: спавн не должен зависеть от HTTP request ctx.
	_, err = o.opts.Store.ApplyRunOperation(op, func(state.OperationResult) {
		o.publishNotice(bus.Event{Type: bus.EventAssistantTaskCreated, StageID: targetStageID, Data: map[string]any{
			"task_id": string(taskID), "profile_id": string(req.ProfileID),
		}})
		for _, m := range members {
			o.publishNotice(bus.Event{Type: bus.EventMeshParticipant, Data: map[string]any{"participant_id": string(m)}})
		}
		o.publishNotice(bus.Event{Type: bus.EventMeshThread, Data: map[string]any{"thread_id": string(threadID), "title": title}})
		if messageID != nil {
			o.publishNotice(bus.Event{Type: bus.EventMeshMessage, Data: map[string]any{
				"message_id": string(*messageID), "thread_id": string(threadID), "sender_id": string(senderID), "kind": string(agentcomm.MessageKindMessage),
			}})
		}
		// Scheduling (and so spawning the executor) only happens when there is actual work to do: a role
		// attached with no FirstMessage must stay queued and addressable, never start its process — the
		// committed effects are identical either way (the admitted initial status), only this externalWork step
		// differs on whether a message was sent.
		if task.Status == assistants.StatusQueued && messageID != nil {
			o.maybeScheduleTask(runCtx, taskID)
		}
	})
	if err != nil {
		return AttachAndSendResult{}, err
	}

	return AttachAndSendResult{TaskID: taskID, ParticipantID: newParticipant.ID, ThreadID: threadID, MessageID: messageID}, nil
}
