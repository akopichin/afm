package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/docker"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/server/workspace"
	"github.com/akopichin/afm/pkg/state"
)

// The workspace is shared by review notes and the dashboard. An absent or
// unusable Docker manifest disables file browsing without failing the run.
func openRunWorkspace(dashboardEnabled bool) workspace.FS {
	raw := os.Getenv(docker.FileRootsEnvVar)
	if !dashboardEnabled || !config.ReExecedIntoContainer() || raw == "" {
		return nil
	}
	man, err := docker.DecodeFileRootManifest(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: file browser disabled: decode file root manifest: %v\n", err)
		return nil
	}
	roots := make([]workspace.Root, 0, len(man.Roots))
	for _, r := range man.Roots {
		roots = append(roots, workspace.Root{
			ID:            r.ID,
			Label:         r.Label,
			Path:          r.ContainerPath,
			Kind:          r.Kind,
			MountReadOnly: r.MountReadOnly,
		})
	}
	ws, err := workspace.New(roots)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: file browser disabled: open workspace: %v\n", err)
		return nil
	}
	if len(ws.Roots()) == 0 {
		_ = ws.Close()
		fmt.Fprintf(os.Stderr, "warning: file browser disabled: no roots could be opened (manifest had %d)\n", len(man.Roots))
		return nil
	}
	return ws
}

// workspaceResolveFile adapts a workspace.FS into orchestrator.Options.
// ResolveFile: it reads the file's full content through ws.Read (which also
// gives us DisplayPath/Reference for free, since Read embeds the same
// "[AFM file: ...]" marker Reference alone would), hashes it for AddNote's
// stale-content check, and — for a line-scoped note — slices out the
// requested 1-indexed line. Any workspace error (not found, too large,
// binary, symlink, ...) is reported as "can't resolve" rather than surfaced
// to the caller: AddNote already turns that into ErrStaleContent.
func workspaceResolveFile(ws workspace.FS) func(root, path string, line *int) (orchestrator.ResolvedFile, bool) {
	return func(root, path string, line *int) (orchestrator.ResolvedFile, bool) {
		f, err := ws.Read(context.Background(), root, path)
		if err != nil {
			return orchestrator.ResolvedFile{}, false
		}
		rf := orchestrator.ResolvedFile{
			DisplayPath: f.DisplayPath,
			Reference:   f.Reference,
			ContentSHA:  state.FileContentSHA([]byte(f.Content)),
		}
		if line != nil {
			// f.Content almost always ends in "\n" for a real source file —
			// a bare strings.Split would then produce a phantom trailing
			// empty element (Split("a\nb\n", "\n") == ["a","b",""]), so
			// len(lines) overcounts by one and a request for the line right
			// after the real last line wrongly reports InRange=true with an
			// empty LineText instead of InRange=false. Trim exactly one
			// trailing newline first so lines counts only real lines. An
			// empty file has 0 real lines (not the 1 a bare Split("", "\n")
			// would report), so a line-1 request against it correctly comes
			// back out of range.
			content := strings.TrimSuffix(f.Content, "\n")
			var lines []string
			if content != "" {
				lines = strings.Split(content, "\n")
			}
			if *line >= 1 && *line <= len(lines) {
				rf.InRange = true
				rf.LineText = lines[*line-1]
			}
		}
		return rf, true
	}
}

// workspaceCurrentFileSHA adapts a workspace.FS into orchestrator.Options.
// CurrentFileSHA: the same content-read path as workspaceResolveFile above,
// minus the line-splitting, used by renderReviewFeedback to detect drift
// between when a review note was taken and when it's injected.
func workspaceCurrentFileSHA(ws workspace.FS) func(root, path string) (string, bool) {
	return func(root, path string) (string, bool) {
		f, err := ws.Read(context.Background(), root, path)
		if err != nil {
			return "", false
		}
		return state.FileContentSHA([]byte(f.Content)), true
	}
}
