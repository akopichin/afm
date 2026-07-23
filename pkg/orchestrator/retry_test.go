package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildRetryContext_FullActionNotTruncated(t *testing.T) {
	stageDir := t.TempDir()
	longOutput := strings.Repeat("output-line ", 100) // far longer than any sane truncate_output limit
	line := fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"%s"}}]}}`, longOutput)
	if err := os.WriteFile(filepath.Join(stageDir, "implementation.jsonl"), []byte(line+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	got := buildRetryContext(stageDir, phaseImplementation)
	if !strings.Contains(got, longOutput) {
		t.Errorf("retry context does not contain full action text — got truncated/missing content: %q", got)
	}
}

func TestBuildRetryContext_MissingLogReturnsEmpty(t *testing.T) {
	stageDir := t.TempDir()
	if got := buildRetryContext(stageDir, phaseImplementation); got != "" {
		t.Errorf("expected empty context for missing jsonl, got %q", got)
	}
}
