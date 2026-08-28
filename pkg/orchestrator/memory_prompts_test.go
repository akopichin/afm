package orchestrator

import (
	"strings"
	"testing"
)

func testPrompts() Prompts {
	return Prompts{Reflect: "REFLECT-BASE", Consolidator: "CONSOLIDATOR-BASE"}
}

func TestBuildMemoryPrompt_Reflect(t *testing.T) {
	got := buildMemoryPrompt(testPrompts(), memoryAgentSpec{
		kind:       "reflect",
		sources:    []string{"/run/s1/autonomous.log", "/run/s1/execution_summary.md"},
		datasetOut: "/run/s1/reflect_dataset.yaml",
	})
	for _, want := range []string{"REFLECT-BASE", "/run/s1/autonomous.log", "/run/s1/execution_summary.md", "/run/s1/reflect_dataset.yaml", "findings"} {
		if !strings.Contains(got, want) {
			t.Errorf("reflect prompt missing %q", want)
		}
	}
}

func TestBuildMemoryPrompt_Consolidator(t *testing.T) {
	got := buildMemoryPrompt(testPrompts(), memoryAgentSpec{
		kind:        "consolidator",
		datasetPath: "/run/s1/reflect_dataset.yaml",
		projectPath: "/proj/PROJECT_MEMORY.yaml",
		sessionPath: "/run/SESSION_MEMORY.yaml",
		outPath:     "/run/s1/consolidated.yaml",
	})
	for _, want := range []string{"CONSOLIDATOR-BASE", "/run/s1/reflect_dataset.yaml", "/proj/PROJECT_MEMORY.yaml", "/run/SESSION_MEMORY.yaml", "/run/s1/consolidated.yaml", "status"} {
		if !strings.Contains(got, want) {
			t.Errorf("consolidator prompt missing %q", want)
		}
	}
}
