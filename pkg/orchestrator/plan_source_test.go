package orchestrator

import (
	"os"
	"path/filepath"
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
