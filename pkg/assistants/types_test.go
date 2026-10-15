package assistants

import (
	"os/exec"
	"strings"
	"testing"
)

// TestNoForbiddenImports фиксирует архитектурную границу: pkg/assistants —
// нейтральный словарь, на который ссылаются и state, и orchestrator. Если он
// сам начнёт импортировать любой из них (даже подпакет), образуется цикл
// (или скрытая связность, которую легко не заметить при рефакторинге).
func TestNoForbiddenImports(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "github.com/akopichin/afm/pkg/assistants").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}

	forbiddenRoots := []string{
		"github.com/akopichin/afm/pkg/state",
		"github.com/akopichin/afm/pkg/orchestrator",
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		for _, root := range forbiddenRoots {
			if line == root || strings.HasPrefix(line, root+"/") {
				t.Fatalf("pkg/assistants must not depend on %s (found %q in go list -deps)", root, line)
			}
		}
	}
}
