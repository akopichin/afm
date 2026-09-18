// Package lifecyclehooks реализует observer-only lifecycle-хуки AFM (Phase 1):
// команды, получающие JSON-payload события в stdin. Ошибка хука никогда не
// меняет FSM. Спека: docs/superpowers/specs/2026-09-18-lifecycle-hooks-design.md.
package lifecyclehooks

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// EventType — публичное имя lifecycle-события. Стабильный контракт: сырые
// имена FSM (Ev*) и события шины наружу не отдаются.
type EventType string

// События флоу.
const (
	EventFlowStarted     EventType = "flow_started"
	EventFlowResumed     EventType = "flow_resumed"
	EventFlowFinished    EventType = "flow_finished"
	EventFlowFailed      EventType = "flow_failed"
	EventFlowInterrupted EventType = "flow_interrupted"
)

// События стадии (FSM-производные, кроме question_* на авто-ответах).
const (
	EventStagePlanningStarted  EventType = "stage_planning_started"
	EventStagePlanReady        EventType = "stage_plan_ready"
	EventStageApproved         EventType = "stage_approved"
	EventStageRevisionStarted  EventType = "stage_revision_started"
	EventStageExecutionStarted EventType = "stage_execution_started"
	EventStageQuestionAsked    EventType = "stage_question_asked"
	EventStageQuestionAnswered EventType = "stage_question_answered"
	EventStageRetryScheduled   EventType = "stage_retry_scheduled"
	EventStageRetryStarted     EventType = "stage_retry_started"
	EventStagePaused           EventType = "stage_paused"
	EventStageResumed          EventType = "stage_resumed"
	EventStageFinished         EventType = "stage_finished"
	EventStageFailed           EventType = "stage_failed"
)

// События script-стадии и script_before/script_after (не-FSM границы).
const (
	EventStageScriptStarted        EventType = "stage_script_started"
	EventStageScriptFinished       EventType = "stage_script_finished"
	EventStageScriptFailed         EventType = "stage_script_failed"
	EventStageScriptBeforeStarted  EventType = "stage_script_before_started"
	EventStageScriptBeforeFinished EventType = "stage_script_before_finished"
	EventStageScriptBeforeFailed   EventType = "stage_script_before_failed"
	EventStageScriptAfterStarted   EventType = "stage_script_after_started"
	EventStageScriptAfterFinished  EventType = "stage_script_after_finished"
	EventStageScriptAfterFailed    EventType = "stage_script_after_failed"
)

var flowEvents = map[EventType]bool{
	EventFlowStarted: true, EventFlowResumed: true, EventFlowFinished: true,
	EventFlowFailed: true, EventFlowInterrupted: true,
}

// IsFlowEvent сообщает, относится ли событие к флоу (недопустимо в stage hook).
func IsFlowEvent(ev EventType) bool { return flowEvents[ev] }

// AllEvents возвращает весь публичный каталог (для `events: all` и валидации).
func AllEvents() []EventType {
	return []EventType{
		EventFlowStarted, EventFlowResumed, EventFlowFinished, EventFlowFailed, EventFlowInterrupted,
		EventStagePlanningStarted, EventStagePlanReady, EventStageApproved,
		EventStageRevisionStarted, EventStageExecutionStarted,
		EventStageQuestionAsked, EventStageQuestionAnswered,
		EventStageRetryScheduled, EventStageRetryStarted,
		EventStagePaused, EventStageResumed, EventStageFinished, EventStageFailed,
		EventStageScriptStarted, EventStageScriptFinished, EventStageScriptFailed,
		EventStageScriptBeforeStarted, EventStageScriptBeforeFinished, EventStageScriptBeforeFailed,
		EventStageScriptAfterStarted, EventStageScriptAfterFinished, EventStageScriptAfterFailed,
	}
}

// EventSelector: скаляр `all` или список имён событий (scalar-or-list, та же
// идиома, что flow.Input scalar-or-object).
type EventSelector struct {
	All    bool
	Events []EventType
}

// UnmarshalYAML декодирует `events: all` либо `events: [a, b]`.
func (s *EventSelector) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		if value.Value != "all" {
			return fmt.Errorf("events: scalar must be \"all\", got %q", value.Value)
		}
		s.All = true
		return nil
	case yaml.SequenceNode:
		var evs []EventType
		if err := value.Decode(&evs); err != nil {
			return fmt.Errorf("events: %w", err)
		}
		s.Events = evs
		return nil
	default:
		return fmt.Errorf("events must be \"all\" or a list of event names")
	}
}

// Hook — декларация lifecycle-хука (одинаковая для config/flow/stage слоёв).
type Hook struct {
	ID         string        `yaml:"id"`
	Events     EventSelector `yaml:"events"`
	SkipEvents []EventType   `yaml:"skip_events,omitempty"`
	Command    string        `yaml:"command"`
	Timeout    time.Duration `yaml:"timeout,omitempty"` // 0 → DefaultHookTimeout в runner
	Retries    int           `yaml:"retries,omitempty"` // 0 → одна попытка
}

// RegisteredHook — хук, вписанный в итоговый список: StageID "" = все стадии.
type RegisteredHook struct {
	Hook    Hook
	StageID string
}

// Event — событие, передаваемое в Emit. Seq > 0 только у FSM-производных
// (durable seq перехода) — из него строится стабильный event_id.
type Event struct {
	Type      EventType
	StageID   string
	StageName string
	From      string
	To        string
	Phase     string
	Reason    string
	Seq       uint64
	Time      time.Time
}

// DispatcherConfig — метаданные рана для payload/env.
type DispatcherConfig struct {
	FlowName string
	RunID    string
	RunDir   string
	RootDir  string
	Resumed  bool
}

// FlowInfo — блок flow JSON-payload.
type FlowInfo struct {
	Name    string `json:"name"`
	RunID   string `json:"run_id"`
	Resumed bool   `json:"resumed"`
	RunDir  string `json:"run_dir"`
	RootDir string `json:"root_dir"`
}

// StageInfo — блок stage JSON-payload (nil для flow-событий).
type StageInfo struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	From  string `json:"from,omitempty"`
	To    string `json:"to,omitempty"`
	Phase string `json:"phase,omitempty"`
}

// Payload — JSON-контракт hook-процесса (stdin).
type Payload struct {
	SchemaVersion int            `json:"schema_version"`
	EventID       string         `json:"event_id"`
	Event         EventType      `json:"event"`
	OccurredAt    time.Time      `json:"occurred_at"`
	Flow          FlowInfo       `json:"flow"`
	Stage         *StageInfo     `json:"stage,omitempty"`
	Reason        string         `json:"reason,omitempty"`
	Data          map[string]any `json:"data,omitempty"`
}

// BuildPayload собирает Payload из метаданных и события.
func BuildPayload(cfg DispatcherConfig, ev Event, eventID string) Payload {
	p := Payload{
		SchemaVersion: 1,
		EventID:       eventID,
		Event:         ev.Type,
		OccurredAt:    ev.Time,
		Flow: FlowInfo{
			Name: cfg.FlowName, RunID: cfg.RunID, Resumed: cfg.Resumed,
			RunDir: cfg.RunDir, RootDir: cfg.RootDir,
		},
		Reason: ev.Reason,
	}
	if ev.StageID != "" {
		p.Stage = &StageInfo{
			ID: ev.StageID, Name: ev.StageName,
			From: ev.From, To: ev.To, Phase: ev.Phase,
		}
	}
	return p
}
