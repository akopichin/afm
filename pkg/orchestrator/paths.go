package orchestrator

import (
	"context"
	"path/filepath"
)

// runContext returns the long-lived run-scoped context for spawning agents from HTTP-initiated actions.
func (o *Orchestrator) runContext(fallback context.Context) context.Context {
	o.runMu.Lock()
	defer o.runMu.Unlock()
	if o.runCtx != nil {
		return o.runCtx
	}
	return context.WithoutCancel(fallback)
}

// projectDir keeps shell verification and tree fingerprints on the agent project root (worktree or root_dir).
// Without an explicit root they use afm's working directory.
func (o *Orchestrator) projectDir() string {
	if o.opts.RootDir != "" {
		return o.opts.RootDir
	}
	return "."
}

// stageDir — каталог стадии АБСОЛЮТНЫМ путём. Нужен там, где путь пересекает границу процесса: CWD
// агента (root_dir/worktree) не совпадает с CWD afm, и относительный путь резолвился бы не туда.
func (o *Orchestrator) stageDir(stageID string) string {
	return filepath.Join(o.absRunDir, stageID)
}

// stageDirFor returns the on-disk directory for one stage's dialog/feedback files.
func (o *Orchestrator) stageDirFor(stageID string) string {
	return filepath.Join(o.opts.RunDir, stageID)
}
