// Package state defines the values exchanged with the execution engine.
package state

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/akopichin/afm/contracts/agentcomm"
	"github.com/akopichin/afm/contracts/assistants"
	"github.com/akopichin/afm/contracts/flow"
)

type Actor struct {
	Kind string `json:"kind,omitempty"`
	ID   string `json:"id,omitempty"`
}

type AssistantsState struct {
	Tasks           map[assistants.TaskID]*TaskState          `json:"tasks,omitempty"`
	ByRootStage     map[string][]assistants.TaskID            `json:"by_root_stage,omitempty"`
	ByParent        map[assistants.TaskID][]assistants.TaskID `json:"by_parent,omitempty"`
	OperationIndex  map[string]uint64                         `json:"operation_index,omitempty"`
	Policy          PolicyState                               `json:"policy,omitempty"`
	QuotaReserved   int                                       `json:"quota_reserved,omitempty"`
	RootGenerations map[string]uint64                         `json:"root_generations,omitempty"`
	Discussions     DiscussionState                           `json:"discussions,omitempty"`
	Proposals       ProposalBoard                             `json:"proposals,omitempty"`
	FeedbackIntents map[string]*FeedbackIntent                `json:"feedback_intents,omitempty"`
	Mesh            MeshState                                 `json:"mesh,omitempty"`
	Loops           map[string]*LoopState                     `json:"loops,omitempty"`
	LoopOrder       []string                                  `json:"loop_order,omitempty"`
	Admitting       bool                                      `json:"-"`
}

type DiscussionState struct {
	Messages map[string]*assistants.DiscussionMessage `json:"messages,omitempty"`
	ByStage  map[string][]string                      `json:"by_stage,omitempty"`
}

type DynamicStageAdded struct {
	OperationID string              `json:"operation_id"`
	Seq         uint64              `json:"seq"`
	Stage       flow.Stage          `json:"stage"`
	ExtraDeps   map[string][]string `json:"extra_deps,omitempty"`
}

type FeedbackIntent struct {
	ID                      string               `json:"id"`
	StageID                 string               `json:"stage_id"`
	Feedback                string               `json:"feedback"`
	Origin                  FeedbackIntentOrigin `json:"origin"`
	ProposalID              string               `json:"proposal_id,omitempty"`
	Participants            []string             `json:"participants,omitempty"`
	ExpectedStageGeneration uint64               `json:"expected_stage_generation"`
	Status                  FeedbackIntentStatus `json:"status"`
	Reason                  string               `json:"reason,omitempty"`
	CreatedAt               time.Time            `json:"created_at"`
	ConsumedAt              time.Time            `json:"consumed_at,omitempty"`
}

type FeedbackIntentOrigin string

type FeedbackIntentStatus string

type Hold struct {
	Reason string `json:"reason,omitempty"`
}

type LoopState struct {
	Key           string                  `json:"key"`
	ShortID       string                  `json:"short_id"`
	CreatorID     agentcomm.ParticipantID `json:"creator_id"`
	TargetID      agentcomm.ParticipantID `json:"target_id"`
	Interval      time.Duration           `json:"interval"`
	Prompt        string                  `json:"prompt"`
	UntilStageID  string                  `json:"until_stage_id,omitempty"`
	OriginStageID string                  `json:"origin_stage_id,omitempty"`
	ThreadID      agentcomm.ThreadID      `json:"thread_id"`
	CreatedAt     time.Time               `json:"created_at"`
	CreatedSeq    uint64                  `json:"created_seq"`
	LastSlot      uint64                  `json:"last_slot,omitempty"`
	StoppedBy     Actor                   `json:"stopped_by,omitempty"`
	StoppedAt     time.Time               `json:"stopped_at,omitempty"`
	StoppedSeq    uint64                  `json:"stopped_seq,omitempty"`
}

type MeshBoardEntry struct{ agentcomm.BoardEntry }

type MeshState struct {
	Participants      map[agentcomm.ParticipantID]*agentcomm.Participant                           `json:"participants,omitempty"`
	ParticipantOrder  []agentcomm.ParticipantID                                                    `json:"participant_order,omitempty"`
	Threads           map[agentcomm.ThreadID]*agentcomm.Thread                                     `json:"threads,omitempty"`
	ThreadOrder       []agentcomm.ThreadID                                                         `json:"thread_order,omitempty"`
	ThreadChangedSeq  map[agentcomm.ThreadID]uint64                                                `json:"thread_changed_seq,omitempty"`
	ThreadTitles      map[agentcomm.ThreadID]MeshThreadTitle                                       `json:"thread_titles,omitempty"`
	Messages          map[agentcomm.MessageID]*agentcomm.Message                                   `json:"messages,omitempty"`
	MessagesByThread  map[agentcomm.ThreadID][]agentcomm.MessageID                                 `json:"messages_by_thread,omitempty"`
	Inbox             map[agentcomm.ParticipantID][]agentcomm.MessageID                            `json:"inbox,omitempty"`
	Receipts          map[agentcomm.MessageID]map[agentcomm.ParticipantID]agentcomm.DeliveryStatus `json:"receipts,omitempty"`
	Waits             map[agentcomm.WaitID]*MeshWait                                               `json:"waits,omitempty"`
	WaitOrder         []agentcomm.WaitID                                                           `json:"wait_order,omitempty"`
	Board             map[string]*MeshBoardEntry                                                   `json:"board,omitempty"`
	BoardTasks        map[string]*agentcomm.BoardTask                                              `json:"board_tasks,omitempty"`
	BoardTaskProtocol agentcomm.BoardTaskProtocol                                                  `json:"board_task_protocol,omitempty"`
}

type MeshThreadTitle struct {
	Title string `json:"title"`
	Seq   uint64 `json:"seq"`
}

type MeshWait struct {
	agentcomm.Wait
	TargetTaskID       string `json:"target_task_id,omitempty"`
	TargetStageID      string `json:"target_stage_id,omitempty"`
	Generation         uint64 `json:"generation,omitempty"`
	FreshResultOnly    bool   `json:"fresh_result_only,omitempty"`
	ResultBaseline     uint64 `json:"result_baseline,omitempty"`
	RecipientReplyOnly bool   `json:"recipient_reply_only,omitempty"`
	BatchID            string `json:"batch_id,omitempty"`
	RegisteredSeq      uint64 `json:"registered_seq,omitempty"`
	Yielded            bool   `json:"yielded,omitempty"`
}

type OperationRejectedError struct {
	Code   assistants.ErrorCode
	Reason string
	Cause  error
}

type PolicyState struct {
	MaxDepth int    `json:"max_depth"`
	Source   string `json:"source"`
	Revision string `json:"revision"`
}

type ProposalBoard struct {
	Proposals map[string]*assistants.Proposal `json:"proposals,omitempty"`
	ByStage   map[string][]string             `json:"by_stage,omitempty"`
}

type QuotaWait struct {
	Provider         string    `json:"provider,omitempty"`
	ResetAt          time.Time `json:"reset_at,omitzero"`
	Until            time.Time `json:"until,omitzero"`
	Window           string    `json:"window,omitempty"`
	Source           string    `json:"source,omitempty"`
	PauseReason      string    `json:"pause_reason,omitempty"`
	Target           string    `json:"target,omitempty"`
	VerifyID         string    `json:"verify_id,omitempty"`
	VerifyStep       int       `json:"verify_step,omitempty"`
	VerifyPhase      string    `json:"verify_phase,omitempty"`
	VerifyFailures   int       `json:"verify_failures,omitempty"`
	VerifyStalemates int       `json:"verify_stalemates,omitempty"`
	VerifyResume     bool      `json:"verify_resume,omitempty"`
	Waits            int       `json:"waits,omitempty"`
	ProgressMark     uint64    `json:"progress_mark,omitempty"`
	Feedback         bool      `json:"feedback,omitempty"`
	ManualProbe      bool      `json:"manual_probe,omitempty"`
}

type RequestOutcome string

type ReviewNote struct {
	ID           string    `json:"id"`
	Root         string    `json:"root"`
	Path         string    `json:"path"`
	DisplayPath  string    `json:"display_path"`
	Reference    string    `json:"reference"`
	Line         *int      `json:"line"`
	OrigLineText *string   `json:"orig_line_text"`
	ContentSHA   string    `json:"content_sha"`
	Text         string    `json:"text"`
	CreatedAt    time.Time `json:"created_at"`
}

type ReviewNotes struct {
	Version int          `json:"version"`
	Rev     int          `json:"rev"`
	NextID  int          `json:"next_id"`
	Notes   []ReviewNote `json:"notes"`
}

type RunState struct {
	FlowName             string                `json:"flow_name"`
	StartedAt            time.Time             `json:"started_at"`
	RunStatus            RunStatus             `json:"run_status,omitempty"`
	EndedAt              *time.Time            `json:"ended_at,omitempty"`
	RunStartedAt         time.Time             `json:"run_started_at,omitempty"`
	ElapsedAccumulatedMs int64                 `json:"elapsed_accumulated_ms"`
	StageOrder           []string              `json:"stage_order"`
	StageNames           map[string]string     `json:"stage_names,omitempty"`
	Stages               map[string]StageState `json:"stages"`
	LastSeq              uint64                `json:"last_seq"`
	IdleAccumulatedMs    int64                 `json:"idle_accumulated_ms"`
	BackoffAccumulatedMs int64                 `json:"backoff_accumulated_ms"`
	Assistants           AssistantsState       `json:"assistants,omitempty"`
	DynamicStages        []DynamicStageAdded   `json:"dynamic_stages,omitempty"`
	LastRecordAt         time.Time             `json:"-"`
}

type RunStatus string

type StageState struct {
	Status        StageStatus         `json:"status"`
	UpdatedAt     time.Time           `json:"updated_at"`
	Timing        StageTiming         `json:"timing,omitzero"`
	PausedFrom    StageStatus         `json:"paused_from,omitempty"`
	Epoch         uint64              `json:"epoch,omitempty"`
	PeerWaits     []agentcomm.WaitID  `json:"peer_waits,omitempty"`
	PeerContinued bool                `json:"peer_continued,omitempty"`
	Seq           uint64              `json:"seq,omitempty"`
	Quota         *QuotaWait          `json:"quota,omitempty"`
	Summary       *SummaryCorrections `json:"summary,omitempty"`
}

type StageStatus string

type StageTiming struct {
	Started      bool      `json:"started,omitempty"`
	Incomplete   bool      `json:"incomplete,omitempty"`
	WorkMs       int64     `json:"work_ms,omitempty"`
	ApprovalMs   int64     `json:"approval_ms,omitempty"`
	HelpersMs    int64     `json:"helpers_ms,omitempty"`
	BackoffMs    int64     `json:"backoff_ms,omitempty"`
	SchedulingMs int64     `json:"scheduling_ms,omitempty"`
	PausedMs     int64     `json:"paused_ms,omitempty"`
	OpenKind     TimeKind  `json:"open_kind,omitempty"`
	OpenSince    time.Time `json:"open_since,omitzero"`
}

type StorageError struct{ Inner error }

type SummaryCorrections struct {
	PostExit    int    `json:"post_exit,omitempty"`
	Hook        int    `json:"hook,omitempty"`
	Reason      string `json:"reason,omitempty"`
	ReservedSeq uint64 `json:"reserved_seq,omitempty"`
	PostExitSeq uint64 `json:"post_exit_seq,omitempty"`
	Issued      bool   `json:"issued,omitempty"`
}

type SummaryStep struct {
	Source string `json:"source"`
	Reason string `json:"reason,omitempty"`
}

type TaskEvent struct {
	Seq     uint64          `json:"seq"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type TaskState struct {
	assistants.Task
	Holds                   map[string]Hold                `json:"holds,omitempty"`
	HoldEpoch               uint64                         `json:"hold_epoch,omitempty"`
	Continuation            *assistants.Continuation       `json:"continuation,omitempty"`
	LastVerdict             assistants.Verdict             `json:"last_verdict,omitempty"`
	ResultSeq               uint64                         `json:"result_seq,omitempty"`
	Turn                    uint64                         `json:"turn,omitempty"`
	TurnInbox               []agentcomm.MessageID          `json:"turn_inbox,omitempty"`
	OpenRequests            []agentcomm.MessageID          `json:"open_requests,omitempty"`
	PendingRequests         []agentcomm.MessageID          `json:"pending_requests,omitempty"`
	ActiveRequest           agentcomm.MessageID            `json:"active_request,omitempty"`
	ActiveSinceSeq          uint64                         `json:"active_since_seq,omitempty"`
	LastRequest             agentcomm.MessageID            `json:"last_request,omitempty"`
	LastOutcome             RequestOutcome                 `json:"last_outcome,omitempty"`
	TurnReadonlyEnforcement assistants.ReadonlyEnforcement `json:"turn_readonly_enforcement,omitempty"`
	Quota                   *QuotaWait                     `json:"quota,omitempty"`
	Waived                  bool                           `json:"waived,omitempty"`
	WaiverReason            string                         `json:"waiver_reason,omitempty"`
	WaivedBy                Actor                          `json:"waived_by,omitempty"`
	WaivedAt                time.Time                      `json:"waived_at,omitempty"`
}

type TimeKind string

type Transition struct {
	Seq            uint64                  `json:"seq"`
	Time           time.Time               `json:"time"`
	StageID        string                  `json:"stage_id"`
	From           StageStatus             `json:"from"`
	To             StageStatus             `json:"to"`
	Event          string                  `json:"event"`
	Reason         string                  `json:"reason,omitempty"`
	PeerWaits      []agentcomm.WaitID      `json:"peer_waits,omitempty"`
	Quota          *QuotaWait              `json:"quota,omitempty"`
	SummaryStep    *SummaryStep            `json:"summary_step,omitempty"`
	ExpectEpoch    *uint64                 `json:"-"`
	ExpectStageSeq *uint64                 `json:"-"`
	Fence          func(rs *RunState) bool `json:"-"`
}

const ActorKindUser = "user"
const ControlSchemaVersion = 1
const FeedbackIntentAccepted FeedbackIntentStatus = "accepted"
const FeedbackIntentDelivered FeedbackIntentStatus = "delivered"
const FeedbackIntentRejected FeedbackIntentStatus = "rejected"
const FeedbackOriginProposal FeedbackIntentOrigin = "assistant_proposal"
const FeedbackOriginUser FeedbackIntentOrigin = "user"
const PauseModeCancel = "cancel"
const PauseModeInject = "inject"
const PauseStatePaused = "paused"
const PauseStateResuming = "resuming"
const QuotaPauseExceedsMaxWait = "exceeds_max_wait"
const QuotaPauseMaxWaits = "max_waits"
const QuotaPauseNoReset = "no_reset"
const QuotaSourceProbe = "probe"
const QuotaSourceProviderHold = "provider_hold"
const QuotaSourceStructured = "structured"
const QuotaSourceText = "text"
const QuotaTargetHelper = "helper"
const QuotaTargetVerify = "verify"
const RequestOutcomeFailed RequestOutcome = "failed"
const RequestOutcomeResult RequestOutcome = "result"
const RunStatusFailed RunStatus = "failed"
const RunStatusFinished RunStatus = "finished"
const RunStatusInterrupted RunStatus = "interrupted"
const RunStatusRunning RunStatus = "running"
const StatusAwaitingApproval StageStatus = "awaiting_approval"
const StatusAwaitingAssistants StageStatus = "awaiting_assistants"
const StatusAwaitingPeer StageStatus = "awaiting_peer"
const StatusAwaitingQuota StageStatus = "awaiting_quota"
const StatusAwaitingUserInput StageStatus = "awaiting_user_input"
const StatusDone StageStatus = "done"
const StatusFailed StageStatus = "failed"
const StatusHookFailed StageStatus = "hook_failed"
const StatusPaused StageStatus = "paused"
const StatusPending StageStatus = "pending"
const StatusPlanning StageStatus = "planning"
const StatusReady StageStatus = "ready"
const StatusRetrying StageStatus = "retrying"
const StatusRevising StageStatus = "revising"
const StatusRunning StageStatus = "running"
const SummarySourcePostExit = "post_exit"
const SummarySourcePostExitIssued = "post_exit_issued"
const SummarySourceStopHook = "stop_hook"
const TimeApproval TimeKind = "approval"
const TimeBackoff TimeKind = "backoff"
const TimeHelpers TimeKind = "helpers"
const TimeNone TimeKind = ""
const TimePaused TimeKind = "paused"
const TimeScheduling TimeKind = "scheduling"
const TimeWork TimeKind = "work"

// isIdle сообщает, ждёт ли флоу реакции пользователя прямо сейчас — единое состояние на весь флоу.
//
// awaiting_assistants (T7.7, §5.6) — особый случай: сам по себе статус НЕ пользовательский Idle (работающий
// помощник — машинная работа), но если durable-гейт сейчас блокирован required-задачей, которая требует
// решения человека (needs_changes/inconclusive-вердикт, failed/cancelled-задача или ручная пауза задачи) —
// это пользовательское ожидание, как и needs_changes/approval/pause у самой стадии. Классификация считается
// по rs.Assistants (durable projection), не по наличию WebSocket — одинаково и в live, и при replay.
func isIdle(rs *RunState) bool {
	hasFailed := false
	anyActive := false
	for stageID, st := range rs.Stages {
		switch st.Status {
		case StatusAwaitingUserInput, StatusAwaitingApproval, StatusPaused, StatusHookFailed:
			return true
		case StatusAwaitingAssistants:
			if rs.Assistants.NeedsHumanDecision(stageID) {
				return true
			}
			anyActive = true
		case StatusFailed:
			hasFailed = true
		case StatusRunning, StatusPlanning, StatusRevising, StatusAwaitingPeer, StatusAwaitingQuota:
			anyActive = true
		default:
		}
	}
	return hasFailed && !anyActive
}

// maxUpdatedAt возвращает самый свежий UpdatedAt среди всех стадий — момент последнего примененного во
// флоу перехода.
func maxUpdatedAt(stages map[string]StageState) time.Time {
	var latest time.Time
	for _, st := range stages {
		if st.UpdatedAt.After(latest) {
			latest = st.UpdatedAt
		}
	}
	return latest
}

func openSince(rs *RunState, stageUpdatedAt time.Time) time.Time {
	if rs.RunStartedAt.After(stageUpdatedAt) {
		return rs.RunStartedAt
	}
	return stageUpdatedAt
}

// IdleSince возвращает момент начала текущего периода простоя, если флоу
// простаивает сейчас (см. isIdle) — иначе nil.
func (rs *RunState) IdleSince() *time.Time {
	if rs.EndedAt != nil {
		return nil
	}
	if !isIdle(rs) {
		return nil
	}
	t := openSince(rs, maxUpdatedAt(rs.Stages))
	if t.IsZero() {
		return nil
	}
	return &t
}

func (rs *RunState) ElapsedSince() *time.Time {
	if rs.EndedAt != nil || rs.RunStartedAt.IsZero() {
		return nil
	}
	t := rs.RunStartedAt
	return &t
}

// BackoffOpenSince возвращает момент входа в retrying для каждой стадии, которая сейчас в этом статусе —
// параллельные эпизоды суммируются на чтении, а не мёржатся здесь.
func (rs *RunState) BackoffOpenSince() []time.Time {
	if rs.EndedAt != nil {
		return nil
	}
	var out []time.Time
	for _, st := range rs.Stages {
		if st.Status == StatusRetrying {
			out = append(out, openSince(rs, st.UpdatedAt))
		}
	}
	return out
}

// AllDone returns true when every stage has StatusDone.
func (rs *RunState) AllDone() bool {
	for _, s := range rs.Stages {
		if s.Status != StatusDone {
			return false
		}
	}
	return true
}

func (e *StorageError) Error() string { return "storage failure: " + e.Inner.Error() }

func (e *StorageError) Unwrap() error { return e.Inner }

// currentRootChildren returns stageID's direct (ParentTaskID == nil) helper tasks belonging to the CURRENT
// root generation (RootGenerations[stageID], 0 if never bumped) — a stage-level mirror of
// pkg/orchestrator's currentChildren one level up. MINOR, intentional duplication: pkg/state cannot import
// pkg/orchestrator (forbidden cycle), and introducing a third, lower-level package for six lines of map
// filtering is not worth it — see dev-plan.md's Review decisions.
func (as AssistantsState) currentRootChildren(stageID string) []assistants.TaskID {
	gen := as.RootGenerations[stageID]
	var out []assistants.TaskID
	for _, id := range as.ByRootStage[stageID] {
		if t, ok := as.Tasks[id]; ok && t.ParentTaskID == nil && t.RootGeneration == gen {
			out = append(out, id)
		}
	}
	return out
}

// currentChildrenOf mirrors pkg/orchestrator's currentChildren (ParentGeneration-filtered direct children) —
// same MINOR-duplication rationale as currentRootChildren above.
func (as AssistantsState) currentChildrenOf(parentID assistants.TaskID, parentGeneration uint64) []assistants.TaskID {
	var out []assistants.TaskID
	for _, id := range as.ByParent[parentID] {
		if t, ok := as.Tasks[id]; ok && t.ParentGeneration == parentGeneration {
			out = append(out, id)
		}
	}
	return out
}

// tasksFor resolves a slice of ids to their current assistants.Task values, silently skipping any id that is
// no longer present (defensive only — ByRootStage/ByParent indexes are kept in sync with Tasks by construction).
func (as AssistantsState) tasksFor(ids []assistants.TaskID) []assistants.Task {
	out := make([]assistants.Task, 0, len(ids))
	for _, id := range ids {
		if t, ok := as.Tasks[id]; ok {
			out = append(out, t.Task)
		}
	}
	return out
}

// taskGateClosed reports whether task id has durably closed its own required gate (assistants.TaskGateClosed),
// reading LastVerdict from the projection — never a file. A WAIVED task (Этап 7, T7.6 — a separate, audited
// human decision) always counts as closed, regardless of its actual Status/LastVerdict: that is the whole
// point of a waiver — accepting the risk of a task that will never otherwise close its gate.
func (as AssistantsState) taskGateClosed(id assistants.TaskID) bool {
	t, ok := as.Tasks[id]
	if !ok {
		return false
	}
	if t.Waived {
		return true
	}
	return assistants.TaskGateClosed(t.Status, t.LastVerdict, t.LastVerdict != "")
}

// RequiredBlocker walks stageID's CURRENT required-chain task subtree (pkg/assistants.RequiredGateOpen,
// starting from the stage's current-generation direct children) and returns the first helper task that has
// not yet closed its required gate. ok=false means the barrier is currently satisfied for stageID — either
// there are no required tasks at all, or every one of them (recursively, through required edges only) has
// closed its gate. An advisory task's subtree is never descended into (§4.3) — a required task nested under
// an advisory one blocks only that advisory task, never the root stage.
func (as AssistantsState) RequiredBlocker(stageID string) (TaskState, bool) {
	roots := as.tasksFor(as.currentRootChildren(stageID))
	children := func(t assistants.Task) []assistants.Task {
		return as.tasksFor(as.currentChildrenOf(t.ID, t.TaskGeneration))
	}
	closed := func(t assistants.Task) bool { return as.taskGateClosed(t.ID) }

	open, blocker := assistants.RequiredGateOpen(roots, children, closed)
	if !open || blocker == nil {
		return TaskState{}, false
	}
	ts, ok := as.Tasks[blocker.ID]
	if !ok {
		return TaskState{}, false
	}
	return *ts, true
}

// NeedsHumanDecision reports whether stageID's CURRENT required-completion blocker (if any) requires a human
// decision rather than just more machine work (T7.7, §5.6): a failed/cancelled/held task, or one that
// completed with a non-pass verdict (needs_changes/inconclusive), needs a Revise/Retry/Waiver from a person.
// A task still actively working (waiting_parent/queued/running/waiting_children/watching) does not. ok=false
// from RequiredBlocker (gate already satisfied, or nothing required attached) is "no decision pending" — a
// race between the stage's status and this read, not a durable state. Used both by isIdle (state.go) and by
// pkg/server's StageView (Этап 7, T7.9) to decide whether awaiting_assistants counts as dashboard attention.
// An idle on_message blocker needs a person — a new request or a decision — unless its last request passed,
// which the system closes on its own; while a request is open it is machine work.
func (as AssistantsState) NeedsHumanDecision(stageID string) bool {
	blocker, ok := as.RequiredBlocker(stageID)
	if !ok {
		return false
	}
	if OnMessageIdle(blocker) {
		return blocker.LastOutcome != RequestOutcomeResult || blocker.LastVerdict != assistants.VerdictPass
	}
	switch blocker.Status {
	case assistants.StatusFailed, assistants.StatusCancelled, assistants.StatusPaused:
		return true
	case assistants.StatusCompleted:
		return blocker.LastVerdict != assistants.VerdictPass
	default:
		return false
	}
}

// Paused reports whether this task itself carries at least one hold (does not look at ancestors — see
// AssistantsState.EffectivelyHeld for that).
func (t TaskState) Paused() bool { return len(t.Holds) > 0 }

// Blocks reports whether a task at the given depth is blocked by the CURRENT policy (depth exceeds
// MaxDepth) — used to mark existing deep tasks policy_blocked after a lowered limit, without touching their
// stored Depth/ParentTaskID (TestPolicy_LoweredLimitKeepsExistingTree).
func (p PolicyState) Blocks(depth int) bool { return depth > p.MaxDepth }

// IsOnMessageHelper reports whether t is a stage's top-level helper that takes one turn per addressed request.
func IsOnMessageHelper(t assistants.Task) bool {
	return t.Trigger == assistants.TriggerOnMessage && t.Scope == assistants.ScopeStage && t.ParentTaskID == nil
}

// OnMessageIdle reports whether t is an on_message helper with no request to work on: until one arrives it
// starts no turn and holds nothing open.
func OnMessageIdle(t TaskState) bool {
	return IsOnMessageHelper(t.Task) && !t.Status.Terminal() && t.ActiveRequest == "" && len(t.PendingRequests) == 0
}

// Stopped reports whether the loop was stopped explicitly.
func (l LoopState) Stopped() bool { return l.StoppedSeq != 0 }

// SlotTime is when slot n (counted from 1) is due.
func (l LoopState) SlotTime(n uint64) time.Time {
	return l.CreatedAt.Add(time.Duration(n) * l.Interval)
}

func (e *OperationRejectedError) Error() string {
	return fmt.Sprintf("operation rejected (%s): %s", e.Code, e.Reason)
}

func (e *OperationRejectedError) Unwrap() error { return e.Cause }
