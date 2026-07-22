package orchestrator

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/akopichin/afm/pkg/flow"
)

// isAutonomousStage возвращает true, если stageDir содержит autonomous.flag —
// маркер того, что стадия уже переведена на автономный трек.
func isAutonomousStage(stageDir string) bool {
	_, err := os.Stat(filepath.Join(stageDir, "autonomous.flag"))
	return err == nil
}

// clearStaleAutonomousFlag удаляет autonomous.flag, оставшийся от более раннего
// решения супервизора (или от неудавшейся автономной попытки), когда текущая
// попытка идёт по стандартному треку (planning). Без этого isAutonomousStage
// (и производный от неё stage_autonomous в /api/status) навсегда считал бы
// стадию автономной — даже после того, как она реально прошла planning и
// получила настоящий plan.md, ожидающий approve/revise в дашборде.
func clearStaleAutonomousFlag(stageDir string) {
	_ = os.Remove(filepath.Join(stageDir, "autonomous.flag"))
}

// logSupervisorDecision записывает решение супервизора в <runDir>/supervisor.jsonl
// (одна JSON-запись на строку). Ошибки записи логируются молча — этот файл
// лучшего характера (для аудита/UI), fallback DetermineStagePhases на него не завязан.
func (o *Orchestrator) logSupervisorDecision(stageID, decision, reason string) {
	type entry struct {
		Ts       string `json:"ts"`
		StageID  string `json:"stage_id"`
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	e := entry{
		Ts:       time.Now().UTC().Format(time.RFC3339),
		StageID:  stageID,
		Decision: decision,
		Reason:   reason,
	}
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	logPath := filepath.Join(o.opts.RunDir, "supervisor.jsonl")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(data, '\n'))
}

// startWithSupervisor — общая точка запуска стадии после решения супервизора:
// автономный трек (пишет autonomous.flag + durable-переходы + runAutonomousAgent)
// либо обычное планирование. Идентичный блок раньше дублировался в
// startPlanningForUnblocked (scheduling.go) и в recovery.go; извлечён сюда.
// Вызывается как agent-функция через spawnAgent.
func (o *Orchestrator) startWithSupervisor(ctx context.Context, s flow.Stage) {
	phases := o.DetermineStagePhases(ctx, s)
	if len(phases) == 1 && phases[0] == phaseAutonomous {
		stageDir := filepath.Join(o.opts.RunDir, s.ID)
		if err := os.MkdirAll(stageDir, 0755); err != nil {
			o.Trigger(s.ID, EvFail, GuardCtx{}, "mkdir failed")
			return
		}
		_ = os.WriteFile(filepath.Join(stageDir, "autonomous.flag"), nil, 0644)
		o.Trigger(s.ID, EvSupervisorApproved, GuardCtx{}, "supervisor: autonomous")
		o.Trigger(s.ID, EvStartRun, GuardCtx{}, "")
		o.runAutonomousAgent(ctx, s)
	} else {
		o.runPlanningAgent(ctx, s)
	}
}

// DetermineStagePhases вызывает Supervisor и возвращает выбранные фазы для стадии.
// Вызывается внутри горутины планирования (не блокирует event loop).
//
// Правила:
//   - stage.Supervisor=false ИЛИ supervisor отключён (nil) → базовые фазы.
//   - inline-артефакт guard: наличие inline-артефакта форсирует стандартный цикл
//     (planning пропускать нельзя — агенту нужен контекст артефакта в plan.md).
//   - при любой ошибке LLM/парсинга → фолбэк на базовые фазы (без crash flow).
//   - CanExecuteAutonomously=true → ["autonomous_execution"], логируется решение,
//     публикуется событие EventSupervisorDecision.
func (o *Orchestrator) DetermineStagePhases(ctx context.Context, s flow.Stage) []string {
	base := agentTypesToStrings(s.Agents)

	if !s.Supervisor || o.supervisor == nil {
		return base
	}
	// Inline-артефакт guard: planning пропускать нельзя — агенту нужен контекст
	// артефакта (фабрика/спецификация) для корректного plan.md.
	for _, art := range s.Artifacts {
		if art.IsInline() {
			log.Printf("supervisor: stage %s has inline artifact %q, skipping evaluation", s.ID, art.Name)
			return base
		}
	}
	decision, err := o.supervisor.EvaluateStage(ctx, s, o.opts.GlobalPrompt)
	if err != nil {
		log.Printf("supervisor: fallback for stage %s: %v", s.ID, err)
		return base
	}
	// Решение супервизора публикуем в UI и пишем в supervisor.jsonl для ОБОИХ треков
	// (раньше standard не публиковался — UI не видел резолюцию).
	track := "standard"
	if decision.CanExecuteAutonomously {
		track = "autonomous"
	}
	o.logSupervisorDecision(s.ID, track, decision.Reason)
	o.ui.Publish(Event{
		Type:    EventSupervisorDecision,
		StageID: s.ID,
		Data:    decision,
	})
	if decision.CanExecuteAutonomously {
		log.Printf("supervisor: stage %s → autonomous_execution. Reason: %s", s.ID, decision.Reason)
		return []string{phaseAutonomous}
	}
	log.Printf("supervisor: stage %s → standard. Reason: %s", s.ID, decision.Reason)
	return base
}
