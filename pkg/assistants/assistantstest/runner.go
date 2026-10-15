// Package assistantstest даёт детерминированный fake-runner и общие
// JSON-фикстуры ответа помощника для тестов пакетов, которые работают с
// pkg/assistants, но не хотят платить за вызов настоящей модели.
//
// ScriptedRunner НЕ импортирует pkg/executor — только сигнатуру его
// RunAgent-контракта, которую повторяет (см. pkg/sideagent.AgentRunner), так
// что он — drop-in замена исполнителя в обоих рантаймах (side-agent и, позже,
// collaboration).
package assistantstest

import (
	"context"
	"time"
)

// toolText — имя «инструмента», которым, по соглашению pkg/executor/
// pkg/sideagent, помечается финальная проза хода агента (не tool-вызов).
const toolText = "text"

// Action — одно опубликованное действие агента: имя инструмента и его деталь,
// как в executor.Config.OnAction(tool, detail string).
type Action struct {
	Tool   string
	Detail string
}

// Step — один шаг сценария ScriptedRunner. Поля взаимоисключающие по смыслу
// (обрабатываются в порядке Fail > Action > Result — см. RunAgent):
//   - Fail != nil — ход завершается этой ошибкой, ничего не публикуя;
//   - Action != nil — публикуется один вызов инструмента;
//   - Result != "" — публикуется финальный текст хода (через toolText).
//
// Delay, если > 0, ждётся через Clock ДО публикации — позволяет
// эмулировать медленного агента без реального time.Sleep в тестах.
type Step struct {
	Delay  time.Duration
	Action *Action
	Fail   error
	Result string
}

// Clock абстрагирует течение времени, чтобы тесты ScriptedRunner никогда не
// зависели от wall-clock. Вызывается только когда Step.Delay > 0.
type Clock interface {
	// Sleep блокируется на d или до отмены ctx — смотря что наступит раньше;
	// возвращает ctx.Err() в обоих случаях (nil, если ctx жив и d истекло).
	Sleep(ctx context.Context, d time.Duration) error
}

// realClock — Clock поверх настоящего времени. Дефолт ScriptedRunner, если
// вызывающий не подставил свой — в тестовом пути подставляй fakeClock.
type realClock struct{}

func (realClock) Sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ScriptedRunner — детерминированный AgentRunner: проигрывает заранее заданный
// список шагов вместо обращения к настоящей модели.
type ScriptedRunner struct {
	steps []Step

	// Clock — источник задержек. По умолчанию realClock{}; тесты подставляют
	// свой (без реального сна).
	Clock Clock
	// OnAction вызывается синхронно для каждого Action/Result шага, до
	// перехода к следующему шагу. nil — действия просто не публикуются.
	OnAction func(tool, detail string)
}

// NewScriptedRunner строит ScriptedRunner с заданным сценарием. Шаги
// копируются — вызывающий может переиспользовать свой слайс.
func NewScriptedRunner(steps ...Step) *ScriptedRunner {
	cp := make([]Step, len(steps))
	copy(cp, steps)
	return &ScriptedRunner{steps: cp, Clock: realClock{}}
}

// RunAgent проигрывает шаги сценария по порядку. Сигнатура совпадает с
// sideagent.AgentRunner (pkg/sideagent/runner.go:15) — параметры agentType/
// stageName/prompt/logFile сценарием не используются, они здесь только чтобы
// ScriptedRunner подходил как drop-in замена настоящего раннера.
func (r *ScriptedRunner) RunAgent(ctx context.Context, _, _, _, _ string) error {
	for _, step := range r.steps {
		if err := ctx.Err(); err != nil {
			return err
		}
		if step.Delay > 0 {
			if err := r.Clock.Sleep(ctx, step.Delay); err != nil {
				return err
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		switch {
		case step.Fail != nil:
			return step.Fail
		case step.Action != nil:
			if r.OnAction != nil {
				r.OnAction(step.Action.Tool, step.Action.Detail)
			}
		case step.Result != "":
			if r.OnAction != nil {
				r.OnAction(toolText, step.Result)
			}
		default:
			// Пустой шаг (ни Fail, ни Action, ни Result) — ничего не публикуем.
		}
	}
	return nil
}
