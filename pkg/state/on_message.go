package state

import (
	"fmt"
	"slices"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
)

// RequestOutcome is how an on_message helper's request ended (TaskStatusChangedPayload.RequestOutcome).
type RequestOutcome string

const (
	// RequestOutcomeResult — the turn committed a task_result for the request.
	RequestOutcomeResult RequestOutcome = "result"
	// RequestOutcomeFailed — the turn failed: the request has no result and no verdict.
	RequestOutcomeFailed RequestOutcome = "failed"
)

// IsOnMessageHelper reports whether t is a stage's top-level helper that takes one turn per addressed request.
func IsOnMessageHelper(t assistants.Task) bool {
	return t.Trigger == assistants.TriggerOnMessage && t.Scope == assistants.ScopeStage && t.ParentTaskID == nil
}

// OnMessageIdle reports whether t is an on_message helper with no request to work on: until one arrives it
// starts no turn and holds nothing open.
func OnMessageIdle(t TaskState) bool {
	return IsOnMessageHelper(t.Task) && !t.Status.Terminal() && t.ActiveRequest == "" && len(t.PendingRequests) == 0
}

// queueOnMessageRequests queues recorded msg for each on_message helper it is addressed to, unless it continues
// the active request: answers the helper's own message sent during it, or resolved one of its waits (woken). Only
// while admitting, always on a copy, is a message the helper can no longer take refused; replay never refuses.
func (rs *RunState) queueOnMessageRequests(msg agentcomm.Message, woken map[agentcomm.ParticipantID]bool, admitting bool) error {
	if msg.AutoResult {
		return nil
	}
	as := &rs.Assistants
	for _, r := range msg.Recipients {
		t := as.onMessageHelper(r)
		if t == nil {
			continue
		}
		continues := t.ActiveRequest != "" && (woken[r] || as.Mesh.repliesToMessageOf(msg, r, t.ActiveSinceSeq))
		if admitting {
			if err := rs.checkOnMessageRoom(t, r, msg.ID, continues); err != nil {
				return err
			}
		}
		if !continues {
			t.PendingRequests = append(slices.Clone(t.PendingRequests), msg.ID)
		}
	}
	return nil
}

// onMessageHelper returns the on_message helper participant r stands for, or nil for anyone else.
func (as *AssistantsState) onMessageHelper(r agentcomm.ParticipantID) *TaskState {
	taskID, isHelper := agentcomm.HelperTaskID(r)
	if !isHelper {
		return nil
	}
	if t := as.Tasks[taskID]; t != nil && IsOnMessageHelper(t.Task) {
		return t
	}
	return nil
}

// repliesToMessageOf reports whether msg replies to a message author wrote after afterSeq — not to a passive
// turn result, which only reports what the author did.
func (ms MeshState) repliesToMessageOf(msg agentcomm.Message, author agentcomm.ParticipantID, afterSeq uint64) bool {
	if msg.InReplyTo == nil {
		return false
	}
	orig := ms.Messages[*msg.InReplyTo]
	return orig != nil && orig.Sender == author && !orig.AutoResult && orig.Seq > afterSeq
}

// checkOnMessageRoom refuses message id that helper t (participant r) cannot take. A new request needs a helper
// serving its stage's current attempt and room in its queue, read requests included; a continuation has its own
// room, so a full queue never blocks the answer the helper needs to finish and work the queue off.
func (rs *RunState) checkOnMessageRoom(t *TaskState, r agentcomm.ParticipantID, id agentcomm.MessageID, continues bool) error {
	if continues {
		if rs.Assistants.Mesh.unreadContinuations(t, r, id) >= agentcomm.MaxInboxUnread {
			return meshRejected(assistants.CodeBudgetExceeded, "helper %s already has %d replies waiting to be read (MaxInboxUnread)", r, agentcomm.MaxInboxUnread)
		}
		return nil
	}
	stage := rs.Stages[t.RootStageID].Status
	switch {
	case t.Status.Terminal():
		return meshRejected(assistants.CodeInvalidStatus, "helper %s is %s and takes no new requests", r, t.Status)
	case t.RootGeneration != rs.Assistants.RootGenerations[t.RootStageID]:
		return meshRejected(assistants.CodeInvalidStatus, "helper %s belongs to a superseded attempt of stage %s and takes no new requests", r, t.RootStageID)
	case stage == StatusDone || stage == StatusFailed:
		return meshRejected(assistants.CodeInvalidStatus, "stage %s is %s: its helper %s takes no new requests", t.RootStageID, stage, r)
	case len(t.PendingRequests) >= agentcomm.MaxInboxUnread:
		return meshRejected(assistants.CodeBudgetExceeded, "helper %s already has %d requests waiting (MaxInboxUnread)", r, agentcomm.MaxInboxUnread)
	}
	return nil
}

// CheckNewMessageRoom refuses a new message to r that it would be refused at commit for lack of room: an on_message
// helper takes it as a new request (checkOnMessageRoom), anyone else into its inbox (CheckInboxCapacity). A plan that
// would rather skip such a message than fail the operation checks it first.
func (rs RunState) CheckNewMessageRoom(r agentcomm.ParticipantID) error {
	if t := rs.Assistants.onMessageHelper(r); t != nil {
		return rs.checkOnMessageRoom(t, r, "", false)
	}
	return rs.Assistants.Mesh.CheckInboxCapacity([]agentcomm.ParticipantID{r}, false)
}

// unreadContinuations counts the messages other than id that have not reached helper t (participant r) yet and
// open no request of it.
func (ms MeshState) unreadContinuations(t *TaskState, r agentcomm.ParticipantID, id agentcomm.MessageID) int {
	n := 0
	for _, mid := range ms.Inbox[r] {
		m := ms.Messages[mid]
		if mid != id && m != nil && !m.AutoResult && ms.Receipts[mid][r] == agentcomm.DeliveryAccepted && !t.queuedRequest(mid) {
			n++
		}
	}
	return n
}

// checkRequestProgress refuses a status record that would take t's requests out of order: an on_message
// helper's turn names the request it works on — the active one or, when none is active, the oldest waiting one —
// and an outcome ends the active request.
func checkRequestProgress(t *TaskState, p TaskStatusChangedPayload, status assistants.TaskStatus) error {
	id := agentcomm.MessageID(p.RequestID)
	if p.RequestOutcome != "" {
		if p.RequestOutcome != RequestOutcomeResult && p.RequestOutcome != RequestOutcomeFailed {
			return fmt.Errorf("unknown request_outcome %q", p.RequestOutcome)
		}
		if id == "" || id != t.ActiveRequest {
			return fmt.Errorf("request_outcome for request %q, but the active request is %q", id, t.ActiveRequest)
		}
	}
	if status != assistants.StatusRunning || p.Turn == 0 {
		return nil
	}
	switch {
	case id == "" && IsOnMessageHelper(t.Task):
		return fmt.Errorf("turn %d names no request: an on_message helper's turn works on one", p.Turn)
	case id == "" || id == t.ActiveRequest:
		return nil
	case t.ActiveRequest != "":
		return fmt.Errorf("turn %d takes request %s while request %s is still active", p.Turn, id, t.ActiveRequest)
	case len(t.PendingRequests) == 0 || t.PendingRequests[0] != id:
		return fmt.Errorf("turn %d takes request %s, but the next waiting request is %v", p.Turn, id, t.PendingRequests)
	}
	return nil
}

// startsRequestTurn reports whether a status record starts a turn that names the request it works on.
func startsRequestTurn(p TaskStatusChangedPayload, status assistants.TaskStatus) bool {
	return status == assistants.StatusRunning && p.Turn != 0 && p.RequestID != ""
}

// advanceRequests applies status record p (at seq) to t's request queue: an outcome ends the active request, a turn
// start makes the request it names active unless it already is (after a yield, Pause or crash), and a terminal status
// drops the rest — but failed: a turn fails just its request, so a failed helper keeps its queue for the Retry.
func advanceRequests(t *TaskState, p TaskStatusChangedPayload, status assistants.TaskStatus, seq uint64) {
	id := agentcomm.MessageID(p.RequestID)
	if p.RequestOutcome != "" && id != "" && id == t.ActiveRequest {
		t.LastRequest, t.LastOutcome = id, p.RequestOutcome
		t.ActiveRequest, t.ActiveSinceSeq = "", 0
	}
	if startsRequestTurn(p, status) && id != t.ActiveRequest {
		t.PendingRequests = slices.DeleteFunc(slices.Clone(t.PendingRequests), func(m agentcomm.MessageID) bool { return m == id })
		t.ActiveRequest, t.ActiveSinceSeq = id, seq
	}
	if status.Terminal() && status != assistants.StatusFailed {
		// A request dropped unanswered fails, so the outcome of an earlier one no longer speaks for the helper.
		if dropped := t.newestQueuedRequest(); dropped != "" {
			t.LastRequest, t.LastOutcome = dropped, RequestOutcomeFailed
		}
		t.PendingRequests, t.ActiveRequest, t.ActiveSinceSeq = nil, "", 0
	}
}

// newestQueuedRequest is the latest request t accepted and has not ended: the last waiting one, else the active one.
func (t *TaskState) newestQueuedRequest() agentcomm.MessageID {
	if n := len(t.PendingRequests); n > 0 {
		return t.PendingRequests[n-1]
	}
	return t.ActiveRequest
}

// queuedRequest reports whether id is one of t's on_message requests, which only their outcome answers.
func (t *TaskState) queuedRequest(id agentcomm.MessageID) bool {
	return id == t.ActiveRequest || slices.Contains(t.PendingRequests, id)
}
