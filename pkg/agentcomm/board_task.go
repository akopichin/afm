package agentcomm

import (
	"strings"
	"time"
)

// UserParticipantID is the fixed participant id of the human user.
const UserParticipantID ParticipantID = "user"

// BoardTaskProtocol marks how a board task is managed.
type BoardTaskProtocol string

// BoardTaskProtocolTypedV1 marks a typed task with explicit assignments and lifecycle.
// The empty protocol is a legacy free-form task.
const BoardTaskProtocolTypedV1 BoardTaskProtocol = "typed_v1"

// AssignmentState is the lifecycle state of one active assignment.
type AssignmentState string

const (
	AssignmentAssigned   AssignmentState = "assigned"
	AssignmentInProgress AssignmentState = "in_progress"
	AssignmentDone       AssignmentState = "done"
)

// AssignmentEnd tells why an assignment left the active list; set only in History.
type AssignmentEnd string

const (
	AssignmentReleased AssignmentEnd = "released"
	AssignmentReopened AssignmentEnd = "reopened"
)

// BoardTaskStatus is the aggregate status derived from active assignments.
type BoardTaskStatus string

const (
	BoardTaskTodo       BoardTaskStatus = "todo"
	BoardTaskInProgress BoardTaskStatus = "in_progress"
	BoardTaskDone       BoardTaskStatus = "done"
)

// BoardTaskAssignment is one participant's share of a board task.
type BoardTaskAssignment struct {
	ID            string          `json:"id"`
	ParticipantID ParticipantID   `json:"participant_id"`
	State         AssignmentState `json:"state"`
	AssignedBy    ParticipantID   `json:"assigned_by"`
	Progress      string          `json:"progress,omitempty"`
	Result        string          `json:"result,omitempty"`
	ArtifactRefs  []string        `json:"artifact_refs,omitempty"`
	AssignedSeq   uint64          `json:"assigned_seq"`
	StartedSeq    uint64          `json:"started_seq,omitempty"`
	CompletedSeq  uint64          `json:"completed_seq,omitempty"`
	Ended         AssignmentEnd   `json:"ended,omitempty"` // set only in History
	EndedBy       ParticipantID   `json:"ended_by,omitempty"`
	EndReason     string          `json:"end_reason,omitempty"`
	EndedSeq      uint64          `json:"ended_seq,omitempty"`
}

// BoardTask is a typed board task with explicit assignments.
type BoardTask struct {
	Key           string                `json:"key"`
	AuthorID      ParticipantID         `json:"author_id"`
	CoordinatorID ParticipantID         `json:"coordinator_id,omitempty"`
	Title         string                `json:"title"`
	Description   string                `json:"description,omitempty"`
	Version       int                   `json:"version"`
	Generation    int                   `json:"generation"`
	Assignments   []BoardTaskAssignment `json:"assignments"`       // active only
	History       []BoardTaskAssignment `json:"history,omitempty"` // released/archived, oldest first
	CreatedSeq    uint64                `json:"created_seq"`
	UpdatedSeq    uint64                `json:"updated_seq"`
	CreatedAt     time.Time             `json:"created_at"`
	UpdatedAt     time.Time             `json:"updated_at"`
}

// Status aggregates active assignments: todo when none has started, done when all are done,
// in_progress otherwise. History is ignored.
func (t BoardTask) Status() BoardTaskStatus {
	if len(t.Assignments) == 0 {
		return BoardTaskTodo
	}
	allDone, anyStarted := true, false
	for _, a := range t.Assignments {
		if a.State != AssignmentDone {
			allDone = false
		}
		if a.State != AssignmentAssigned {
			anyStarted = true
		}
	}
	switch {
	case allDone:
		return BoardTaskDone
	case anyStarted:
		return BoardTaskInProgress
	default:
		return BoardTaskTodo
	}
}

// Coordinator returns CoordinatorID, else the author unless the author is the user (then "").
func (t BoardTask) Coordinator() ParticipantID {
	if t.CoordinatorID != "" {
		return t.CoordinatorID
	}
	if t.AuthorID == UserParticipantID {
		return ""
	}
	return t.AuthorID
}

// Assignment finds an active assignment by id.
func (t BoardTask) Assignment(id string) (BoardTaskAssignment, bool) {
	return findAssignment(t.Assignments, func(a BoardTaskAssignment) bool { return a.ID == id })
}

// EndedAssignment finds a history assignment by id.
func (t BoardTask) EndedAssignment(id string) (BoardTaskAssignment, bool) {
	return findAssignment(t.History, func(a BoardTaskAssignment) bool { return a.ID == id })
}

// ActiveFor finds the active assignment of participant p.
func (t BoardTask) ActiveFor(p ParticipantID) (BoardTaskAssignment, bool) {
	return findAssignment(t.Assignments, func(a BoardTaskAssignment) bool { return a.ParticipantID == p })
}

func findAssignment(list []BoardTaskAssignment, match func(BoardTaskAssignment) bool) (BoardTaskAssignment, bool) {
	for _, a := range list {
		if match(a) {
			return a, true
		}
	}
	return BoardTaskAssignment{}, false
}

// BoardTaskView is a BoardTask with its computed status, for API responses.
type BoardTaskView struct {
	BoardTask
	Status BoardTaskStatus `json:"status"`
}

// ViewOf builds the API view of a task.
func ViewOf(t BoardTask) BoardTaskView {
	return BoardTaskView{BoardTask: t, Status: t.Status()}
}

// BoardTaskKeyPrefix starts the key of every typed board task. In a run with the typed protocol the board
// keys under it belong to typed tasks only.
const BoardTaskKeyPrefix = "task/"

const maxBoardTaskKeyLen = 200

// ValidBoardTaskKey reports whether key is "task/<slug>" with a non-empty slug without
// further slashes, at most 200 bytes long.
func ValidBoardTaskKey(key string) bool {
	slug, ok := strings.CutPrefix(key, BoardTaskKeyPrefix)
	return ok && slug != "" && !strings.Contains(slug, "/") && len(key) <= maxBoardTaskKeyLen
}
