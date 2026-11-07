// Package orchestrator defines the values exchanged with the execution engine.
package control

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	bus "github.com/akopichin/afm/contracts/events"

	stagefiles "github.com/akopichin/afm/contracts/activity"
	"github.com/akopichin/afm/contracts/agentcomm"
	"github.com/akopichin/afm/contracts/assistants"
	"github.com/akopichin/afm/contracts/flow"
	"github.com/akopichin/afm/contracts/graph"
	"github.com/akopichin/afm/contracts/state"
)

type AcknowledgeRequest struct {
	OperationID string
	MessageID   agentcomm.MessageID
	Participant agentcomm.ParticipantID
	Actor       state.Actor
}

type AddRunAgentRequest struct {
	OperationID  string
	ProfileID    assistants.ProfileID
	Title        string
	Instruction  string
	FirstMessage *string
	Recipients   []agentcomm.ParticipantID
	Actor        state.Actor
	Skills       []string
	SkillsDir    string
}

type AddStageDraft struct {
	Kind             string
	Stage            graph.StageDraft
	TargetID         string
	ExplicitDeps     []string
	ExpectedRevision int64
	RequestID        string
}

type AddStagePreview struct {
	Plan          graph.InsertionPlan
	GraphRevision int64
}

type AddStageResult struct {
	Stage         flow.Stage
	GraphRevision int64
}

type AttachAndSendRequest struct {
	OperationID  string
	ProfileID    assistants.ProfileID
	Scope        assistants.Scope
	StageID      string
	Mode         assistants.Mode
	Trigger      assistants.Trigger
	Title        string
	Instruction  string
	FirstMessage *string
	Sender       agentcomm.ParticipantID
	FirstKind    agentcomm.MessageKind
	Origin       assistants.Origin
	Recipients   []agentcomm.ParticipantID
	OriginStage  string
	Actor        state.Actor
	Skills       []string
	SkillsDir    string
}

type AttachAndSendResult struct {
	TaskID        assistants.TaskID
	ParticipantID agentcomm.ParticipantID
	ThreadID      agentcomm.ThreadID
	MessageID     *agentcomm.MessageID
}

type AttachAssistantProposalPayload struct {
	ProfileID   string `json:"profile_id"`
	Title       string `json:"title"`
	Instruction string `json:"instruction"`
	Mode        string `json:"mode,omitempty"`
	Trigger     string `json:"trigger,omitempty"`
}

type AttachRequest struct {
	OperationID string
	StageID     string
	ProfileID   assistants.ProfileID
	Title       string
	Instruction string
	Mode        assistants.Mode
	Trigger     assistants.Trigger
	Actor       state.Actor
	Skills      []string
	SkillsDir   string
}

type BoardPage struct{ Entries []agentcomm.BoardEntry }

type BoardTaskAction string

type BoardTaskReceipt struct {
	Key           string                    `json:"key"`
	Version       int                       `json:"version"`
	Status        agentcomm.BoardTaskStatus `json:"status"`
	AssignmentID  string                    `json:"assignment_id,omitempty"`
	Seq           uint64                    `json:"seq"`
	MessageID     agentcomm.MessageID       `json:"message_id,omitempty"`
	ThreadID      agentcomm.ThreadID        `json:"thread_id,omitempty"`
	NoticeSkipped string                    `json:"notice_skipped,omitempty"`
}

type BoardTaskRequest struct {
	OperationID     string
	Action          BoardTaskAction
	Actor           agentcomm.ParticipantID
	Key             string
	ExpectedVersion int
	AssignmentID    string
	To              string
	Title           *string
	Description     *string
	CoordinatorID   *string
	Text            string
	ArtifactRefs    *[]string
}

type CancelWaitRequest struct {
	OperationID string
	WaitID      agentcomm.WaitID
	BatchID     string
	WaitIDs     []agentcomm.WaitID
	StageID     string
	Reason      string
	Actor       state.Actor
}

type Classification int

type CloseThreadRequest struct {
	OperationID string
	ThreadID    agentcomm.ThreadID
	Actor       state.Actor
}

type CreateLoopRequest struct {
	OperationID  string
	Target       agentcomm.ParticipantID
	Interval     time.Duration
	Prompt       string
	UntilStageID string
	OriginStage  string
	Actor        state.Actor
}

type CreateProposalRequest struct {
	OperationID             string
	StageID                 string
	TaskID                  string
	Type                    assistants.ProposalType
	Title                   string
	Rationale               string
	Payload                 json.RawMessage
	Authors                 []assistants.MessageAuthor
	SourceMessageIDs        []string
	SourceFindingIDs        []string
	ExpectedStageGeneration uint64
	Actor                   state.Actor
}

type FeedbackProposalPayload struct {
	Feedback string `json:"feedback"`
}

type HelperRequestState string

type HelperRequestView struct {
	ID     agentcomm.MessageID
	State  HelperRequestState
	Turn   uint64
	Reason string
}

type HelperResultView struct {
	TaskID       assistants.TaskID
	Title        string
	Status       assistants.TaskStatus
	StatusReason string
	Generation   uint64
	Request      *HelperRequestView
	Result       json.RawMessage
}

type IncompleteWorkError = stagefiles.IncompleteWorkError

type ListLoopsRequest struct{ Actor state.Actor }

type ListParticipantsRequest struct{ Actor state.Actor }

type ListThreadsRequest struct {
	Participant agentcomm.ParticipantID
	Actor       state.Actor
}

type LoopPage struct {
	Loops []LoopView `json:"loops"`
}

type LoopStatus string

type LoopView struct {
	ID          string                  `json:"id"`
	CreatorID   agentcomm.ParticipantID `json:"creator_id"`
	TargetID    agentcomm.ParticipantID `json:"target_id"`
	Every       string                  `json:"every"`
	OriginStage string                  `json:"origin_stage,omitempty"`
	UntilStage  string                  `json:"until_stage,omitempty"`
	Status      LoopStatus              `json:"status"`
	NextTickAt  *time.Time              `json:"next_tick_at,omitempty"`
	EndReason   string                  `json:"end_reason,omitempty"`
}

type MessagePage struct {
	Messages   []agentcomm.Message
	NextCursor uint64
}

type MissingArtifactError = stagefiles.MissingArtifactError

type MissingSectionsError struct{ Missing []string }

type OpenThreadRequest struct {
	OperationID string
	Title       string
	Scope       agentcomm.Scope
	Members     []agentcomm.ParticipantID
	MaxMessages int
	OriginStage string
	Actor       state.Actor
}

type ParticipantPage struct{ Participants []agentcomm.Participant }

type PostMessageRequest struct {
	OperationID     string
	StageID         string
	AuthorKind      assistants.MessageAuthorKind
	AuthorID        string
	Role            string
	Round           int
	ParentMessageID string
	ContextVersion  string
	Evidence        []string
	Body            string
	Actor           state.Actor
	Mentions        []agentcomm.ParticipantID
	Interrupt       bool
}

type Prompts struct {
	Planning       string
	Implementation string
	Review         string
	Summary        string
	Autonomous     string
	Reflect        string
	Aggregate      string
	Prioritize     string
	Update         string
	Verify         string
	HelperResult   string
	HelperDelegate string
}

type PublishRequest struct {
	OperationID     string
	Author          agentcomm.ParticipantID
	Key             string
	Status          string
	ArtifactRefs    []string
	ExpectedVersion int
	Notify          []agentcomm.ParticipantID
	Actor           state.Actor
}

type ReadBoardRequest struct {
	Key   string
	Actor state.Actor
}

type ReadInboxRequest struct {
	Participant agentcomm.ParticipantID
	Cursor      uint64
	Limit       int
	OnlyNew     bool
	Actor       state.Actor
}

type ReadThreadRequest struct {
	ThreadID agentcomm.ThreadID
	Reader   agentcomm.ParticipantID
	Cursor   uint64
	Limit    int
	Actor    state.Actor
}

type RegisterSideAgentRequest struct {
	Command string
	Actor   state.Actor
}

type ResolvedFile struct {
	DisplayPath string
	Reference   string
	ContentSHA  string
	LineText    string
	InRange     bool
}

type SendReceipt struct {
	MessageID agentcomm.MessageID
	Seq       uint64
	ThreadID  agentcomm.ThreadID
}

type SendRequest struct {
	OperationID         string
	ThreadID            agentcomm.ThreadID
	Sender              agentcomm.ParticipantID
	Recipients          []agentcomm.ParticipantID
	RequestedRecipients []agentcomm.ParticipantID
	Kind                agentcomm.MessageKind
	InReplyTo           *agentcomm.MessageID
	Body                string
	ArtifactRefs        []string
	AutoResult          bool
	OriginStage         string
	Actor               state.Actor
}

type SpawnHelperRequest struct {
	OperationID string
	ProfileID   assistants.ProfileID
	Title       string
	Instruction string
	Trigger     assistants.Trigger
	Actor       state.Actor
}

type StageFailedError struct {
	StageID string
	Reason  string
	Seq     uint64
}

type StageMessagePage struct {
	Messages   []agentcomm.Message
	Threads    []agentcomm.Thread
	NextCursor uint64
	HasMore    bool
}

type StageWaitView struct {
	WaitID    agentcomm.WaitID        `json:"wait_id"`
	BatchID   string                  `json:"batch_id,omitempty"`
	Condition agentcomm.WaitCondition `json:"condition"`
	Status    agentcomm.WaitStatus    `json:"status"`
	Reason    string                  `json:"reason,omitempty"`
	Deadline  *time.Time              `json:"deadline,omitempty"`
}

type StopLoopRequest struct {
	OperationID string
	LoopID      string
	Actor       state.Actor
}

type StorageError = bus.StorageError

type TaskRequestQueue struct {
	Active      agentcomm.MessageID
	Pending     int
	Last        agentcomm.MessageID
	LastOutcome state.RequestOutcome
	Turn        uint64
}

type ThreadPage struct {
	Threads   []agentcomm.Thread                   `json:"threads"`
	Summaries map[agentcomm.ThreadID]ThreadSummary `json:"summaries"`
}

type ThreadSummary struct {
	LastMessage               *ThreadSummaryMessage `json:"last_message,omitempty"`
	LastOtherMessageSeq       uint64                `json:"last_other_message_seq"`
	LastAgentToUserMessageSeq uint64                `json:"last_agent_to_user_message_seq"`
	TitleSeq                  uint64                `json:"title_seq"`
}

type ThreadSummaryMessage struct {
	ID        agentcomm.MessageID     `json:"id"`
	Seq       uint64                  `json:"seq"`
	CreatedAt time.Time               `json:"created_at"`
	SenderID  agentcomm.ParticipantID `json:"sender_id"`
	Preview   string                  `json:"preview"`
}

type UpdateThreadTitleReceipt struct {
	Title    string `json:"title"`
	TitleSeq uint64 `json:"title_seq"`
}

type UpdateThreadTitleRequest struct {
	OperationID      string
	ThreadID         agentcomm.ThreadID
	Title            string
	ExpectedTitleSeq uint64
	Actor            state.Actor
}

type VerifyExecError struct {
	Reason    string
	Step      int
	Transient bool
}

type VerifyRejectedError struct {
	*stagefiles.IncompleteWorkError
	ReportID string
	Step     int
}

type WaitItemReceipt struct {
	MessageID agentcomm.MessageID
	WaitID    agentcomm.WaitID
	Status    agentcomm.WaitStatus
	Reason    string
}

type WaitReceipt struct {
	WaitID         agentcomm.WaitID
	Status         agentcomm.WaitStatus
	BatchID        string
	Items          []WaitItemReceipt
	AlreadyPending bool
}

type WaitRequest struct {
	OperationID      string
	Waiter           agentcomm.ParticipantID
	Condition        agentcomm.WaitCondition
	MessageIDs       []agentcomm.MessageID
	Deadline         *time.Time
	ContinuationNote string
	Actor            state.Actor
	Invocation       string `json:"-"`
}

const AddStageKindAppend = "append"
const AddStageKindInsertBefore = "insert_before"
const BoardTaskAssign BoardTaskAction = "assign"
const BoardTaskClaim BoardTaskAction = "claim"
const BoardTaskCreate BoardTaskAction = "create"
const BoardTaskDone BoardTaskAction = "done"
const BoardTaskEdit BoardTaskAction = "edit"
const BoardTaskProgress BoardTaskAction = "progress"
const BoardTaskRelease BoardTaskAction = "release"
const BoardTaskReopen BoardTaskAction = "reopen"
const BoardTaskStart BoardTaskAction = "start"
const ClassFatal Classification = 5
const ClassIncomplete Classification = 2
const ClassMissingArtifact Classification = 3
const ClassMissingSections Classification = 4
const ClassNone Classification = 0
const ClassRetryable Classification = 1
const ClassRetryableUnknown Classification = 8
const ClassStorageFatal Classification = 6
const ClassUsageLimit Classification = 9
const ClassVerifyFailed Classification = 7
const HelperRequestActive HelperRequestState = "active"
const HelperRequestFailed HelperRequestState = "failed"
const HelperRequestPending HelperRequestState = "pending"
const HelperRequestResult HelperRequestState = "result"
const LoopActive LoopStatus = "active"
const LoopEnded LoopStatus = "ended"
const LoopStopped LoopStatus = "stopped"
const MaxBatchWaits = 16

func (e *MissingSectionsError) Error() string {
	return "plan missing sections: " + strings.Join(e.Missing, ", ")
}

func (e *VerifyRejectedError) Error() string {
	return fmt.Sprintf("verify rejected (step %d, report %s): %s", e.Step, e.ReportID, e.IncompleteWorkError.Error())
}

// Unwrap делает встроенный *IncompleteWorkError видимым для errors.As — без этого метода embedding НЕ даёт
// автоматической цепочки Unwrap, и Classify/IsIncompleteWorkError не нашли бы его.
func (e *VerifyRejectedError) Unwrap() error { return e.IncompleteWorkError }

func (e *VerifyExecError) Error() string {
	if e.Step > 0 {
		return fmt.Sprintf("verify execution failed at step %d: %s", e.Step, e.Reason)
	}
	return "verify execution failed: " + e.Reason
}

func (e *StageFailedError) Error() string {
	return fmt.Sprintf("stage %s failed: %s", e.StageID, e.Reason)
}
