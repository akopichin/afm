// Package orchestrator (this file, T3.6): the on-disk file namespace for one helper task —
//
//	.afm/runs/<run_id>/assistants/<task_id>/
//	    task.json        # projection cache (not authoritative — events.jsonl is)
//	    context.md        # the context actually handed to the helper (T3.5's buildTaskContext output)
//	    result.json        # the accepted result, once completed
//	    result.json.raw    # the model's raw final-turn text, cleared before each turn (see rawResultFileName)
//	    turn-inbox.json    # <inbox> message ids of a turn that has not finished yet (turnInboxFileName)
//	    agent.log          # human-readable log
//	    agent.jsonl        # raw stream
//	    scratch/           # the ONLY writable directory given to the helper
//
// Anchored to the run directory (RunDir) regardless of root_dir — same discipline as AFM_STAGE_DIR: the
// agent's actual project-root CWD can be anywhere, but its OWN bookkeeping files always live under the run.
package orchestrator

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"

	"github.com/akopichin/afm/pkg/assistants"
)

const assistantsNamespaceDir = "assistants"
const taskScratchDirName = "scratch"

// rawResultFileName — the single literal for a task's raw-answer file, shared by runHelperTask (which clears
// and derives its path from it) and TaskRawResultRelPath (which pkg/server reads it through).
const rawResultFileName = "result.json.raw"

// TaskRawResultRelPath returns the run-dir-relative path of taskID's raw answer file — for pkg/server's
// GET /api/assistants/{id}/raw, which must resolve the path WITHOUT importing orchestrator's full Orchestrator
// (it only has RunDir).
func TaskRawResultRelPath(taskID string) string {
	return filepath.Join(assistantsNamespaceDir, safeTaskDirName(taskID), rawResultFileName)
}

// taskIDSafe mirrors pkg/assistants' internal profileIDSafe charset: a task id becomes a directory name
// under RunDir, so the same restrictive [A-Za-z0-9._-] set applies, rejecting empty, ".", "..", path
// separators, NUL, and anything else that could traverse out of the namespace.
func taskIDSafe(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// safeTaskDirName returns a path component that is ALWAYS safe to join under RunDir/assistants/, regardless
// of what taskID actually contains. taskID is always server-generated (deriveTaskID: "t-"+20 hex chars) and
// therefore always already safe in practice — this is defense in depth, not a path a legitimate caller
// should ever take: if an unsafe id is ever fed in anyway (a future bug), it is NEVER embedded directly into
// a path (that would let ".."/"/"/NUL escape the namespace) — it is hashed into a safe synthetic name
// instead, so the result always stays confined under the one namespace directory.
func safeTaskDirName(id string) string {
	if taskIDSafe(id) {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return "unsafe-" + hex.EncodeToString(sum[:])[:20]
}

// taskDir returns the file namespace root for one helper task.
func (o *Orchestrator) taskDir(taskID assistants.TaskID) string {
	return filepath.Join(o.opts.RunDir, assistantsNamespaceDir, safeTaskDirName(string(taskID)))
}

// taskScratchDir returns the ONE writable directory a helper task's subprocess is given.
func (o *Orchestrator) taskScratchDir(taskID assistants.TaskID) string {
	return filepath.Join(o.taskDir(taskID), taskScratchDirName)
}

// ensureTaskWorkspace creates the task's own directory AND its scratch/ subdirectory (MkdirAll covers both
// in one call, since scratch is nested under dir) — the single call site every task run starts from.
func ensureTaskWorkspace(dir, scratchDir string) error {
	if err := os.MkdirAll(scratchDir, 0o755); err != nil {
		return err
	}
	// Belt-and-braces: MkdirAll(scratchDir) already implies dir exists, but keep the explicit call so this
	// function's contract ("both exist after a successful return") does not depend on scratch always being a
	// child of dir if that ever changes.
	return os.MkdirAll(dir, 0o755)
}
