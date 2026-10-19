package orchestrator

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"unicode/utf8"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/state"
)

// A run agent's accepted turn result is posted as a reply to the message that woke the turn, so a colleague who
// asked gets an answer even from an agent that never writes on its own. The reply is PASSIVE
// (agentcomm.Message.AutoResult): it waits in the recipient's inbox for its next turn but never wakes it — two
// agents answering each other's turn results would otherwise keep each other busy forever. It is committed by
// the turn's own finish operation (finishTaskTurnReplying), and never keeps that turn from finishing.

// errTurnAlreadyAnswered rejects a turn reply whose agent answered the waking message itself during the turn.
var errTurnAlreadyAnswered = errors.New("the agent already replied to the message that woke its turn")

// turnReplyMessageID is the id of the reply that ends taskID's turn — derived from turn-reply:<task>/<turn>, so
// a turn can only ever post one.
func turnReplyMessageID(taskID assistants.TaskID, turn uint64) agentcomm.MessageID {
	return deriveMeshMessageID(fmt.Sprintf("turn-reply:%s/%d", taskID, turn))
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

// planTurnReply plans the reply that ends taskID's turn with result: to the message that woke the turn, back to
// its sender, in its thread — through planMessage, so a reply to a Discussion post is mirrored into the
// Discussion too. nil: the turn had no waking input, or the agent already answered it itself.
func (o *Orchestrator) planTurnReply(taskID assistants.TaskID, turn uint64, result assistants.Result) *plannedMessage {
	rs := o.opts.Store.Snapshot()
	t, ok := rs.Assistants.Tasks[taskID]
	if !ok || turn == 0 {
		return nil
	}
	waking, ok := wakingMessage(rs.Assistants.Mesh, t.TurnInbox)
	if !ok {
		return nil
	}
	agent := meshParticipantForTask(rs, t.Task)
	if answeredBy(rs.Assistants.Mesh, agent, waking.ID) {
		return nil
	}
	inReplyTo := waking.ID
	msg, err := o.planMessage(rs, SendRequest{
		ThreadID: waking.ThreadID, Sender: agent, Recipients: []agentcomm.ParticipantID{waking.Sender},
		Kind: agentcomm.MessageKindReply, InReplyTo: &inReplyTo, Body: turnReplyBody(taskID, turn, result), AutoResult: true,
	}, turnReplyMessageID(taskID, turn), nil)
	if err != nil {
		log.Printf("WARN: assistants: plan the turn reply of task %s: %v", taskID, err)
		return nil
	}
	return &msg
}

// turnReplyCheck is the commit-time check of a finish operation carrying reply: the agent has not answered the
// waking message itself meanwhile (errTurnAlreadyAnswered — the same turn's explicit reply may commit after
// planTurnReply looked), and the recipient can still receive a message.
func (o *Orchestrator) turnReplyCheck(reply plannedMessage) func(*state.RunState) error {
	reachable := o.recipientsReachable([]plannedMessage{reply})
	return func(rs *state.RunState) error {
		if answeredBy(rs.Assistants.Mesh, reply.send.Sender, *reply.send.InReplyTo) {
			return &state.OperationRejectedError{Code: assistants.CodeInvalidStatus, Reason: errTurnAlreadyAnswered.Error(), Cause: errTurnAlreadyAnswered}
		}
		return reachable(rs)
	}
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
	return truncateText(summary, agentcomm.MaxBodyBytes-len(footer)) + footer
}

// truncateText cuts s to at most maxBytes bytes on a character boundary, marking the cut with an ellipsis.
func truncateText(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	const ellipsis = "…"
	cut := maxBytes - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + ellipsis
}

// noticeResultNotDelivered tells the feed that taskID's result was committed without the message that should
// have carried it, and why.
func (o *Orchestrator) noticeResultNotDelivered(taskID assistants.TaskID, stageID string, cause error) {
	reason := cause.Error()
	var rejected *state.OperationRejectedError
	if errors.As(cause, &rejected) {
		reason = rejected.Reason
	}
	o.publishNotice(bus.Event{Type: bus.EventContextWarning, StageID: stageID, Data: fmt.Sprintf(
		"turn result of task %s not delivered: %s", taskID, o.RedactSecrets(reason))})
}
