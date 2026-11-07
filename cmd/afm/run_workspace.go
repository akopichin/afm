package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	implstate "github.com/akopichin/afm-engine/engine/inspection/state"
	"github.com/akopichin/afm/contracts/config"
	orchestrator "github.com/akopichin/afm/contracts/control"
	"github.com/akopichin/afm/contracts/docker"
	"github.com/akopichin/afm/pkg/server/workspace"
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
func workspaceResolveFile(ws workspace.FS) func(root, path string, line *int) (orchestrator.ResolvedFile, bool) {
	return func(root, path string, line *int) (orchestrator.ResolvedFile, bool) {
		f, err := ws.Read(context.Background(), root, path)
		if err != nil {
			return orchestrator.ResolvedFile{}, false
		}
		rf := orchestrator.ResolvedFile{
			DisplayPath: f.DisplayPath,
			Reference:   f.Reference,
			ContentSHA:  implstate.FileContentSHA([]byte(f.Content)),
		}
		if line != nil {
			// Trim one trailing newline to count real lines; empty files have zero lines.
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
func workspaceCurrentFileSHA(ws workspace.FS) func(root, path string) (string, bool) {
	return func(root, path string) (string, bool) {
		f, err := ws.Read(context.Background(), root, path)
		if err != nil {
			return "", false
		}
		return implstate.FileContentSHA([]byte(f.Content)), true
	}
}
