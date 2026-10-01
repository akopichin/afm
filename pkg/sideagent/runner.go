package sideagent

import (
	"context"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/redact"
)

// toolText — имя «инструмента», которым executor помечает прозу агента
// (contentToAction отдаёт ("text", <текст>) для ассистентского нарратива).
// Держим собственную константу, а не импортируем неэкспортируемую из executor.
const toolText = "text"

// AgentRunner запускает одного агента для одного хода бокового разговора.
// Сигнатура намеренно совпадает с (*executor.Executor).RunAgent, чтобы реальный
// исполнитель удовлетворял интерфейсу напрямую, а тесты подменяли его фейком без
// запуска подпроцесса. Действия агента приходят через executor.Config.OnAction,
// переданный в фабрику NewExecutor; InterruptCh/Dir/WrapperDir и т.п. — тоже из
// Config. Блокирует до завершения агента.
type AgentRunner interface {
	RunAgent(ctx context.Context, agentType, stageName, prompt, logFile string) error
}

// NewExecutorRunner — продовая фабрика AgentRunner поверх executor.New.
// Используется Manager по умолчанию, когда Config.NewExecutor не задан.
func NewExecutorRunner(cfg executor.Config) AgentRunner {
	return executor.New(cfg)
}

// mapAction превращает одно действие исполнителя (tool, detail) в типизированное
// событие журнала разговора. Весь пользовательский текст (проза и detail) ДО
// возврата проходит через redact.String — секрет не попадёт ни в durable-журнал,
// ни в доставленную подписчику копию (инвариант 8). tool=="text" → проза агента
// (assistant_text); остальные инструменты → tool_action со ссылкой на подробный
// лог хода (logRef). Имя инструмента (Bash/Write/…) — фиксированный идентификатор,
// не пользовательский текст, поэтому не редактируется.
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
