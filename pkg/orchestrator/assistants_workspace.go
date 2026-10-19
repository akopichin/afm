package orchestrator

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/gitcmd"
)

// workspaceSection tells a helper where it works: the project root (its working directory, the same as the
// stage agent's), the revision to diff the project against, and its own scratch directory for notes. It
// promises no write protection: whether the helper can change project files depends on its environment
// (docs/collaboration.md), not on this text. A helper in an OS read-only sandbox (the host codex adapter runs
// `-s read-only`) cannot write its scratch directory either, and is told so instead — judged by the environment
// this turn runs in (currentReadonlyEnforcement), not by the admission snapshot.
func (o *Orchestrator) workspaceSection(task assistants.Task) string {
	root, err := filepath.Abs(o.fingerprintDir())
	if err != nil {
		root = o.fingerprintDir()
	}
	var b strings.Builder
	b.WriteString("## Workspace\n\n")
	fmt.Fprintf(&b, "- Project root (your working directory): `%s`\n", root)
	if base := diffBaseRevision(root); base != "" {
		fmt.Fprintf(&b, "- Base revision: `%s` — `git diff %s` shows the changes made on top of it, committed and not.\n", base, base)
	}
	scratch := o.taskScratchDir(task.ID)
	if o.currentReadonlyEnforcement(task) == string(assistants.ReadonlyEnforcementOS) {
		fmt.Fprintf(&b, "- Scratch directory: `%s` (also in `$AFM_SCRATCH_DIR`) — you run in a read-only sandbox and cannot write files there or anywhere else; put your notes in your answer.\n\n", scratch)
		return b.String()
	}
	fmt.Fprintf(&b, "- Scratch directory: `%s` (also in `$AFM_SCRATCH_DIR`) — keep your notes and temporary files there, not in the project.\n\n", scratch)
	return b.String()
}

// diffBaseRevision returns the commit to diff dir's checkout against, resolved to its id: gitcmd.DiffBaseRef,
// the same policy the dashboard's diff view uses — in a linked git worktree the branch's fork point, so
// committed branch work shows; otherwise HEAD. "" when dir is not a git checkout or git fails.
func diffBaseRevision(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), fingerprintTimeout)
	defer cancel()
	out, ok := gitCapture(ctx, dir, "rev-parse", "--path-format=absolute", "--git-dir")
	gitDir := strings.TrimSpace(out)
	if !ok || gitDir == "" {
		return ""
	}
	ref := gitcmd.DiffBaseRef(ctx, gitcmd.RunGitDir, gitDir, "")
	id, ok, err := gitcmd.RunGitDir(ctx, gitDir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil || !ok {
		return ""
	}
	return strings.TrimSpace(string(id))
}

// currentReadonlyEnforcement is the read-only guarantee THIS process's environment gives task's command: a task
// admitted on the host under the codex sandbox and resumed in a container has none. The turn start records it
// (state.TaskState.TurnReadonlyEnforcement) and the helper's prompt states it.
func (o *Orchestrator) currentReadonlyEnforcement(task assistants.Task) string {
	return config.HelperReadonlyEnforcement(task.ProfileSnapshot.Command, o.opts.Config)
}
