package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/memory"
	"github.com/akopichin/afm/pkg/memorypipeline"
	"github.com/akopichin/afm/pkg/orchestrator/bus"
	"github.com/akopichin/afm/pkg/orchestrator/stagefiles"
)

// flowMemoryLabel — имя, под которым пайплайн памяти помечает шаги/нотисы,
// относящиеся к общей project-wide памяти (а не к конкретной стадии). Общая
// константа, а не разбросанные строковые литералы (goconst) — используется в
// нотисах runEndOfRunMemory ниже; memorypipeline держит свой собственный
// (неэкспортируемый) аналог для меток целей внутри Finalize.
const flowMemoryLabel = "flow-memory"

// maybeRunReflection в фоне ТОЛЬКО каптурит сессию завершившейся стадии в
// reflect_dataset.yaml (memorypipeline.Pipeline.CaptureStage, запись сразу в
// канонический путь — офлайн-каптура через staging здесь не нужна, см. Task
// 10 brief review #3). Сама сборка памяти (aggregate → prioritize → update)
// перенесена в конец рана (runEndOfRunMemory): после каждой стадии ничего не
// строится — так память видит все стадии вместе, а сборка бежит один раз, а не
// N раз по ходу флоу. No-op, если память выключена, стадия без reflect (или
// reflect без write), или это script-стадия (нет агентской сессии).
// Best-effort: НИКОГДА не трогает FSM.
func (o *Orchestrator) maybeRunReflection(ctx context.Context, stageID string) {
	if o.opts.MemoryDir == "" {
		return
	}
	stage := o.graph.Stage(stageID)
	if stage == nil || stage.Reflect == nil || !stage.Reflect.CanWrite() || stage.IsScript() {
		return
	}
	stageDir := filepath.Join(o.opts.RunDir, stageID)
	canonical := memory.StageFile(o.opts.RunDir, filepath.Join(stageID, "reflect_dataset.yaml"))

	o.pendingReflections.Add(1)
	o.concurrency.SpawnDetached(ctx, func(ctx context.Context) {
		defer func() {
			o.pendingReflections.Add(-1)
			o.concurrency.WakeEventLoop()
		}()

		if _, err := o.mem.CaptureStage(ctx, *stage, stageDir, canonical, stageDir, false); err != nil {
			o.reflectFailed(stage.Name, memorypipeline.KindReflect, err)
		}
	})
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

// eligibleMemoryDatasets returns the write-reflect, non-script stages whose
// reflect_dataset.yaml was actually captured on disk during this run (see
// maybeRunReflection above), paired with the memorypipeline.DatasetResult
// Finalize needs for each. Built EXPLICITLY from o.opts.Stages rather than a
// filepath.Glob over the run dir (review #13 in Task 10 brief) — a stale
// reflect_dataset.yaml left in a stage dir whose stage was since removed from
// flow.yaml (or that never wrote a valid dataset this run) never enters the
// project aggregate. Source is "reused": CaptureStage already wrote the
// dataset directly to its canonical path (datasetOut == canonical), so
// Finalize's dataset-publish step has nothing further to promote.
func (o *Orchestrator) eligibleMemoryDatasets() ([]flow.Stage, []memorypipeline.DatasetResult) {
	var stages []flow.Stage
	var datasets []memorypipeline.DatasetResult
	for _, s := range o.opts.Stages {
		if s.Reflect == nil || !s.Reflect.CanWrite() || s.IsScript() {
			continue
		}
		dataset := filepath.Join(o.opts.RunDir, s.ID, "reflect_dataset.yaml")
		if _, err := os.Stat(dataset); err != nil {
			continue
		}
		stages = append(stages, s)
		datasets = append(datasets, memorypipeline.DatasetResult{
			StageID:       s.ID,
			Source:        "reused",
			DatasetPath:   dataset,
			CanonicalPath: dataset,
		})
	}
	return stages, datasets
}

// runEndOfRunMemory прогоняет конвейер памяти ОДИН раз по ВСЕМ eligible-
// датасетам рана (reflect_dataset.yaml каждой write-reflect стадии, уже
// написанные во время рана) через memorypipeline.Pipeline.Finalize — под
// межпроцессным memory-lock (см. AcquireMemoryLock) плюс внутрипроцессный
// reflectMu (на случай двух горутин в одном процессе — Finalize сам считает,
// что лок уже взят вызывающим). Синхронный — Run() дожидается его перед
// завершением. Идемпотентен (finalReflectDone). No-op, если память выключена
// или ни одна стадия не оставила eligible-датасет. Best-effort: ошибка любого
// шага — нотис, FSM не трогается.
func (o *Orchestrator) runEndOfRunMemory(ctx context.Context) {
	if o.finalReflectDone {
		return
	}
	if o.opts.MemoryDir == "" {
		return
	}
	o.finalReflectDone = true

	stages, datasets := o.eligibleMemoryDatasets()
	if len(datasets) == 0 {
		return
	}

	o.reflectMu.Lock()
	defer o.reflectMu.Unlock()

	lock, err := memorypipeline.AcquireMemoryLock(ctx, o.opts.MemoryDir)
	if err != nil {
		o.reflectFailed(flowMemoryLabel, "memory-lock", err)
		return
	}
	defer lock.Close()

	// A unique staging dir per finalize attempt — NOT a reused stage dir, so
	// Finalize's own freshness checks (requireFreshFile inside DistillTarget)
	// can never mistake a stale leftover from a prior attempt for this
	// attempt's real output.
	workDir, err := memorypipeline.NewUniqueAttemptDir(filepath.Join(o.opts.RunDir, ".memory-finalize"), newOperationID)
	if err != nil {
		o.reflectFailed(flowMemoryLabel, "memory-lock", err)
		return
	}

	_, err = o.mem.Finalize(ctx, memorypipeline.FinalizeRequest{
		Meta: memorypipeline.OperationMeta{
			RunID:      filepath.Base(o.opts.RunDir),
			RunPath:    o.opts.RunDir,
			RootDir:    o.opts.RootDir,
			MemoryDir:  o.opts.MemoryDir,
			MemoryMode: o.opts.Memory.Mode,
			MaxRules:   o.opts.Memory.MaxRules,
			StartedAt:  time.Now().Format(time.RFC3339),
		},
		RunDir: o.opts.RunDir,
		// WorkDir — a fresh attempt dir (see above). MemoryDir — the real
		// project memory dir the caller (this method) already holds locked.
		WorkDir:   workDir,
		MemoryDir: o.opts.MemoryDir,
		Stages:    stages,
		Memory:    o.opts.Memory,
		Datasets:  datasets,
		// DryRun:false, ManifestPath:"" — the live path publishes directly and
		// writes no manifest (that's an offline-rebuild artifact, Task 11).
	})
	if err != nil {
		o.reflectFailed(flowMemoryLabel, "finalize", err)
		return
	}

	if o.opts.Memory.Commit {
		if _, err := memory.Commit(o.opts.MemoryDir, "chore(memory): update project memory"); err != nil {
			o.reflectNotice(flowMemoryLabel, "memory commit failed: "+err.Error())
		}
	}
}
