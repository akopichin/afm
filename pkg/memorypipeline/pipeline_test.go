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
	// Sources — уже готовый, классифицированный SourceInventory список
	// (agent-session сначала, затем supplemental): промпт должен назвать
	// КАЖДЫЙ путь явно, не "прочитай директорию".
	got := BuildPrompt(testPrompts(), AgentSpec{
		Kind: "reflect",
		Sources: []string{
			"/run/s1/autonomous.log",
			"/run/s1/execution_summary.md",
			"/run/s1/planning.dialog.jsonl",
			"/run/s1/prenote.md",
			"/run/s1/feedback.md",
		},
		DatasetOut: "/run/s1/reflect_dataset.yaml",
	})
	for _, want := range []string{
		"REFLECT-BASE",
		"/run/s1/autonomous.log",
		"/run/s1/execution_summary.md",
		"/run/s1/planning.dialog.jsonl",
		"/run/s1/prenote.md",
		"/run/s1/feedback.md",
		"/run/s1/reflect_dataset.yaml",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("reflect prompt missing %q", want)
		}
	}
	// Промпт больше не должен инструктировать читать "каждый *.log под
	// директорией" — источники теперь явный список файлов от SourceInventory.
	if strings.Contains(got, "if a path is a directory") {
		t.Errorf("reflect prompt must not fall back to directory-scanning wording:\n%s", got)
	}
	// Формулировка про приоритет пользовательского ввода над логами должна
	// сохраниться.
	if !strings.Contains(got, "user") || !strings.Contains(got, "priority") {
		t.Errorf("reflect prompt must keep the user-input-priority wording:\n%s", got)
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
