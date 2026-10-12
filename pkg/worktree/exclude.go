package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/akopichin/afm/pkg/gitcmd"
)

// excludeEntry is the line EnsureExcluded guarantees is present in
// info/exclude.
const excludeEntry = ".worktrees/"

// EnsureExcluded appends ".worktrees/" to <git-common-dir>/info/exclude unless an
// exact entry is already there. info/exclude is local and never committed, so afm
// does not touch the user's .gitignore.
//
// An existing line counts as "already present" (and is not duplicated) when,
// after trimming surrounding whitespace, it equals either ".worktrees/" or
// ".worktrees" (no trailing slash).
func EnsureExcluded(ctx context.Context, base string) error {
	gitDir, err := gitCommonDir(ctx, base)
	if err != nil {
		return err
	}

	infoDir := filepath.Join(gitDir, "info")
	if err := os.MkdirAll(infoDir, 0o755); err != nil {
		return fmt.Errorf("worktree: create %s: %w", infoDir, err)
	}

	excludePath := filepath.Join(infoDir, "exclude")
	existing, err := os.ReadFile(excludePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("worktree: read %s: %w", excludePath, err)
	}

	if alreadyExcluded(existing) {
		return nil
	}

	var buf bytes.Buffer
	buf.Write(existing)
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		buf.WriteByte('\n')
	}
	buf.WriteString(excludeEntry)
	buf.WriteByte('\n')

	// 0644: info/exclude is a plain text file, not a secret.
	if err := os.WriteFile(excludePath, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("worktree: write %s: %w", excludePath, err)
	}
	return nil
}

// alreadyExcluded reports whether content already has a line that is exactly
// ".worktrees/" or ".worktrees" once trimmed of surrounding whitespace.
func alreadyExcluded(content []byte) bool {
	for _, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == excludeEntry || trimmed == strings.TrimSuffix(excludeEntry, "/") {
			return true
		}
	}
	return false
}

// gitCommonDir resolves the shared .git directory for base, using
// --git-common-dir (not --git-dir) so the result is correct even when base is
// itself inside a linked worktree. Routed through gitcmd.Run so an ambient
// GIT_DIR/GIT_COMMON_DIR can't redirect this lookup away from base.
func gitCommonDir(ctx context.Context, base string) (string, error) {
	out, ok, err := gitcmd.Run(ctx, base, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("worktree: git rev-parse --git-common-dir: %w", err)
	}
	if !ok {
		return "", errors.New("worktree: git rev-parse --git-common-dir failed")
	}
	dir := strings.TrimSpace(out)
	if dir == "" {
		return "", errors.New("worktree: git rev-parse --git-common-dir returned empty output")
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(base, dir)
	}
	return dir, nil
}
