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

// TestIsRetryableError: перенесён из completion_test.go при выносе
// completion.go в pkg/orchestrator/stagefiles (Task 3 orchestrator-split) —
// isRetryableError и константы match* остаются в этом пакете (retry.go,
// errors.go), поэтому тест здесь, а не в stagefiles.
func TestIsRetryableError(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"You've hit your limit", true},
		{matchRateLimit + " exceeded", true},
		{matchTooManyRequests, true},
		{matchOverloaded, true},
		{matchAtCapacity, true},
		{"500 Internal Server Error", true},
		{matchInternalServerError, true},
		{"something went wrong", false},
		{"", false},
	}
	for _, c := range cases {
		var err error
		if c.msg != "" {
			err = fmt.Errorf("%s", c.msg)
		}
		if got := isRetryableError(err); got != c.want {
			t.Errorf("isRetryableError(%q) = %v, want %v", c.msg, got, c.want)
		}
	}

	if isRetryableError(nil) {
		t.Error("nil should not be retryable")
	}
}
