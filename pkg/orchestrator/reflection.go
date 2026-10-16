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
)

// flowMemoryLabel — имя, под которым пайплайн памяти помечает шаги/нотисы, относящиеся к общей
// project-wide памяти.
const flowMemoryLabel = "flow-memory"

// maybeRunReflection в фоне ТОЛЬКО каптурит сессию завершившейся стадии в reflect_dataset.yaml.
// Best-effort: НИКОГДА не трогает FSM.
func (o *Orchestrator) maybeRunReflection(ctx context.Context, stageID string) {
	if o.opts.MemoryDir == "" {
		return
	}
	stage := o.reg().Stage(stageID)
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
	o.publishNotice(bus.Event{Type: bus.EventReflectFailed, Data: data})
}

// eligibleMemoryDatasets returns captured datasets from configured write-reflect stages; stale directories
// are excluded.
func (o *Orchestrator) eligibleMemoryDatasets() ([]flow.Stage, []memorypipeline.DatasetResult) {
	var stages []flow.Stage
	var datasets []memorypipeline.DatasetResult
	for _, s := range o.reg().Stages() {
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

// runEndOfRunMemory прогоняет конвейер памяти ОДИН раз по ВСЕМ eligible- датасетам рана через
// memorypipeline.Pipeline.Finalize — под межпроцессным memory-lock плюс внутрипроцессный reflectMu.
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

	// A unique staging dir per finalize attempt — NOT a reused stage dir, so Finalize's own freshness checks
	// can never mistake a stale leftover from a prior attempt for this attempt's real output.
	workDir, err := memorypipeline.NewUniqueAttemptDir(filepath.Join(o.opts.RunDir, ".memory-finalize"), newOperationID)
	if err != nil {
		o.reflectFailed(flowMemoryLabel, "workdir", err)
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
		// The live path publishes directly without an offline-rebuild manifest.
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
