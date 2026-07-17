package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
)

// TestResolvePlanSource фиксирует разрешение пути stage.Plan: относительный путь
// (./plan.md) ищется в run-директориях стадий-зависимостей (где артефакты реально
// лежат), с fallback на буквальный путь для pre-existing планов.
func TestResolvePlanSource(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "init"), 0755); err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(dir, "init", "plan.md")
	if err := os.WriteFile(planPath, []byte("# plan"), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		stage flow.Stage
		want  string
	}{
		{
			name:  "relative plan resolved from a dependency's run dir",
			stage: flow.Stage{Plan: "./plan.md", DependsOn: []string{"init"}},
			want:  planPath,
		},
		{
			name:  "fallback to literal path when no dependency has the file",
			stage: flow.Stage{Plan: "./plan.md", DependsOn: []string{"other"}},
			want:  "./plan.md",
		},
		{
			name:  "non-relative path returned as-is",
			stage: flow.Stage{Plan: "/abs/plan.md"},
			want:  "/abs/plan.md",
		},
		{
			name:  "empty plan stays empty",
			stage: flow.Stage{Plan: ""},
			want:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolvePlanSource(dir, tt.stage); got != tt.want {
				t.Errorf("resolvePlanSource = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestCollectDependencyPlans_AutonomousFallback проверяет, что для зависимости,
// прошедшей автономный трек (есть autonomous.flag), контекст читается из
// execution_summary.md, а не из plan.md.
func TestCollectDependencyPlans_AutonomousFallback(t *testing.T) {
	runDir := t.TempDir()

	// Создаём "предыдущую" автономную стадию
	depDir := filepath.Join(runDir, "dep1")
	if err := os.MkdirAll(depDir, 0755); err != nil {
		t.Fatal(err)
	}
	// autonomous.flag сигнализирует что стадия прошла автономный трек
	if err := os.WriteFile(filepath.Join(depDir, "autonomous.flag"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	// execution_summary.md — контекст который будет передан зависимой стадии
	summary := "## Summary\nDid the thing.\n## Changes\n- foo.go\n## Result\nOK\n"
	if err := os.WriteFile(filepath.Join(depDir, "execution_summary.md"), []byte(summary), 0644); err != nil {
		t.Fatal(err)
	}

	stages := []flow.Stage{
		{ID: "dep1", Description: "dep", Agents: []flow.AgentType{flow.AgentPlanning}},
		{ID: "s1", Description: "main", DependsOn: []string{"dep1"}, Agents: []flow.AgentType{flow.AgentPlanning}},
	}
	result := CollectDependencyPlans(runDir, stages[1], stages)
	if !strings.Contains(result, "Did the thing") {
		t.Errorf("expected execution_summary.md content in result, got:\n%s", result)
	}
	if strings.Contains(result, "plan not available") {
		t.Error("should not say 'plan not available' when execution_summary.md exists")
	}
}

// TestCollectDependencyPlans_StandardPlan проверяет стандартный путь: без
// autonomous.flag читается plan.md как и раньше.
func TestCollectDependencyPlans_StandardPlan(t *testing.T) {
	runDir := t.TempDir()
	depDir := filepath.Join(runDir, "dep1")
	if err := os.MkdirAll(depDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Нет autonomous.flag — читаем plan.md как обычно
	if err := os.WriteFile(filepath.Join(depDir, "plan.md"), []byte("## Tasks\n- do it\n"), 0644); err != nil {
		t.Fatal(err)
	}
	stages := []flow.Stage{
		{ID: "dep1", Agents: []flow.AgentType{flow.AgentPlanning}},
		{ID: "s1", DependsOn: []string{"dep1"}, Agents: []flow.AgentType{flow.AgentPlanning}},
	}
	result := CollectDependencyPlans(runDir, stages[1], stages)
	if !strings.Contains(result, "do it") {
		t.Errorf("expected plan.md content, got:\n%s", result)
	}
}
