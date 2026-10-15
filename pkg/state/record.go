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

// Target называет основную сущность, которой адресована control-запись (задача помощника и/или её корневая
// стадия). Оба поля опциональны — у flow-уровневых записей (например policy_set) может не быть ни задачи,
// ни конкретной стадии.
type Target struct {
	TaskID  string `json:"task_id,omitempty"`
	StageID string `json:"stage_id,omitempty"`
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
	// kindOperationCommitted — the single wrapper kind ApplyRunOperation (T2.4) writes: one Control record
	// bundling every Effect of one caller-intent operation, applied all-or-nothing. Its payload's kind is
	// op.Kind (the caller's semantic name, e.g. "create_children") — distinct from the Control's own Kind,
	// which is always this fixed wrapper string so decodeLine/controlKinds only need to know ONE kind for
	// the whole operation machinery, regardless of how many different op.Kind values callers invent.
	kindOperationCommitted = "operation_committed"
)

// controlKinds — множество control-kind'ов, которые понимает эта сборка. Расширяется по мере добавления
// задач (T2.3 заводит task_*, T2.6 — policy_set/quota_reserved). Неизвестный kind — не ошибка на будущее,
// а гарантия того, что лог, записанный более новой версией afm, не будет молча неверно понят более старой.
var controlKinds = map[string]bool{
	kindTaskCreated:           true,
	kindTaskStatusChanged:     true,
	kindTaskHoldSet:           true,
	kindTaskHoldCleared:       true,
	kindTaskContinuationSaved: true,
	kindPolicySet:             true,
	kindQuotaReserved:         true,
	kindTaskResult:            true,
	kindOperationCommitted:    true,
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
