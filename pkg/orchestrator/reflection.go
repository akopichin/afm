package orchestrator

import (
	"context"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/akopichin/afm/pkg/memory"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
)

// maybeRunReflection запускает конвейер памяти в фоне после завершения стадии.
// No-op, если память выключена, стадия без reflect, или это script-стадия
// (нет агентской сессии). Best-effort: НИКОГДА не трогает FSM.
func (o *Orchestrator) maybeRunReflection(ctx context.Context, stageID string) {
	if o.opts.MemoryProjectPath == "" {
		return
	}
	stage := o.graph.Stage(stageID)
	if stage == nil || !stage.Reflect || stage.IsScript() {
		return
	}
	stageDir := filepath.Join(o.opts.RunDir, stageID)
	// Источники: компактные логи фаз стадии + summary/plan. Агент читает сам.
	sources := []string{stageDir} // директория; reflect читает *.log под ней
	o.pendingReflections.Add(1)
	o.concurrency.SpawnDetached(ctx, func(ctx context.Context) {
		defer func() {
			o.pendingReflections.Add(-1)
			o.concurrency.WakeEventLoop()
		}()
		o.runReflectionPipeline(ctx, stage.Name, sources, stageDir)
	})
}

// runReflectionPipeline — reflect → consolidator → afm(reconcile/evict/save).
// Сериализован reflectMu. logDir — куда класть reflect.log/consolidator.log/…
// и reflect_dataset.yaml/consolidated.yaml. Best-effort: любая ошибка шага
// прерывает конвейер и логируется нотисом, но не трогает FSM и не валит ран.
func (o *Orchestrator) runReflectionPipeline(ctx context.Context, stageName string, sources []string, logDir string) {
	o.reflectMu.Lock()
	defer o.reflectMu.Unlock()

	datasetOut := filepath.Join(logDir, "reflect_dataset.yaml")
	consolidatedOut := filepath.Join(logDir, "consolidated.yaml")

	if err := o.runMemoryAgent(ctx, memoryAgentSpec{
		kind:       memoryKindReflect,
		stageName:  stageName,
		sources:    sources,
		datasetOut: datasetOut,
		logFile:    filepath.Join(logDir, "reflect.log"),
	}); err != nil {
		o.reflectFailed(stageName, memoryKindReflect, err)
		return
	}

	if err := o.runMemoryAgent(ctx, memoryAgentSpec{
		kind:        memoryKindConsolidator,
		stageName:   stageName,
		datasetPath: datasetOut,
		projectPath: o.opts.MemoryProjectPath,
		sessionPath: o.opts.MemorySessionPath,
		outPath:     consolidatedOut,
		logFile:     filepath.Join(logDir, "consolidator.log"),
	}); err != nil {
		o.reflectFailed(stageName, memoryKindConsolidator, err)
		return
	}

	o.reconcileAndSave(stageName, consolidatedOut)
}

// reconcileAndSave — код-владелец записи файлов памяти (агенты сами их
// никогда не пишут, только читают). Читает смёрженный YAML консолидатора,
// сверяет метаданные (new/reinforced/unchanged → first_seen/last_seen/
// confirm_count) относительно ОБОИХ текущих сторов, разбивает результат по
// scope, вытесняет лишнее (Evict) и атомарно сохраняет (Save) каждый scope
// независимо. Best-effort: ошибка чтения/парсинга/сохранения логируется
// нотисом и не прерывает остальные шаги.
func (o *Orchestrator) reconcileAndSave(stageName, consolidatedPath string) {
	data, err := os.ReadFile(consolidatedPath)
	if err != nil {
		o.reflectFailed(stageName, memoryKindConsolidator, err)
		return
	}
	var merged memory.MergedStore
	if err := yaml.Unmarshal(data, &merged); err != nil {
		o.reflectFailed(stageName, memoryKindConsolidator, err)
		return
	}

	runID := filepath.Base(o.opts.RunDir)
	prevProject, _ := memory.Load(o.opts.MemoryProjectPath)
	prevSession, _ := memory.Load(o.opts.MemorySessionPath)
	reconciled := memory.Reconcile(mergeStores(prevProject, prevSession), merged, runID)

	var projectStore, sessionStore memory.Store
	for _, f := range reconciled.Findings {
		if f.Scope == memory.ScopeSession {
			sessionStore.Findings = append(sessionStore.Findings, f)
			continue
		}
		projectStore.Findings = append(projectStore.Findings, f)
	}

	if err := memory.Save(o.opts.MemoryProjectPath, memory.Evict(projectStore, o.opts.Memory.MaxFindings)); err != nil {
		o.reflectNotice(stageName, "failed to save project memory: "+err.Error())
	}
	if err := memory.Save(o.opts.MemorySessionPath, memory.Evict(sessionStore, o.opts.Memory.MaxFindings)); err != nil {
		o.reflectNotice(stageName, "failed to save session memory: "+err.Error())
	}
}

// mergeStores объединяет findings двух сторов (project+session) в один —
// memory.Reconcile принимает единый prev Store, не разделённый по scope.
func mergeStores(a, b memory.Store) memory.Store {
	out := memory.Store{Findings: make([]memory.Finding, 0, len(a.Findings)+len(b.Findings))}
	out.Findings = append(out.Findings, a.Findings...)
	out.Findings = append(out.Findings, b.Findings...)
	return out
}

// reflectFailed / reflectNotice — best-effort UI-нотисы (live + notices.jsonl),
// НЕ FSM. Зеркалит publishHookNotice/AppendNotice.
func (o *Orchestrator) reflectFailed(stageName, step string, err error) {
	o.reflectNotice(stageName, "reflection "+step+" failed: "+err.Error())
}

func (o *Orchestrator) reflectNotice(stageName, msg string) {
	data := map[string]string{"stage": stageName, "message": msg}
	o.ui.Publish(bus.Event{Type: bus.EventReflectFailed, Data: data})
	stagefiles.AppendNotice(o.opts.RunDir, "", string(bus.EventReflectFailed), data)
}

// runFinalReflectionOnce прогоняет конвейер памяти ОДИН раз по ВСЕЙ сессии
// флоу в конце Run(). reflect читает логи всех стадий рана (директория RunDir).
// Синхронный — Run() дожидается его перед завершением. Идемпотентен.
func (o *Orchestrator) runFinalReflectionOnce(ctx context.Context) {
	if o.finalReflectDone {
		return
	}
	if o.opts.MemoryProjectPath == "" || !o.opts.Memory.FinalReflect {
		return
	}
	o.finalReflectDone = true
	o.runReflectionPipeline(ctx, "flow-final", []string{o.opts.RunDir}, o.opts.RunDir)
}

// initSessionMemory сбрасывает SESSION-стор в свежий пустой Store на старте
// рана (пер-ран скоуп: предыдущий ран не переносится). No-op, если память
// выключена.
func (o *Orchestrator) initSessionMemory() {
	if o.opts.MemoryProjectPath == "" || o.opts.MemorySessionPath == "" {
		return
	}
	_ = memory.Save(o.opts.MemorySessionPath, memory.Store{})
}
