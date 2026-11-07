// Package bus defines the values exchanged with the execution engine.
package events

type Event struct {
	Type    EventType `json:"type"`
	StageID string    `json:"stage_id"`
	Data    any       `json:"data,omitempty"`
	Seq     uint64    `json:"seq,omitempty"`
	Epoch   *uint64   `json:"-"`
	ParkSeq uint64    `json:"-"`
}

type EventType string

type StorageError struct{ Inner error }

const EventAgentAction EventType = "agent_action"
const EventAgentCompleted EventType = "agent_completed"
const EventAgentNote EventType = "agent_note"
const EventApproved EventType = "approved"
const EventAskUser EventType = "ask_user"
const EventAssistantActivity EventType = "assistant_activity"
const EventAssistantTaskCreated EventType = "assistant_task_created"
const EventAssistantTaskStatus EventType = "assistant_task_status"
const EventAutoAnswered EventType = "auto_answered"
const EventContextWarning EventType = "context_warning"
const EventDialogAnswer EventType = "dialog_answer"
const EventDialogQuestion EventType = "dialog_question"
const EventDiscussionMessage EventType = "discussion_message"
const EventFeedbackDelivered EventType = "feedback_delivered"
const EventHelperResultRepaired EventType = "helper_result_repaired"
const EventHelperTreeModified EventType = "helper_tree_modified"
const EventHookFailed EventType = "hook_failed"
const EventHookResolved EventType = "hook_resolved"
const EventLifecycleHookFailed EventType = "lifecycle_hook_failed"
const EventMeshBoard EventType = "mesh_board"
const EventMeshMessage EventType = "mesh_message"
const EventMeshParticipant EventType = "mesh_participant"
const EventMeshReceipt EventType = "mesh_receipt"
const EventMeshThread EventType = "mesh_thread"
const EventMeshWait EventType = "mesh_wait"
const EventPeerQuestion EventType = "peer_question"
const EventPeerReplied EventType = "peer_replied"
const EventPeerWaitEnded EventType = "peer_wait_ended"
const EventProposalCreated EventType = "proposal_created"
const EventProposalStatus EventType = "proposal_status"
const EventReflectFailed EventType = "reflect_failed"
const EventRetryExhausted EventType = "retry_exhausted"
const EventRetryScheduled EventType = "retry_scheduled"
const EventScriptFailed EventType = "script_failed"
const EventScriptOutput EventType = "script_output"
const EventSideAgentMeshFailure EventType = "side_agent_mesh_failure"
const EventStageStatusChanged EventType = "stage_status_changed"
const EventTeamWarning EventType = "team_warning"
const EventUserAnswered EventType = "user_answered"
const EventVerifyResult EventType = "verify_result"
const EventVerifyRetry EventType = "verify_retry"
const EventVerifyStarted EventType = "verify_started"
const EventVerifyTreeModified EventType = "verify_tree_modified"

func (e *StorageError) Error() string { return "storage failure: " + e.Inner.Error() }

func (e *StorageError) Unwrap() error { return e.Inner }
