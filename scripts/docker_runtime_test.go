package scripts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerfileRuntime_InstallsCursorAgentAndItsAdapter(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "Dockerfile.runtime"))
	if err != nil {
		t.Fatal(err)
	}
	dockerfile := string(raw)
	for _, want := range []string{"scripts/install-cursor-agent.sh", "scripts/cursor-agent-as-claude.sh /usr/local/bin/cursor-agent-as-claude"} {
		if !strings.Contains(dockerfile, want) {
			t.Errorf("Dockerfile.runtime lacks %q", want)
		}
	}
	if strings.Contains(dockerfile, "cursor-as-claude") {
		t.Error("Dockerfile.runtime still installs the removed Cursor Cloud Agents adapter")
	}
}
