package orchestrator

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// Dashboard and CLI share loop mutations; implicit ends are derived from the current projection.
// Only creation, delivered slots and explicit stops belong in the journal.

// actorKindAgent is state.Actor.Kind of a token-authenticated agent; its ID is its participant id.
const actorKindAgent = "agent"

// LoopStatus is where a loop stands: running, stopped by a person or agent, or over by itself.
type LoopStatus string

const (
	LoopActive  LoopStatus = "active"
	LoopStopped LoopStatus = "stopped"
	LoopEnded   LoopStatus = "ended"
)

// CreateLoopRequest creates a loop. Actor — and so the loop's creator — is set by the HTTP adapter from the
// authenticated caller, never from a request body.
type CreateLoopRequest struct {
	OperationID string
	// Target is the participant that receives the prompt: an id or an alias, "@" optional. Empty means the
	// creator itself, which only an agent may ask for — the human names one target.
	Target   agentcomm.ParticipantID
	Interval time.Duration
	Prompt   string
	// UntilStageID, when set, ends the loop with that stage.
	UntilStageID string
	// OriginStage is the stage Feed the human creates the loop in. An agent's origin is derived from the agent
	// (a helper's stage, none for a run agent) and this field is ignored for it.
	OriginStage string
	Actor       state.Actor
}

// StopLoopRequest stops the loop with the short id LoopID ("L1", case-insensitive). Only its creator or the
// human may.
type StopLoopRequest struct {
	OperationID string
	LoopID      string
	Actor       state.Actor
}

// ListLoopsRequest lists the loops Actor may see: every loop for the human, only its own for an agent.
type ListLoopsRequest struct {
	Actor state.Actor
}

// LoopPage is ListLoops' result, in creation order, stopped and ended loops included.
type LoopPage struct {
	Loops []LoopView `json:"loops"`
}

// LoopView is a loop as the dashboard and CLI show it.
type LoopView struct {
	ID        string                  `json:"id"`
	CreatorID agentcomm.ParticipantID `json:"creator_id"`
	TargetID  agentcomm.ParticipantID `json:"target_id"`
	// Every is the interval as a short label, such as "5m" or "1m30s".
	Every string `json:"every"`
	// OriginStage is the Feed the loop was created in; empty for a loop an agent made for the run.
	OriginStage string     `json:"origin_stage,omitempty"`
	UntilStage  string     `json:"until_stage,omitempty"`
	Status      LoopStatus `json:"status"`
	// NextTickAt is the first slot still in the future; set only while the loop is active.
	NextTickAt *time.Time `json:"next_tick_at,omitempty"`
	// EndReason says why a stopped or ended loop no longer ticks.
	EndReason string `json:"end_reason,omitempty"`
}

// CreateLoop saves the resolved participant so later alias changes cannot redirect ticks.
// A committed retry returns the original loop.
func (o *Orchestrator) CreateLoop(ctx context.Context, req CreateLoopRequest) (LoopView, error) {
	// The scheduler that delivers ticks only runs when AssistantsEnabled (Orchestrator.Run), matching
	// AttachAndSend's own gate: a loop created while it is off would sit at "active" and never tick.
	if !o.opts.AssistantsEnabled {
		return LoopView{}, &state.OperationRejectedError{Code: assistants.CodeFeatureDisabled, Reason: "collaboration is disabled"}
	}
	if req.OperationID == "" {
		return LoopView{}, errors.New("agentcomm: operation id is required")
	}
	if err := rejectReservedRequestID(req.OperationID, req.Actor); err != nil {
		return LoopView{}, err
	}
	creator, err := loopActor(req.Actor)
	if err != nil {
		return LoopView{}, err
	}
	req.Prompt = o.RedactSecrets(strings.TrimSpace(req.Prompt))
	key := deriveLoopKey(req.OperationID)

	rs := o.opts.Store.Snapshot()
	roster := o.liveRoster(rs)
	req.OriginStage = loopOriginStage(rs, creator, req)
	if view, replayed, err := o.replayedLoopCreate(rs, roster, key, creator, req); replayed {
		return view, err
	}
	if err := validateLoopRequest(rs, req); err != nil {
		return LoopView{}, err
	}
	if _, known := resolveParticipant(rs, creator); !known {
		return LoopView{}, attachRejected(assistants.CodeNotFound, "unknown participant %q", creator)
	}
	target, err := loopTarget(roster, creator, req.Target)
	if err != nil {
		return LoopView{}, err
	}

	eff, err := marshalEffect("loop_created", state.Target{LoopKey: key}, state.LoopCreatedPayload{
		CreatorID: creator, TargetID: target, Interval: req.Interval, Prompt: req.Prompt,
		UntilStageID: req.UntilStageID, OriginStageID: req.OriginStage, ThreadID: deriveLoopThreadID(key),
	})
	if err != nil {
		return LoopView{}, err
	}
	op := state.Operation{
		ID: req.OperationID, Kind: opKindLoopCreate, Actor: req.Actor, Effects: []state.Effect{eff},
		Expect: state.Preconditions{Check: func(rs *state.RunState) error { return o.checkLoopCreate(*rs, target, req.UntilStageID) }},
	}
	if _, err := o.opts.Store.ApplyRunOperation(op, nil); err != nil {
		return LoopView{}, err
	}
	o.loopWake.notify()
	return o.committedLoopView(key)
}

// loopActor is the participant id a loop operation is made as: the human, or the agent the token names.
func loopActor(actor state.Actor) (agentcomm.ParticipantID, error) {
	switch {
	case actor.Kind == actorKindUser:
		return meshUserParticipantID, nil
	case actor.Kind == actorKindAgent && actor.ID != "":
		return agentcomm.ParticipantID(actor.ID), nil
	}
	return "", attachRejected(assistants.CodeActorNotAllowed, "only the human and run agents may manage loops")
}

// loopOriginStage is the Feed a loop is shown in: the one the human created it in, or the stage an agent
// belongs to — never one an agent's request asks for.
func loopOriginStage(rs state.RunState, creator agentcomm.ParticipantID, req CreateLoopRequest) string {
	if creator == meshUserParticipantID {
		return req.OriginStage
	}
	return participantStage(rs, creator)
}

// validateLoopRequest checks what the request alone and the known stages decide; what can change before the
// commit (the target finishing, the active count) is checkLoopCreate's, under the commit's lock.
func validateLoopRequest(rs state.RunState, req CreateLoopRequest) error {
	switch {
	case req.Interval < agentcomm.MinLoopInterval:
		return attachRejected(assistants.CodeInvalidRequest, "a loop interval must be at least %s", agentcomm.MinLoopInterval)
	case req.Prompt == "":
		return attachRejected(assistants.CodeInvalidRequest, "a loop needs a prompt")
	case len(req.Prompt) > agentcomm.MaxLoopPromptBytes:
		return attachRejected(assistants.CodeInvalidRequest, "a loop prompt is at most %d bytes", agentcomm.MaxLoopPromptBytes)
	}
	for _, stage := range []struct{ what, id string }{{"origin stage", req.OriginStage}, {"stage bound", req.UntilStageID}} {
		if _, known := rs.Stages[stage.id]; stage.id != "" && !known {
			return attachRejected(assistants.CodeInvalidRequest, "unknown %s %q", stage.what, stage.id)
		}
	}
	return nil
}

// loopTarget resolves who receives the prompt. A loop can target a run agent or a stage helper: a stage's main
// agent and the Side agent are not loop targets, and the human is not an agent.
func loopTarget(roster []agentcomm.Participant, creator, requested agentcomm.ParticipantID) (agentcomm.ParticipantID, error) {
	name := requested
	if name == "" {
		if creator == meshUserParticipantID {
			return "", attachRejected(assistants.CodeInvalidRequest, "name one target for the loop, such as @agent (`afm agent list` shows who is in this run)")
		}
		name = creator
	}
	resolved, err := resolveRecipients(roster, []agentcomm.ParticipantID{name})
	if err != nil {
		return "", err
	}
	id := resolved[0]
	for _, p := range roster {
		if p.ID == id && p.Kind != agentcomm.KindRunAgent && p.Kind != agentcomm.KindHelper {
			return "", attachRejected(assistants.CodeInvalidRequest, "%s is a %s: a loop can target a run agent or a stage helper", id, p.Kind)
		}
	}
	return id, nil
}

// checkLoopCreate is the create operation's admission check, run against the state under the commit's own lock.
// A loop must not be born over: whatever would end the new loop by itself — final or not — refuses its creation.
func (o *Orchestrator) checkLoopCreate(rs state.RunState, target agentcomm.ParticipantID, untilStage string) error {
	candidate := state.LoopState{TargetID: target, UntilStageID: untilStage}
	if reason, _ := loopEndReason(rs, candidate); reason != "" {
		return attachRejected(assistants.CodeInvalidStatus, "the loop would be over from the start: %s", reason)
	}
	if err := o.rejectUnreachableRecipients(rs, []agentcomm.ParticipantID{target}); err != nil {
		return err
	}
	if n := activeLoopCount(rs); n >= agentcomm.MaxActiveLoops {
		return attachRejected(assistants.CodeBudgetExceeded, "%d loops are active already (at most %d): stop one first", n, agentcomm.MaxActiveLoops)
	}
	return nil
}

// replayedLoopCreate answers a create whose request id already committed with the loop that commit made. Any
// other request under that id is a conflict. replayed=false: nothing committed.
func (o *Orchestrator) replayedLoopCreate(rs state.RunState, roster []agentcomm.Participant, key string, creator agentcomm.ParticipantID, req CreateLoopRequest) (LoopView, bool, error) {
	op, committed, err := o.opts.Store.CommittedOperation(req.OperationID, opKindLoopCreate)
	if err != nil || !committed {
		return LoopView{}, err != nil, err // a storage error answers the request too
	}
	var p state.LoopCreatedPayload
	target, found, err := op.Effect("loop_created", &p)
	if err != nil {
		return LoopView{}, true, err
	}
	if !found || target.LoopKey != key || p.CreatorID != creator || p.Interval != req.Interval || p.UntilStageID != req.UntilStageID ||
		p.OriginStageID != req.OriginStage || !o.sameRedactedText(p.Prompt, req.Prompt) || !sameLoopTarget(roster, p, req.Target) {
		return LoopView{}, true, state.OperationIDConflictError(req.OperationID)
	}
	view, err := o.committedLoopView(key)
	return view, true, err
}

// sameLoopTarget reports whether the retry names the target the committed loop has: by its id, by an alias that
// resolves to it now, or by nothing for a loop the agent made for itself.
func sameLoopTarget(roster []agentcomm.Participant, committed state.LoopCreatedPayload, requested agentcomm.ParticipantID) bool {
	if requested == "" {
		return committed.TargetID == committed.CreatorID
	}
	resolved, err := resolveRecipients(roster, []agentcomm.ParticipantID{requested})
	return err == nil && resolved[0] == committed.TargetID
}

// StopLoop implements Service.StopLoop.
func (o *Orchestrator) StopLoop(ctx context.Context, req StopLoopRequest) (LoopView, error) {
	if req.OperationID == "" {
		return LoopView{}, errors.New("agentcomm: operation id is required")
	}
	if err := rejectReservedRequestID(req.OperationID, req.Actor); err != nil {
		return LoopView{}, err
	}
	actor, err := loopActor(req.Actor)
	if err != nil {
		return LoopView{}, err
	}
	rs := o.opts.Store.Snapshot()
	shortID := strings.ToUpper(strings.TrimSpace(req.LoopID))
	l, ok := rs.Assistants.LoopByShortID(shortID)
	if !ok {
		return LoopView{}, attachRejected(assistants.CodeNotFound, "unknown loop %q — /loop list (or `afm agent loop list`) shows this run's loops", req.LoopID)
	}
	if actor != meshUserParticipantID && actor != l.CreatorID {
		return LoopView{}, attachRejected(assistants.CodeActorNotAllowed, "only the loop's creator or the human may stop %s", l.ShortID)
	}

	if view, replayed, err := o.replayedLoopStop(l.Key, req.OperationID); replayed {
		return view, err
	}
	eff, err := marshalEffect("loop_stopped", state.Target{LoopKey: l.Key}, struct{}{})
	if err != nil {
		return LoopView{}, err
	}
	op := state.Operation{ID: req.OperationID, Kind: opKindLoopStop, Actor: req.Actor, Effects: []state.Effect{eff}}
	if _, err := o.opts.Store.ApplyRunOperation(op, nil); err != nil {
		return LoopView{}, err
	}
	o.loopWake.notify()
	return o.committedLoopView(l.Key)
}

// replayedLoopStop answers a stop whose request id already committed with the loop it stopped; the same id
// stopping another loop is a conflict. replayed=false: nothing committed.
func (o *Orchestrator) replayedLoopStop(key, operationID string) (LoopView, bool, error) {
	op, committed, err := o.opts.Store.CommittedOperation(operationID, opKindLoopStop)
	if err != nil || !committed {
		return LoopView{}, err != nil, err
	}
	target, found, err := op.Effect("loop_stopped", &struct{}{})
	if err != nil {
		return LoopView{}, true, err
	}
	if !found || target.LoopKey != key {
		return LoopView{}, true, state.OperationIDConflictError(operationID)
	}
	view, err := o.committedLoopView(key)
	return view, true, err
}

// ListLoops implements Service.ListLoops.
func (o *Orchestrator) ListLoops(ctx context.Context, req ListLoopsRequest) (LoopPage, error) {
	actor, err := loopActor(req.Actor)
	if err != nil {
		return LoopPage{}, err
	}
	rs := o.opts.Store.Snapshot()
	now := o.clk().Now()
	page := LoopPage{Loops: []LoopView{}}
	for _, l := range rs.Assistants.OrderedLoops() {
		if actor == meshUserParticipantID || actor == l.CreatorID {
			page.Loops = append(page.Loops, loopView(rs, l, now))
		}
	}
	return page, nil
}

// committedLoopView is the loop key as the projection holds it now: the short id is allocated by the
// projection, so it is read back after the commit, for a first create and a replay alike.
func (o *Orchestrator) committedLoopView(key string) (LoopView, error) {
	rs := o.opts.Store.Snapshot()
	l, ok := rs.Assistants.Loops[key]
	if !ok {
		return LoopView{}, fmt.Errorf("loop %s is committed but missing from the projection", key)
	}
	return loopView(rs, *l, o.clk().Now()), nil
}

// loopView derives what a reader sees of l at now.
func loopView(rs state.RunState, l state.LoopState, now time.Time) LoopView {
	v := LoopView{
		ID: l.ShortID, CreatorID: l.CreatorID, TargetID: l.TargetID, Every: formatLoopInterval(l.Interval),
		OriginStage: l.OriginStageID, UntilStage: l.UntilStageID, Status: LoopActive,
	}
	switch reason, _ := loopEndReason(rs, l); {
	case l.Stopped():
		v.Status, v.EndReason = LoopStopped, "stopped by "+cmp.Or(l.StoppedBy.ID, l.StoppedBy.Kind)
	case reason != "":
		v.Status, v.EndReason = LoopEnded, reason
	default:
		next := nextLoopSlot(l, now)
		at := l.SlotTime(next)
		v.NextTickAt = &at
	}
	return v
}

// nextLoopSlot is the first slot of l that is still in the future at now and was not delivered yet: slots are
// fixed to the creation time, and one that passed unserved is skipped, never caught up.
func nextLoopSlot(l state.LoopState, now time.Time) uint64 {
	next := uint64(max(now.Sub(l.CreatedAt)/l.Interval, 0)) + 1
	return max(next, l.LastSlot+1)
}

// loopEnd is one reason a loop no longer ticks by itself. final says it can never come back; a reversible end
// (a failed stage or task that a Retry may requeue) leaves the loop's slot taken and the loop stoppable.
type loopEnd struct {
	reason string
	final  bool
}

// loopEndReason derives implicit ends without extra journal records.
// Permanent ends take precedence over failures that a Retry can reverse.
func loopEndReason(rs state.RunState, l state.LoopState) (reason string, final bool) {
	var reversible string
	for _, end := range []loopEnd{flowEnd(rs), stageEnd(rs, l.UntilStageID), loopTargetEnd(rs, l.TargetID), loopThreadEnd(rs, l)} {
		switch {
		case end.reason == "":
		case end.final:
			return end.reason, true
		case reversible == "":
			reversible = end.reason
		default:
		}
	}
	return reversible, false
}

// flowEnd is the end of a flow with nothing left to run. A failed stage can be retried, so it is final only
// when every stage is done.
func flowEnd(rs state.RunState) loopEnd {
	if !allStagesTerminal(rs) {
		return loopEnd{}
	}
	for _, s := range rs.Stages {
		if s.Status == state.StatusFailed {
			return loopEnd{reason: "every stage of the flow has finished, one of them failed"}
		}
	}
	return loopEnd{reason: "every stage of the flow has finished", final: true}
}

// stageEnd is the end of a loop bound to stageID: final for a done stage, reversible for a failed one.
func stageEnd(rs state.RunState, stageID string) loopEnd {
	if !stageTerminal(rs, stageID) {
		return loopEnd{}
	}
	return loopEnd{reason: fmt.Sprintf("stage %s has ended", stageID), final: rs.Stages[stageID].Status == state.StatusDone}
}

// loopThreadEnd is the end of a loop whose own thread is closed or full; both are final.
func loopThreadEnd(rs state.RunState, l state.LoopState) loopEnd {
	th, ok := rs.Assistants.Mesh.Threads[l.ThreadID]
	switch {
	case !ok:
		return loopEnd{}
	case th.Status != agentcomm.ThreadOpen:
		return loopEnd{reason: "the loop's thread was closed", final: true}
	case !loopThreadHasRoom(len(rs.Assistants.Mesh.MessagesByThread[l.ThreadID])):
		return loopEnd{reason: "the loop's thread reached its message limit", final: true}
	}
	return loopEnd{}
}

// loopTargetEnd lets successful helpers restart until their stage ends.
// Failed tasks may recover through Retry; cancellation or supersession ends the loop permanently.
func loopTargetEnd(rs state.RunState, target agentcomm.ParticipantID) loopEnd {
	p, known := resolveParticipant(rs, target)
	if !known {
		return loopEnd{reason: fmt.Sprintf("target %s no longer exists", target), final: true}
	}
	if p.Kind == agentcomm.KindHelper {
		return helperTargetEnd(rs, target, p)
	}
	if p.Reachable {
		return loopEnd{}
	}
	if taskID, ok := runTaskForParticipant(rs, target); ok {
		return loopEnd{reason: p.UnreachableReason, final: rs.Assistants.Tasks[taskID].Status != assistants.StatusFailed}
	}
	return loopEnd{reason: p.UnreachableReason, final: true}
}

// helperTargetEnd is loopTargetEnd for a stage helper. Final ends are checked before reversible ones.
func helperTargetEnd(rs state.RunState, target agentcomm.ParticipantID, p agentcomm.Participant) loopEnd {
	taskID := assistants.TaskID(strings.TrimPrefix(string(target), meshHelperPrefix))
	t := rs.Assistants.Tasks[taskID]
	switch {
	case helperSuperseded(rs, t):
		return loopEnd{reason: fmt.Sprintf("helper %s was superseded by a new attempt of stage %s", taskID, t.RootStageID), final: true}
	case t.Status == assistants.StatusCancelled:
		return loopEnd{reason: p.UnreachableReason, final: true}
	}
	if end := stageEnd(rs, p.StageID); end.reason != "" {
		return end
	}
	if t.Status == assistants.StatusFailed {
		return loopEnd{reason: p.UnreachableReason}
	}
	return loopEnd{}
}

// helperSuperseded reports whether t belongs to an earlier attempt of its stage (a new root generation) or, for
// a nested helper, of its parent (a retry): such a task is invisible to the current attempt, whatever its status.
func helperSuperseded(rs state.RunState, t *state.TaskState) bool {
	if t.RootGeneration != rs.Assistants.RootGenerations[t.RootStageID] {
		return true
	}
	if t.ParentTaskID == nil {
		return false
	}
	parent, ok := rs.Assistants.Tasks[*t.ParentTaskID]
	return ok && t.ParentGeneration != parent.TaskGeneration
}

// loopThreadHasRoom reports whether a thread already holding messages messages can take one more tick and
// the reply to it. A loop's thread is opened without a budget of its own, so the global limit applies.
func loopThreadHasRoom(messages int) bool {
	return messages+2 <= agentcomm.MaxThreadMessages
}

// activeLoopCount is how many loops hold a slot of the limit: not stopped, and not over for good. A loop with
// a reversible end holds its slot, because a Retry brings it back.
func activeLoopCount(rs state.RunState) int {
	n := 0
	for _, l := range rs.Assistants.OrderedLoops() {
		if _, final := loopEndReason(rs, l); !l.Stopped() && !final {
			n++
		}
	}
	return n
}

func stageTerminal(rs state.RunState, stageID string) bool {
	s, known := rs.Stages[stageID]
	return known && bus.IsTerminal(s.Status)
}

// allStagesTerminal reports whether every stage of the flow is done or failed: nothing is left to run.
func allStagesTerminal(rs state.RunState) bool {
	for id := range rs.Stages {
		if !stageTerminal(rs, id) {
			return false
		}
	}
	return len(rs.Stages) > 0
}

// formatLoopInterval is d as a short label: "5m" rather than "5m0s", "1h" rather than "1h0m0s".
func formatLoopInterval(d time.Duration) string {
	s := d.String()
	if t, ok := strings.CutSuffix(s, "m0s"); ok {
		s = t + "m"
	}
	if t, ok := strings.CutSuffix(s, "h0m"); ok {
		s = t + "h"
	}
	return s
}

// loopTickBody labels scheduled prompts so recipients and humans can identify and stop their loop.
func loopTickBody(l state.LoopState) string {
	return fmt.Sprintf("⟳ Loop %s · every %s\n(a scheduled prompt: the loop's creator or the human can stop %s)\n\n%s",
		l.ShortID, formatLoopInterval(l.Interval), l.ShortID, l.Prompt)
}

// deriveLoopKey is a loop's stable internal key, derived from the operation that created it.
func deriveLoopKey(createOperationID string) string {
	return "loop-" + deriveMeshID("loop", createOperationID)
}

// deriveLoopThreadID is the mesh thread a loop's messages go to, one per loop so scheduled messages never share
// an ordinary conversation's message budget.
func deriveLoopThreadID(loopKey string) agentcomm.ThreadID {
	return agentcomm.ThreadID("mt-" + deriveMeshID("loop-thread", loopKey))
}

// loopKeyOfThread keeps ordinary sends out of a loop thread and preserves its message budget.
func loopKeyOfThread(rs state.RunState, threadID agentcomm.ThreadID) (string, bool) {
	for key := range rs.Assistants.Loops {
		if deriveLoopThreadID(key) == threadID {
			return key, true
		}
	}
	return "", false
}
