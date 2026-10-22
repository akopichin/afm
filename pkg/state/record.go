package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrUnsupportedRecord — строка журнала синтаксически валидна как control-запись (непустой kind), но
// называет kind или schema_version, которые эта сборка не понимает. В отличие от битой строки (торчащий
// хвост/сломанный JSON), это НЕ повод для карантина: ран не может безопасно продолжиться с неполным графом
// задач помощников, поэтому разбор останавливается немедленно и жёстко (fail-closed) — оригинал
// events.jsonl не трогается, в отличие от quarantine-пути ErrCorruptLog.
var ErrUnsupportedRecord = errors.New("unsupported control record")

// ControlSchemaVersion — единственная поддерживаемая версия envelope'а control-записей.
const ControlSchemaVersion = 1

// Actor называет, кто инициировал control-запись: человек через UI, side agent, сам помощник, или система
// (восстановление/политика).
type Actor struct {
	Kind string `json:"kind,omitempty"`
	ID   string `json:"id,omitempty"`
}

// ActorKindUser — Actor.Kind человека, работающего через дашборд или CLI.
const ActorKindUser = "user"

// Target называет основную сущность, которой адресована control-запись (задача помощника и/или её корневая
// стадия). Поля опциональны — у flow-уровневых записей (например policy_set) может не быть ни задачи,
// ни конкретной стадии. MessageID/ProposalID/IntentID (T6.1/T6.4/T6.6) следуют тому же соглашению, что и
// TaskID: это СОБСТВЕННЫЙ id сущности, которую создаёт/меняет эта запись, а не произвольная ссылка.
type Target struct {
	TaskID  string `json:"task_id,omitempty"`
	StageID string `json:"stage_id,omitempty"`
	// MessageID — id обсуждения (discussion_message), T6.1.
	MessageID string `json:"message_id,omitempty"`
	// ProposalID — id предложения (proposal_created/proposal_status_changed), T6.4.
	ProposalID string `json:"proposal_id,omitempty"`
	// IntentID — id FeedbackIntent (feedback_intent_created/feedback_intent_consumed), T6.5/T6.6.
	IntentID string `json:"intent_id,omitempty"`
	// ThreadID — id of an agentcomm.Thread (mesh_thread_opened/mesh_thread_closed/mesh_message_sent),
	// Agent Mesh Phase 1 (T1.1). Follows the same "own id of the entity this record creates/changes" rule as
	// MessageID/ProposalID above.
	ThreadID string `json:"thread_id,omitempty"`
	// ParticipantID — id of an agentcomm.Participant: the entity created by mesh_participant_added, or the
	// addressed recipient for a mesh_message_delivered/mesh_message_acked/mesh_message_undeliverable receipt,
	// or the publishing author for mesh_board_published (T1.1/T1.2/T1.4).
	ParticipantID string `json:"participant_id,omitempty"`
	// WaitID — id of an agentcomm.Wait (mesh_wait_registered/mesh_wait_resolved), Agent Mesh Phase 1 (T1.3).
	WaitID string `json:"wait_id,omitempty"`
	// BoardKey — the shared board's key (mesh_board_published), Agent Mesh Phase 1 (T1.4). Unlike the other
	// Target id fields this is not server-minted: the caller names an arbitrary, meaningful key (e.g.
	// "design-doc"), so there is no derive-from-operation-id convention for it.
	BoardKey string `json:"board_key,omitempty"`
}

// Control — управляющая запись нового формата журнала (помощники/задачи/политика/квоты). Отличается от
// legacy Transition наличием непустого Kind — см. decodeLine.
type Control struct {
	Seq           uint64          `json:"seq"`
	Time          time.Time       `json:"time"`
	Kind          string          `json:"kind"`
	SchemaVersion int             `json:"schema_version"`
	OperationID   string          `json:"operation_id,omitempty"`
	Actor         Actor           `json:"actor"`
	Target        Target          `json:"target"`
	Payload       json.RawMessage `json:"payload,omitempty"`
}

// Control kind constants — the full set this build understands (controlKinds below). Named once here and
// reused everywhere a kind is compared/switched on, rather than repeating the string literal.
const (
	kindTaskCreated           = "task_created"
	kindTaskStatusChanged     = "task_status_changed"
	kindTaskHoldSet           = "task_hold_set"
	kindTaskHoldCleared       = "task_hold_cleared"
	kindTaskContinuationSaved = "task_continuation_saved"
	kindPolicySet             = "policy_set"
	kindQuotaReserved         = "quota_reserved"
	// kindTaskResult — carries a completed task's decoded assistants.Result (verdict/summary/findings/...)
	// as its Payload, bundled in the SAME ApplyRunOperation call as the task_status_changed-to-"completed"
	// effect (pkg/orchestrator's finishTaskFrom). Observational only: applyTaskResult (below) never mutates
	// TaskState — the durable control stream (Store.TaskEvents) IS the read path the dashboard's per-task
	// event-history page (GET /api/assistants/{id}/events) consumes.
	kindTaskResult = "task_result"
	// kindTaskGenerationBumped/kindRootGenerationBumped (T4a.6): a task's own TaskGeneration, or a root
	// stage's whole-tree RootGeneration counter, incremented by one. Used to mark everything created BEFORE
	// the bump (a child's ParentGeneration/RootGeneration, stamped once at task_created time and never
	// rewritten) as belonging to a superseded attempt — see assistants.Task's own field doc comments and
	// pkg/orchestrator's requiredChildOpen/tryResumeParent/collectChildResultsJSON, which all skip a child
	// whose ParentGeneration no longer matches its parent's CURRENT TaskGeneration.
	kindTaskGenerationBumped = "task_generation_bumped"
	kindRootGenerationBumped = "root_generation_bumped"
	// kindOperationCommitted — the single wrapper kind ApplyRunOperation (T2.4) writes: one Control record
	// bundling every Effect of one caller-intent operation, applied all-or-nothing. Its payload's kind is
	// op.Kind (the caller's semantic name, e.g. "create_children") — distinct from the Control's own Kind,
	// which is always this fixed wrapper string so decodeLine/controlKinds only need to know ONE kind for
	// the whole operation machinery, regardless of how many different op.Kind values callers invent.
	kindOperationCommitted = "operation_committed"
	// kindDiscussionMessage (T6.1) — one durable entry in a stage's discussion log. Round-limit enforcement
	// (at most one independent + one discussion round, plus a separately-bounded synthesis "round") happens
	// INSIDE applyDiscussionMessage, so a record that would exceed the budget never reaches the journal at
	// all (same validate-before-write discipline as applyTaskCreated's whole-tree check).
	kindDiscussionMessage = "discussion_message"
	// kindProposalCreated/kindProposalStatusChanged (T6.4) — a Proposal's creation and every lifecycle
	// transition thereafter. Illegal transitions (assistants.Proposal.CanTransition) are rejected the same
	// fail-closed way.
	kindProposalCreated       = "proposal_created"
	kindProposalStatusChanged = "proposal_status_changed"
	// kindFeedbackIntentCreated/kindFeedbackIntentConsumed (T6.5/T6.6) — the durable FeedbackIntent pipeline
	// that carries an accepted proposal's (or a plain user note's) feedback to the general Revise path exactly
	// once, surviving a crash at any point.
	kindFeedbackIntentCreated  = "feedback_intent_created"
	kindFeedbackIntentConsumed = "feedback_intent_consumed"
	// kindTaskWaived (Этап 7, T7.6) — a human explicitly accepted the risk of this required task never
	// closing its own gate (failed/cancelled/needs_changes, or simply never run). A SEPARATE, explicitly-
	// audited operation — never a side effect of cancel/retry/any task_result — Actor is checked at admission
	// (pkg/orchestrator's WaiveTask) to be a human, never a model's own result (§13.2/§19.5).
	kindTaskWaived = "task_waived"
	// kindStageGraphAdded (Этап 9, T9.4) — one durable record of a dynamically-added regular stage: the new
	// stage's full definition, any additional dependency it gives an EXISTING stage (insert-before never
	// removes a dependency, only adds one — see DynamicStageAdded's doc comment), and the resulting stage
	// display order, all three bundled into this ONE control record/journal write. Deliberately a TOP-LEVEL
	// kind (handled directly in dispatchControl against the whole *RunState), not an Effect nested inside
	// kindOperationCommitted: that wrapper's per-effect projection (applyOperationCommitted) only ever mutates
	// AssistantsState, which has nothing to do with the stage graph/StageOrder this kind needs to touch.
	kindStageGraphAdded = "stage_graph_added"
	// Agent Mesh Phase 1 (T1.1-T1.4) control kinds — the durable layer for pkg/agentcomm's shared vocabulary
	// (participants/threads/messages/receipts/waits/board). Each projects onto AssistantsState.Mesh
	// (mesh_projection.go/mesh_waits.go/mesh_board.go), the same way the task-tree kinds above project onto
	// AssistantsState's other fields — dispatchControlKind handles every one of them, standalone or bundled as
	// an operation_committed Effect.
	kindMeshParticipantAdded     = "mesh_participant_added"
	kindMeshThreadOpened         = "mesh_thread_opened"
	kindMeshThreadTitleUpdated   = "mesh_thread_title_updated"
	kindMeshThreadClosed         = "mesh_thread_closed"
	kindMeshMessageSent          = "mesh_message_sent"
	kindMeshMessageDelivered     = "mesh_message_delivered"
	kindMeshMessageAcked         = "mesh_message_acked"
	kindMeshMessageUndeliverable = "mesh_message_undeliverable"
	kindMeshWaitRegistered       = "mesh_wait_registered"
	kindMeshWaitResolved         = "mesh_wait_resolved"
	kindMeshBoardPublished       = "mesh_board_published"
	kindMeshBoardProtocolSet     = "mesh_board_protocol_set"
)

// controlKinds — множество control-kind'ов, которые понимает эта сборка. Расширяется по мере добавления
// задач (T2.3 заводит task_*, T2.6 — policy_set/quota_reserved). Неизвестный kind — не ошибка на будущее,
// а гарантия того, что лог, записанный более новой версией afm, не будет молча неверно понят более старой.
var controlKinds = map[string]bool{
	kindTaskCreated:              true,
	kindTaskStatusChanged:        true,
	kindTaskHoldSet:              true,
	kindTaskHoldCleared:          true,
	kindTaskContinuationSaved:    true,
	kindPolicySet:                true,
	kindQuotaReserved:            true,
	kindTaskResult:               true,
	kindTaskGenerationBumped:     true,
	kindRootGenerationBumped:     true,
	kindOperationCommitted:       true,
	kindDiscussionMessage:        true,
	kindProposalCreated:          true,
	kindProposalStatusChanged:    true,
	kindFeedbackIntentCreated:    true,
	kindFeedbackIntentConsumed:   true,
	kindTaskWaived:               true,
	kindStageGraphAdded:          true,
	kindMeshParticipantAdded:     true,
	kindMeshThreadOpened:         true,
	kindMeshThreadTitleUpdated:   true,
	kindMeshThreadClosed:         true,
	kindMeshMessageSent:          true,
	kindMeshMessageDelivered:     true,
	kindMeshMessageAcked:         true,
	kindMeshMessageUndeliverable: true,
	kindMeshWaitRegistered:       true,
	kindMeshWaitResolved:         true,
	kindMeshBoardPublished:       true,
	kindMeshBoardProtocolSet:     true,
}

// recordProbe — первый проход разбора одной непустой строки журнала: достаточно kind и schema_version,
// чтобы решить, какую полную форму разбирать (или отказаться fail-closed), не трогая остальные поля.
type recordProbe struct {
	Kind          string `json:"kind"`
	SchemaVersion int    `json:"schema_version"`
}

// decodedLine — результат успешного разбора одной непустой строки журнала. Ровно одно из двух полей
// непусто.
type decodedLine struct {
	transition *Transition
	control    *Control
}

// decodeLine разбирает одну непустую строку events.jsonl. Три исхода:
//   - err == nil: dl несёт ровно один из transition/control.
//   - err != nil, unsupported == false: строка синтаксически битая (malformed JSON либо type mismatch) —
//     вызывающий применяет существующую обработку "битая строка в середине лога" (карантин, ErrCorruptLog).
//   - err != nil, unsupported == true (errors.Is(err, ErrUnsupportedRecord)): строка синтаксически валидна,
//     но называет kind/schema_version, которых эта сборка не знает — вызывающий обязан остановить разбор
//     НЕМЕДЛЕННО и не продолжать с частичным состоянием (fail-closed, без карантина).
func decodeLine(line []byte) (dl decodedLine, unsupported bool, err error) {
	var probe recordProbe
	if err := json.Unmarshal(line, &probe); err != nil {
		return decodedLine{}, false, err
	}
	if probe.Kind == "" {
		// Legacy-ветка: поведение не меняется относительно кода до T2.1.
		var t Transition
		if err := json.Unmarshal(line, &t); err != nil {
			return decodedLine{}, false, err
		}
		return decodedLine{transition: &t}, false, nil
	}
	if !controlKinds[probe.Kind] || probe.SchemaVersion != ControlSchemaVersion {
		return decodedLine{}, true, fmt.Errorf("%w: kind=%q schema_version=%d", ErrUnsupportedRecord, probe.Kind, probe.SchemaVersion)
	}
	var c Control
	if err := json.Unmarshal(line, &c); err != nil {
		return decodedLine{}, false, err
	}
	return decodedLine{control: &c}, false, nil
}

// applyControlToRunState is defined in assistants_projection.go (T2.3onward) — it projects a single control
// record onto RunState (task tree, holds, continuations, policy, quotas, idempotency index). Called from
// BOTH the live write path (Store.AppendControl) and replay (parseEventLog) so the two can never diverge —
// same "один хелпер, два call site" pattern as accountIdleAndBackoff.
