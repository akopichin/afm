package orchestrator

import (
	"fmt"
	"os"
	"strings"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/memory"
)

// memoryBlockForStage computes the pointer block to splice into the agent's
// prompt (prompts.Inputs.MemoryBlock). Pointer-based, per §4 of the v3 design:
// the project-wide memory.md (if it exists) is pointed at from EVERY stage;
// the stage's own reflect file is additionally pointed at only when its mode
// allows reading (r/rw) and the file already exists on disk. "" is a valid,
// expected result — memory disabled, or nothing has been written yet.
func (o *Orchestrator) memoryBlockForStage(s flow.Stage) string {
	if o.opts.MemoryDir == "" {
		return ""
	}
	// Participation gate (v3): per-stage memory_use override if set, else the
	// global memory.memory_use (default false). false → nothing is injected.
	if !o.opts.Memory.UseFor(s.MemoryUse) {
		return ""
	}

	var paths []string
	// Project-wide memory.md is injected only when memory.mode allows reading
	// (r/rw). memory.mode governs the global file; Reflect.Mode governs the
	// stage's own file below.
	if o.opts.Memory.CanReadProject() {
		if projectFile := memory.ProjectFile(o.opts.MemoryDir); fileExists(projectFile) {
			paths = append(paths, projectFile)
		}
	}
	if s.Reflect != nil && s.Reflect.CanRead() {
		if stageFile := memory.StageFile(o.opts.MemoryDir, s.Reflect.File); fileExists(stageFile) {
			paths = append(paths, stageFile)
		}
	}
	if len(paths) == 0 {
		return ""
	}
	return memoryPointerBlock(paths)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// memoryPointerBlock names the given memory files and instructs the agent to
// read them before starting — the agent reads them itself via its own Read
// tool rather than the prompt growing with the memory content.
func memoryPointerBlock(paths []string) string {
	var b strings.Builder
	b.WriteString("Project memory lives in these files (Markdown \"# Project rules\" with \"## Pattern\" blocks). Read them before you start and follow the rules:\n")
	for _, p := range paths {
		fmt.Fprintf(&b, "  - %s\n", p)
	}
	return strings.TrimRight(b.String(), "\n")
}
