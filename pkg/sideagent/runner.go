package sideagent

import (
	"context"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/redact"
)

// toolText — имя «инструмента», которым executor помечает прозу агента. Держим собственную константу, а не
// импортируем неэкспортируемую из executor.
const toolText = "text"

// AgentRunner запускает одного агента для одного хода бокового разговора. Блокирует до завершения агента.
type AgentRunner interface {
	RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error
}

// NewExecutorRunner — продовая фабрика AgentRunner поверх executor.New.
// Используется Manager по умолчанию, когда Config.NewExecutor не задан.
func NewExecutorRunner(cfg executor.Config) AgentRunner {
	return executor.New(cfg)
}

// mapAction превращает одно действие исполнителя в типизированное событие журнала разговора.
func mapAction(tool, detail, logRef string, secrets []string) (EventType, any) {
	detail = redact.String(detail, secrets)
	if tool == toolText {
		return EventAssistantText, AssistantTextData{Text: detail}
	}
	return EventToolAction, ToolActionData{
		Tool:   tool,
		Detail: detail,
		LogRef: logRef,
	}
}
