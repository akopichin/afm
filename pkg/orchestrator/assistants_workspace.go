package orchestrator

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/akopichin/afm/pkg/assistants"
)

// workspaceSection tells a helper where it works: the project root (its working directory, the same as the
// stage agent's), the revision to diff the project against, and its own scratch directory for notes. It
// promises no write protection: whether the helper can change project files depends on its environment
// (docs/collaboration.md), not on this text. A helper in an OS read-only sandbox (the host codex adapter runs
// `-s read-only`) cannot write its scratch directory either, and is told so instead.
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
	if task.ProfileSnapshot.ReadonlyEnforcement == assistants.ReadonlyEnforcementOS {
		fmt.Fprintf(&b, "- Scratch directory: `%s` (also in `$AFM_SCRATCH_DIR`) — you run in a read-only sandbox and cannot write files there or anywhere else; put your notes in your answer.\n\n", scratch)
		return b.String()
	}
	fmt.Fprintf(&b, "- Scratch directory: `%s` (also in `$AFM_SCRATCH_DIR`) — keep your notes and temporary files there, not in the project.\n\n", scratch)
	return b.String()
}

// diffBaseRevision returns the commit to diff dir's checkout against: in a linked git worktree, the branch's
// fork point — merge-base of its HEAD and the main worktree's HEAD — so committed branch work shows, the same
// baseline the dashboard's diff view uses; in an ordinary checkout, HEAD. "" when dir is not a git checkout or
// git fails.
func diffBaseRevision(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), fingerprintTimeout)
	defer cancel()
	git := func(at string, args ...string) string {
		out, ok := gitCapture(ctx, at, args...)
		if !ok {
			return ""
		}
		return strings.TrimSpace(out)
	}
	head := git(dir, "rev-parse", "HEAD")
	if head == "" {
		return ""
	}
	gitDir := git(dir, "rev-parse", "--path-format=absolute", "--git-dir")
	commonDir := git(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if gitDir == "" || commonDir == "" || filepath.Clean(gitDir) == filepath.Clean(commonDir) {
		return head // an ordinary checkout, not a linked worktree
	}
	mainHead := git(commonDir, "rev-parse", "HEAD")
	if mainHead == "" {
		return head
	}
	if base := git(dir, "merge-base", "HEAD", mainHead); base != "" {
		return base
	}
	return head
}
