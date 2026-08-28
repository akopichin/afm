package orchestrator

import (
	"strings"
	"testing"
)

func testPrompts() Prompts {
	return Prompts{Reflect: "REFLECT-BASE", Updater: "UPDATER-BASE", Compressor: "COMPRESS-BASE"}
}

func TestBuildMemoryPrompt_Reflect(t *testing.T) {
	got := buildMemoryPrompt(testPrompts(), memoryAgentSpec{
		kind:       "reflect",
		sources:    []string{"/run/s1/autonomous.log", "/run/s1/execution_summary.md"},
		datasetOut: "/run/s1/reflect_dataset.yaml",
	})
	for _, want := range []string{"REFLECT-BASE", "/run/s1/autonomous.log", "/run/s1/execution_summary.md", "/run/s1/reflect_dataset.yaml"} {
		if !strings.Contains(got, want) {
			t.Errorf("reflect prompt missing %q", want)
		}
	}
}

func TestBuildMemoryPrompt_Updater(t *testing.T) {
	got := buildMemoryPrompt(testPrompts(), memoryAgentSpec{
		kind:        "updater",
		datasetPath: "/run/s1/reflect_dataset.yaml",
		projectPath: "/proj/PROJECT_MEMORY.md",
		sessionPath: "/run/SESSION_MEMORY.md",
	})
	for _, want := range []string{"UPDATER-BASE", "/run/s1/reflect_dataset.yaml", "/proj/PROJECT_MEMORY.md", "/run/SESSION_MEMORY.md"} {
		if !strings.Contains(got, want) {
			t.Errorf("updater prompt missing %q", want)
		}
	}
}

func TestBuildMemoryPrompt_CompressorPlain(t *testing.T) {
	got := buildMemoryPrompt(testPrompts(), memoryAgentSpec{
		kind:       "compressor",
		targetFile: "/proj/PROJECT_MEMORY.md",
	})
	if !strings.Contains(got, "COMPRESS-BASE") || !strings.Contains(got, "/proj/PROJECT_MEMORY.md") {
		t.Error("compressor prompt missing base or path")
	}
	if strings.Contains(got, "CRITICAL: reduce") {
		t.Error("plain compressor must not contain the line-limit tail")
	}
}

func TestBuildMemoryPrompt_CompressorExtreme(t *testing.T) {
	got := buildMemoryPrompt(testPrompts(), memoryAgentSpec{
		kind:       "compressor",
		targetFile: "/proj/PROJECT_MEMORY.md",
		lineLimit:  40,
	})
	if !strings.Contains(got, "CRITICAL: reduce") || !strings.Contains(got, "40") {
		t.Error("extreme compressor must contain the dynamic line-limit tail with N=40")
	}
}
