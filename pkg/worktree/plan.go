// Package worktree manages per-flow git worktrees: where they live on disk,
// validating flow names as safe branch/path identifiers, and parsing
// `git worktree list --porcelain` output.
package worktree

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Plan is where a flow's worktree lives.
type Plan struct {
	Base   string // the project root the worktree is anchored to (absolute)
	Dir    string // <Base>/.worktrees
	Path   string // <Base>/.worktrees/<flow>
	Branch string // <flow>
}

// PlanFor validates flowName as both a git branch name and a single safe path
// component, and returns where its worktree belongs under base.
//
// flowName ends up both as a git branch name and as a single path component
// under <base>/.worktrees, so it must satisfy both constraints at once. This
// mirrors the stageIDSafe / lifecycle-hook-id convention used elsewhere in
// this repo: a conservative allowlist beats trying to replicate the full
// `git check-ref-format` rule set.
func PlanFor(base, flowName string) (Plan, error) {
	if err := validateFlowName(flowName); err != nil {
		return Plan{}, err
	}
	dir := filepath.Join(base, ".worktrees")
	return Plan{
		Base:   base,
		Dir:    dir,
		Path:   filepath.Join(dir, flowName),
		Branch: flowName,
	}, nil
}

// validateFlowName quotes the rejected name with %s, not %q: %q escapes
// control bytes (e.g. NUL), which would stop the error from echoing the
// rejected name verbatim — callers match on the raw name appearing in the
// message.
func validateFlowName(name string) error {
	if name == "" {
		return fmt.Errorf("worktree: flow name '%s' is empty", name)
	}
	if name == "." || name == ".." {
		return fmt.Errorf("worktree: flow name '%s' is not a valid path component", name)
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("worktree: flow name '%s' must not contain '..'", name)
	}
	if strings.HasSuffix(name, ".lock") {
		return fmt.Errorf("worktree: flow name '%s' must not end in '.lock'", name)
	}
	first := rune(name[0])
	if first == '-' || first == '.' {
		return fmt.Errorf("worktree: flow name '%s' must not start with '-' or '.'", name)
	}
	if strings.HasSuffix(name, ".") {
		return fmt.Errorf("worktree: flow name '%s' must not end with '.'", name)
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return fmt.Errorf("worktree: flow name '%s' contains invalid character %q (allowed: letters, digits, '.', '_', '-')", name, r)
		}
	}
	return nil
}
