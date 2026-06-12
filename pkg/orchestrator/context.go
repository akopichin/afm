package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/akopichin/afm/pkg/flow"
)

// CollectDependencyPlans reads plan.md from each stage in DependsOn
// and returns a formatted prompt section. Missing plans produce a warning comment.
func CollectDependencyPlans(runDir string, stage flow.Stage, allStages []flow.Stage) string {
	if len(stage.DependsOn) == 0 {
		return ""
	}

	nameIndex := make(map[string]string, len(allStages))
	for _, s := range allStages {
		nameIndex[s.ID] = s.Name
	}

	var buf strings.Builder
	buf.WriteString("\n\n## Context from dependent stages\n")

	for _, depID := range stage.DependsOn {
		planPath := filepath.Join(runDir, depID, "plan.md")
		data, err := os.ReadFile(planPath)
		name := nameIndex[depID]
		if name == "" {
			name = depID
		}
		fmt.Fprintf(&buf, "\n### Stage: %s (%s)\n\n", name, depID)
		if err != nil {
			buf.WriteString("(plan not available)\n")
			continue
		}
		buf.WriteString(string(data))
		buf.WriteString("\n")
	}

	return buf.String()
}

// resolveArtifactPath resolves an artifact path to an absolute file path.
// Paths starting with "./" are relative to the stage's run directory.
// All other paths are relative to the project directory.
func resolveArtifactPath(projectDir, runDir, stageID, artifactPath string) string {
	if strings.HasPrefix(artifactPath, "./") {
		return filepath.Join(runDir, stageID, artifactPath[2:])
	}
	return filepath.Join(projectDir, artifactPath)
}

// CollectArtifacts reads artifact files referenced by a stage's Inputs
// and returns a formatted prompt section. Returns an error if a required
// artifact file is missing.
func CollectArtifacts(projectDir, runDir string, stage flow.Stage, allStages []flow.Stage) (string, error) {
	if len(stage.Inputs) == 0 {
		return "", nil
	}

	// Build index: stageID -> artifactName -> Artifact
	artIndex := make(map[string]map[string]flow.Artifact, len(allStages))
	for _, s := range allStages {
		m := make(map[string]flow.Artifact, len(s.Artifacts))
		for _, a := range s.Artifacts {
			m[a.Name] = a
		}
		artIndex[s.ID] = m
	}

	var buf strings.Builder
	buf.WriteString("\n\n## Artifacts\n")
	hasContent := false

	for _, inp := range stage.Inputs {
		parts := strings.SplitN(inp.Ref, ".", 2)
		if len(parts) < 2 {
			return "", fmt.Errorf("invalid input ref %q: expected format stageID.artifactName", inp.Ref)
		}
		stageID, artName := parts[0], parts[1]
		stageArts, stageOk := artIndex[stageID]
		if !stageOk {
			return "", fmt.Errorf("unknown stage %q in input ref %q", stageID, inp.Ref)
		}
		art, artOk := stageArts[artName]
		if !artOk {
			return "", fmt.Errorf("artifact %q not declared in stage %q", artName, stageID)
		}

		resolved := resolveArtifactPath(projectDir, runDir, stageID, art.Path)

		if art.IsInline() {
			data, err := os.ReadFile(resolved)
			if err != nil {
				if inp.Optional {
					continue
				}
				return "", fmt.Errorf("required artifact %q (stage %q): %w", artName, stageID, err)
			}
			fmt.Fprintf(&buf, "\n### %s (from %s): %s\n\n", artName, stageID, art.Description)
			buf.Write(data)
			buf.WriteString("\n")
			hasContent = true
		} else {
			if _, err := os.Stat(resolved); err != nil {
				if inp.Optional {
					continue
				}
				return "", fmt.Errorf("required artifact %q (stage %q): %w", artName, stageID, err)
			}
			fmt.Fprintf(&buf, "\n### %s (from %s): %s\n\nFile path: %s\n(Use Read tool to access this file)\n", artName, stageID, art.Description, resolved)
			hasContent = true
		}
	}

	if !hasContent {
		return "", nil
	}
	return buf.String(), nil
}
