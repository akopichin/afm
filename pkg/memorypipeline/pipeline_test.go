package memorypipeline

import (
	"strings"
	"testing"
)

func testPrompts() Prompts {
	return Prompts{
		Reflect:    "REFLECT-BASE",
		Aggregate:  "AGGREGATE-BASE",
		Prioritize: "PRIORITIZE-BASE",
		Update:     "UPDATE-BASE for <FILEPATH>, cap <MAX_RULES>",
	}
}

func TestBuildMemoryPrompt_Reflect(t *testing.T) {
	got := BuildPrompt(testPrompts(), AgentSpec{
		Kind:       "reflect",
		Sources:    []string{"/run/s1/autonomous.log", "/run/s1/execution_summary.md"},
		DatasetOut: "/run/s1/reflect_dataset.yaml",
	})
	for _, want := range []string{"REFLECT-BASE", "/run/s1/autonomous.log", "/run/s1/execution_summary.md", "/run/s1/reflect_dataset.yaml"} {
		if !strings.Contains(got, want) {
			t.Errorf("reflect prompt missing %q", want)
		}
	}
	// Диалоги и заметки пользователя тоже подаются в обработку reflect-агенту —
	// файловая инструкция должна называть их явно.
	for _, want := range []string{"dialog.jsonl", "prenote.md", "feedback.md"} {
		if !strings.Contains(got, want) {
			t.Errorf("reflect prompt must include user dialog/notes source %q", want)
		}
	}
}

func TestBuildMemoryPrompt_Aggregate(t *testing.T) {
	got := BuildPrompt(testPrompts(), AgentSpec{
		Kind:    "aggregate",
		InPaths: []string{"/run/s1/reflect_dataset.yaml", "/run/s2/reflect_dataset.yaml"},
		Out:     "/run/s1/patterns.md",
	})
	for _, want := range []string{"AGGREGATE-BASE", "/run/s1/reflect_dataset.yaml", "/run/s2/reflect_dataset.yaml", "/run/s1/patterns.md"} {
		if !strings.Contains(got, want) {
			t.Errorf("aggregate prompt missing %q", want)
		}
	}
}

func TestBuildMemoryPrompt_Prioritize(t *testing.T) {
	got := BuildPrompt(testPrompts(), AgentSpec{
		Kind: "prioritize",
		In:   "/run/s1/patterns.md",
		Out:  "/run/s1/prioritized.md",
	})
	for _, want := range []string{"PRIORITIZE-BASE", "/run/s1/patterns.md", "/run/s1/prioritized.md"} {
		if !strings.Contains(got, want) {
			t.Errorf("prioritize prompt missing %q", want)
		}
	}
}

func TestBuildMemoryPrompt_Update(t *testing.T) {
	got := BuildPrompt(testPrompts(), AgentSpec{
		Kind:       "update",
		HighPath:   "/run/s1/high.md",
		TargetFile: "/proj/mem/s1.md",
		MaxRules:   25,
	})
	if strings.Contains(got, "<FILEPATH>") || strings.Contains(got, "<MAX_RULES>") {
		t.Errorf("update prompt must substitute template placeholders:\n%s", got)
	}
	for _, want := range []string{"UPDATE-BASE for /proj/mem/s1.md, cap 25", "/run/s1/high.md", "/proj/mem/s1.md"} {
		if !strings.Contains(got, want) {
			t.Errorf("update prompt missing %q", want)
		}
	}
}
