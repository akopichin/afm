package orchestrator

import (
	"context"
	"os"
	"path/filepath"

	"github.com/akopichin/afm/pkg/memory"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
)

// maybeRunReflection запускает конвейер памяти в фоне после завершения стадии.
// No-op, если память выключена, стадия без reflect (или reflect не включает
// write), или это script-стадия (нет агентской сессии). Best-effort: НИКОГДА
// не трогает FSM.
func (o *Orchestrator) maybeRunReflection(ctx context.Context, stageID string) {
	if o.opts.MemoryDir == "" {
		return
	}
	stage := o.graph.Stage(stageID)
	if stage == nil || stage.Reflect == nil || !stage.Reflect.CanWrite() || stage.IsScript() {
		return
	}
	stageDir := filepath.Join(o.opts.RunDir, stageID)
	targetFile := memory.StageFile(o.opts.MemoryDir, stage.Reflect.File)

	o.pendingReflections.Add(1)
	o.concurrency.SpawnDetached(ctx, func(ctx context.Context) {
		defer func() {
			o.pendingReflections.Add(-1)
			o.concurrency.WakeEventLoop()
		}()

		dataset := filepath.Join(stageDir, "reflect_dataset.yaml")
		if err := o.runMemoryAgent(ctx, memoryAgentSpec{
			kind:       memoryKindReflect,
			stageName:  stage.Name,
			sources:    []string{stageDir},
			datasetOut: dataset,
			logFile:    filepath.Join(stageDir, "reflect.log"),
		}); err != nil {
			o.reflectFailed(stage.Name, memoryKindReflect, err)
			return
		}

		o.distill(ctx, stage.Name, []string{dataset}, stageDir, targetFile)
	})
}

// distill — код-конвейер aggregate → prioritize → (код) select-High → update,
// общий для per-stage reflection и для end-of-run прохода по всем датасетам.
// Сериализован reflectMu: одновременно бежит только один шаг записи в общие
// файлы памяти. Best-effort: ошибка любого шага прерывает конвейер и
// логируется нотисом, но не трогает FSM и не валит ран.
func (o *Orchestrator) distill(ctx context.Context, stageName string, datasets []string, logDir, targetFile string) {
	o.reflectMu.Lock()
	defer o.reflectMu.Unlock()

	patternsPath := filepath.Join(logDir, "patterns.md")
	if err := o.runMemoryAgent(ctx, memoryAgentSpec{
		kind:      memoryKindAggregate,
		stageName: stageName,
		inPaths:   datasets,
		out:       patternsPath,
		logFile:   filepath.Join(logDir, "aggregate.log"),
	}); err != nil {
		o.reflectFailed(stageName, memoryKindAggregate, err)
		return
	}

	prioritizedPath := filepath.Join(logDir, "prioritized.md")
	if err := o.runMemoryAgent(ctx, memoryAgentSpec{
		kind:      memoryKindPrioritize,
		stageName: stageName,
		in:        patternsPath,
		out:       prioritizedPath,
		logFile:   filepath.Join(logDir, "prioritize.log"),
	}); err != nil {
		o.reflectFailed(stageName, memoryKindPrioritize, err)
		return
	}

	data, err := os.ReadFile(prioritizedPath)
	if err != nil {
		o.reflectFailed(stageName, memoryKindPrioritize, err)
		return
	}
	high := memory.SelectHigh(string(data))
	if high == "" {
		o.reflectNotice(stageName, "no high-priority patterns")
		return
	}
	highPath := filepath.Join(logDir, "high.md")
	if err := memory.AtomicWrite(highPath, []byte(high)); err != nil {
		o.reflectFailed(stageName, memoryKindUpdate, err)
		return
	}

	if err := o.runMemoryAgent(ctx, memoryAgentSpec{
		kind:       memoryKindUpdate,
		stageName:  stageName,
		highPath:   highPath,
		targetFile: targetFile,
		maxRules:   o.opts.Memory.MaxRules,
		logFile:    filepath.Join(logDir, "update.log"),
	}); err != nil {
		o.reflectFailed(stageName, memoryKindUpdate, err)
		return
	}
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

// runEndOfRunMemory прогоняет конвейер памяти ОДИН раз по ВСЕМ датасетам рана
// (reflect_dataset.yaml каждой стадии, уже написанные во время рана) и
// сливает их в project-wide memory.md. Синхронный — Run() дожидается его
// перед завершением. Идемпотентен (finalReflectDone). No-op, если память
// выключена или ни одна стадия не оставила датасет.
func (o *Orchestrator) runEndOfRunMemory(ctx context.Context) {
	if o.finalReflectDone {
		return
	}
	if o.opts.MemoryDir == "" {
		return
	}
	o.finalReflectDone = true

	matches, _ := filepath.Glob(filepath.Join(o.opts.RunDir, "*", "reflect_dataset.yaml"))
	var datasets []string
	for _, m := range matches {
		if _, err := os.Stat(m); err == nil {
			datasets = append(datasets, m)
		}
	}
	if len(datasets) == 0 {
		return
	}

	o.distill(ctx, "flow-memory", datasets, o.opts.RunDir, memory.ProjectFile(o.opts.MemoryDir))

	if o.opts.Memory.Commit {
		if _, err := memory.Commit(o.opts.MemoryDir, "chore(memory): update project memory"); err != nil {
			o.reflectNotice("flow-memory", "memory commit failed: "+err.Error())
		}
	}
}
