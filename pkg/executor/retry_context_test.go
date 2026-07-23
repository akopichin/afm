package executor_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/executor"
)

func TestRenderActions(t *testing.T) {
	dir := t.TempDir()
	jsonl := filepath.Join(dir, "implementation.jsonl")
	longText := strings.Repeat("x", 500)
	longCmd := strings.Repeat("echo hi; ", 50)
	lines := []string{
		fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"text","text":"%s"}]}}`, longText),
		fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"%s"}}]}}`, longCmd),
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"/tmp/a.md"}}]}}`,
		`{"type":"result","subtype":"success"}`,
	}
	if err := os.WriteFile(jsonl, []byte(strings.Join(lines, "\n")), 0644); err != nil {
		t.Fatal(err)
	}

	got := executor.RenderActions(jsonl)
	if len(got) != 3 {
		t.Fatalf("RenderActions returned %d lines, want 3: %v", len(got), got)
	}
	if !strings.Contains(got[0], longText) {
		t.Errorf("text action truncated, want full %d-char text in %q", len(longText), got[0])
	}
	if !strings.Contains(got[1], longCmd) {
		t.Errorf("bash action truncated, want full command in %q", got[1])
	}
	if !strings.Contains(got[2], "/tmp/a.md") {
		t.Errorf("write action missing file path: %q", got[2])
	}
}

func TestRenderActionsMissingFile(t *testing.T) {
	if got := executor.RenderActions(filepath.Join(t.TempDir(), "nope.jsonl")); got != nil {
		t.Errorf("expected nil for missing file, got %v", got)
	}
}
