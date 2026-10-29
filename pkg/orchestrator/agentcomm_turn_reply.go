package orchestrator

import (
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/prompts"
	"github.com/akopichin/afm/pkg/state"
)

// A run agent's accepted turn result is posted as a reply to the message that woke the turn, so a colleague who
// asked gets an answer even from an agent that never writes on its own. The reply is PASSIVE
// (agentcomm.Message.AutoResult): it waits in the recipient's inbox for its next turn but never wakes it — two
// agents answering each other's turn results would otherwise keep each other busy forever. It is committed by
// the turn's own finish operation (finishTaskTurnReplying), and never keeps that turn from finishing.

// errTurnAlreadyAnswered rejects a turn reply whose agent answered the message it replies to itself during the
// turn.
var errTurnAlreadyAnswered = errors.New("the agent already replied to the message that woke its turn")

// turnReplyMessageID is the id of the reply that ends taskID's turn answering inReplyTo — derived from
// turn-reply:<task>/<turn>/<message>, so a turn posts at most one reply to each message.
func turnReplyMessageID(taskID assistants.TaskID, turn uint64, inReplyTo agentcomm.MessageID) agentcomm.MessageID {
	return deriveMeshMessageID(fmt.Sprintf("turn-reply:%s/%d/%s", taskID, turn, inReplyTo))
}

// wakingMessage returns the message that woke a turn handed inbox (its inventory, in inbox order): the last one
// that is not itself a passive turn result. ok=false: the turn had no such input.
func wakingMessage(ms state.MeshState, inbox []agentcomm.MessageID) (*agentcomm.Message, bool) {
	for i := len(inbox) - 1; i >= 0; i-- {
		if m := ms.Messages[inbox[i]]; m != nil && !m.AutoResult {
			return m, true
		}
	}
	return nil, false
}

// answeredBy reports whether sender has already replied to messageID.
func answeredBy(ms state.MeshState, sender agentcomm.ParticipantID, messageID agentcomm.MessageID) bool {
	for _, m := range ms.Messages {
		if m.Sender == sender && m.InReplyTo != nil && *m.InReplyTo == messageID {
			return true
		}
	}
	return false
}

// turnReplyTargets are the messages the end of t's current turn answers: the message that woke this turn, and
// each request an earlier turn ended waiting on (state.TaskState.OpenRequests) that someone still waits for an
// answer to (a pending reply_to wait) — a turn woken only by a passive result owes that requester its answer,
// or the requester would wait forever while the agent idles. A request nobody waits on is left alone: the
// agent may have answered it some other way. Each target once, without those the agent answered itself.
func turnReplyTargets(rs state.RunState, t *state.TaskState, agent agentcomm.ParticipantID) []*agentcomm.Message {
	var ids []agentcomm.MessageID
	for _, id := range t.OpenRequests {
		if hasPendingReplyWait(rs, id) {
			ids = append(ids, id)
		}
	}
	if waking, ok := wakingMessage(rs.Assistants.Mesh, t.TurnInbox); ok && !slices.Contains(ids, waking.ID) {
		ids = append(ids, waking.ID)
	}
	var out []*agentcomm.Message
	for _, id := range ids {
		if m := rs.Assistants.Mesh.Messages[id]; m != nil && m.Sender != agent && !answeredBy(rs.Assistants.Mesh, agent, id) && !tickOfAGoneCreator(rs, m) {
			out = append(out, m)
		}
	}
	return out
}

// tickOfAGoneCreator suppresses automatic replies to a loop creator that cannot receive them.
// The loop follows its target lifetime, so a finished creator must not break later ticks.
func tickOfAGoneCreator(rs state.RunState, m *agentcomm.Message) bool {
	if m.LoopKey == "" {
		return false
	}
	creator, known := resolveParticipant(rs, m.Sender)
	return !known || !creator.Reachable
}

// hasPendingReplyWait reports whether somebody still waits for a reply to messageID.
func hasPendingReplyWait(rs state.RunState, messageID agentcomm.MessageID) bool {
	for _, w := range rs.Assistants.Mesh.Waits {
		if w.Status == agentcomm.WaitPending && w.Condition.Kind == agentcomm.WaitOnReplyTo && w.Condition.MessageID == messageID {
			return true
		}
	}
	return false
}

// planTurnReplies plans the replies that end taskID's turn with body (turnReplyBody, or turnFailureBody for a
// turn whose answer was rejected): one to each of turnReplyTargets, back to its sender, in its thread — through
// planMessage, so a reply to a Discussion post is mirrored into the Discussion too. None: the turn had nothing
// to answer, or the agent answered it all itself.
func (o *Orchestrator) planTurnReplies(taskID assistants.TaskID, turn uint64, body string) []plannedMessage {
	rs := o.opts.Store.Snapshot()
	t, ok := rs.Assistants.Tasks[taskID]
	if !ok || turn == 0 {
		return nil
	}
	agent := meshParticipantForTask(rs, t.Task)
	var replies []plannedMessage
	for _, m := range turnReplyTargets(rs, t, agent) {
		inReplyTo := m.ID
		msg, err := o.planMessage(rs, SendRequest{
			ThreadID: m.ThreadID, Sender: agent, Recipients: []agentcomm.ParticipantID{m.Sender},
			Kind: agentcomm.MessageKindReply, InReplyTo: &inReplyTo, Body: body, AutoResult: true,
		}, turnReplyMessageID(taskID, turn, m.ID), nil)
		if err != nil {
			log.Printf("WARN: assistants: plan the turn reply of task %s to %s: %v", taskID, m.ID, err)
			continue
		}
		replies = append(replies, msg)
	}
	return replies
}

// unansweredReplies drops the replies whose message the agent has answered itself meanwhile.
func unansweredReplies(rs state.RunState, replies []plannedMessage) []plannedMessage {
	return slices.DeleteFunc(slices.Clone(replies), func(r plannedMessage) bool {
		return answeredBy(rs.Assistants.Mesh, r.send.Sender, *r.send.InReplyTo)
	})
}

// turnRepliesCheck is the commit-time check of a finish operation carrying replies: the agent has not answered
// any of their messages itself meanwhile (errTurnAlreadyAnswered — the same turn's explicit reply may commit
// after planTurnReplies looked), and every recipient can still receive a message.
func (o *Orchestrator) turnRepliesCheck(replies []plannedMessage) func(*state.RunState) error {
	reachable := o.recipientsReachable(replies)
	return func(rs *state.RunState) error {
		if len(unansweredReplies(*rs, replies)) != len(replies) {
			return &state.OperationRejectedError{Code: assistants.CodeInvalidStatus, Reason: errTurnAlreadyAnswered.Error(), Cause: errTurnAlreadyAnswered}
		}
		return reachable(rs)
	}
}

// undeliveredReply is a turn reply that could not be delivered, and why.
type undeliveredReply struct {
	reply  plannedMessage
	reason string
}

// splitDeliverable splits replies into those that can still be delivered against rs and those that cannot, so
// one full conversation never costs another requester its answer. Replies are accepted one by one, in order,
// each checked together with those already accepted — turnRepliesCheck's commit-time rule and the projection's
// own limits (RunState.CheckEffects: a full conversation or inbox) — so two replies competing for a
// conversation's last slot deliver one, not neither. The commit validates the accepted set again.
func (o *Orchestrator) splitDeliverable(rs state.RunState, replies []plannedMessage) ([]plannedMessage, []undeliveredReply) {
	var good []plannedMessage
	var bad []undeliveredReply
	var accepted []state.Effect
	for _, r := range replies {
		candidate := append(slices.Clone(good), r)
		err := o.turnRepliesCheck(candidate)(&rs)
		if err == nil {
			err = rs.CheckEffects(append(slices.Clone(accepted), r.effects...))
		}
		if err != nil {
			bad = append(bad, undeliveredReply{reply: r, reason: "the answer to it could not be delivered: " + o.RedactSecrets(rejectionReason(err))})
			continue
		}
		good = candidate
		accepted = append(accepted, r.effects...)
	}
	return good, bad
}

// undeliveredReplyWaitEffects cancels the pending reply_to waits for the messages undelivered replies would
// have answered, each with its reply's reason, when nobody else among the message's recipients can still answer
// it: the agent has finished its turn for that request, and its answer could not be delivered, so a requester
// waiting for it would wait forever. The cancellations commit with the finish that drops the replies; reasons
// maps each cancelled wait to its reason, for the notice.
func undeliveredReplyWaitEffects(rs state.RunState, undelivered []undeliveredReply) ([]state.Effect, map[agentcomm.WaitID]string) {
	var ids []agentcomm.WaitID
	reasons := map[agentcomm.WaitID]string{}
	for _, u := range undelivered {
		r := u.reply
		m := rs.Assistants.Mesh.Messages[*r.send.InReplyTo]
		if m == nil || slices.ContainsFunc(m.Recipients, func(p agentcomm.ParticipantID) bool {
			return p != r.send.Sender && canStillAnswer(rs, agentcomm.WaitCondition{Kind: agentcomm.WaitOnReplyTo, MessageID: m.ID}, p)
		}) {
			continue
		}
		for _, id := range sortedWaitIDs(rs.Assistants.Mesh.Waits) {
			w := rs.Assistants.Mesh.Waits[id]
			if w.Status != agentcomm.WaitPending || w.Condition.Kind != agentcomm.WaitOnReplyTo || w.Condition.MessageID != m.ID {
				continue
			}
			ids = append(ids, id)
			reasons[id] = u.reason
		}
	}
	return cancelWaitEffects(ids, reasons), reasons
}

// turnReplyBody is the text of a turn reply: the result's summary, cut to fit the mesh's body limit, and a line
// with the verdict, the number of findings and where the full result is.
func turnReplyBody(taskID assistants.TaskID, turn uint64, r assistants.Result) string {
	footer := "\n\n— verdict " + string(r.Verdict)
	switch n := len(r.Findings); n {
	case 0:
	case 1:
		footer += ", 1 finding"
	default:
		footer += fmt.Sprintf(", %d findings", n)
	}
	footer += fmt.Sprintf("; full result: task %s, turn %d", taskID, turn)
	summary := strings.TrimSpace(r.Summary)
	if summary == "" {
		summary = "(no summary)"
	}
	return prompts.TruncateUTF8(summary, agentcomm.MaxBodyBytes-len(footer), "…") + footer
}

// turnFailureBody is the text of the reply that ends a run agent's turn whose answer could not be accepted:
// who could not answer, why, and that writing again starts a new turn.
func turnFailureBody(taskID assistants.TaskID, turn uint64, agent, reason string) string {
	footer := fmt.Sprintf("\n\n— turn failed; write again to retry; task %s, turn %d", taskID, turn)
	text := fmt.Sprintf("%s could not produce a valid result: %s", agent, describeTaskFailure(reason))
	return prompts.TruncateUTF8(text, agentcomm.MaxBodyBytes-len(footer), "…") + footer
}

// protocolErrorPrefix starts the status reason of a turn whose answer broke the result protocol.
const protocolErrorPrefix = "protocol_error: "

const noReasonRecorded = "no reason recorded"

// describeTaskFailure says in plain words why a task's turn failed: a protocol error is an invalid result.
func describeTaskFailure(reason string) string {
	if detail, ok := strings.CutPrefix(reason, protocolErrorPrefix); ok {
		return "invalid result — " + detail
	}
	if reason == "" {
		return noReasonRecorded
	}
	return reason
}

// noticeResultNotDelivered tells the feed that taskID's result was committed without the message that should
// have carried it, and why.
func (o *Orchestrator) noticeResultNotDelivered(taskID assistants.TaskID, stageID string, cause error) {
	reason := rejectionReason(cause)
	o.publishNotice(bus.Event{Type: bus.EventContextWarning, StageID: stageID, Data: fmt.Sprintf(
		"turn result of task %s not delivered: %s", taskID, o.RedactSecrets(reason))})
}

// rejectionReason is the human-readable reason of a rejected operation.
func rejectionReason(err error) string {
	var rejected *state.OperationRejectedError
	if errors.As(err, &rejected) {
		return rejected.Reason
	}
	return err.Error()
}
