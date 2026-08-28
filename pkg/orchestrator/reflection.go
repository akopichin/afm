package orchestrator

import (
	"context"
	"path/filepath"

	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
)

// memoryKindReflect/Updater/Compressor — значения memoryAgentSpec.kind,
// общие с switch в buildMemoryPrompt (memory_prompts.go) — единые константы,
// а не разбросанные строковые литералы (goconst).
const (
	memoryKindReflect    = "reflect"
	memoryKindUpdater    = "updater"
	memoryKindCompressor = "compressor"
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

// runReflectionPipeline — reflect → updater → size-check/compress-loop.
// Сериализован reflectMu. logDir — куда класть reflect.log/updater.log/…
// и reflect_dataset.yaml. Best-effort: любая ошибка шага прерывает конвейер
// и логируется нотисом, но не трогает FSM и не валит ран.
func (o *Orchestrator) runReflectionPipeline(ctx context.Context, stageName string, sources []string, logDir string) {
	o.reflectMu.Lock()
	defer o.reflectMu.Unlock()

	datasetOut := filepath.Join(logDir, "reflect_dataset.yaml")

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
		kind:        memoryKindUpdater,
		stageName:   stageName,
		datasetPath: datasetOut,
		projectPath: o.opts.MemoryProjectPath,
		sessionPath: o.opts.MemorySessionPath,
		logFile:     filepath.Join(logDir, "updater.log"),
	}); err != nil {
		o.reflectFailed(stageName, memoryKindUpdater, err)
		return
	}

	for _, target := range []string{o.opts.MemoryProjectPath, o.opts.MemorySessionPath} {
		o.compressIfNeeded(ctx, stageName, target, logDir)
	}
}

// compressIfNeeded гоняет компрессор до compress_retries раз; на последней
// попытке добавляет динамический line-limit; если всё ещё превышен —
// FIFO-выброс старых блоков + warning-нотис.
func (o *Orchestrator) compressIfNeeded(ctx context.Context, stageName, target, logDir string) {
	maxBytes := o.opts.Memory.MaxBytes
	if !fileExceeds(target, maxBytes) {
		return
	}
	base := filepath.Base(target)
	for attempt := 0; attempt < o.opts.Memory.CompressRetries; attempt++ {
		limit := 0
		if attempt == o.opts.Memory.CompressRetries-1 {
			limit = lineLimitForBytes(maxBytes) // экстремальный проход на последней попытке
		}
		if err := o.runMemoryAgent(ctx, memoryAgentSpec{
			kind:       memoryKindCompressor,
			stageName:  stageName,
			targetFile: target,
			lineLimit:  limit,
			logFile:    filepath.Join(logDir, "compressor-"+base+".log"),
		}); err != nil {
			o.reflectFailed(stageName, memoryKindCompressor, err)
			break
		}
		if !fileExceeds(target, maxBytes) {
			return
		}
	}
	if fileExceeds(target, maxBytes) {
		_ = fifoDropOldestBlocks(target, maxBytes)
		o.reflectNotice(stageName, "memory file "+base+" still over limit after compression; dropped oldest blocks")
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

// initSessionMemory сбрасывает SESSION_MEMORY.md в свежий stub на старте рана
// (пер-ран скоуп: предыдущий ран не переносится). No-op, если память выключена.
func (o *Orchestrator) initSessionMemory() {
	if o.opts.MemoryProjectPath == "" || o.opts.MemorySessionPath == "" {
		return
	}
	_ = atomicWriteFile(o.opts.MemorySessionPath, []byte("# SESSION MEMORY\n"))
}
