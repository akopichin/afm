package executor_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/executor"
)

// stubClaudeRecorder puts a fake `claude` on PATH that records its argv (one per line) to the returned
// file, so tests can assert on the assembled command line for claude-family commands.
func stubClaudeRecorder(t *testing.T) (argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args.txt")
	script := "#!/usr/bin/env bash\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > " + argsFile + "\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsFile
}

func runClaudeAndReadArgs(t *testing.T, cfg executor.Config, argsFile string) string {
	t.Helper()
	logFile := filepath.Join(t.TempDir(), "impl.log")
	if err := executor.New(cfg).RunAgent(context.Background(), "implementation", "s1", "do work", logFile); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	return string(data)
}

func TestReasoningEffort_ClaudeGetsFlag(t *testing.T) {
	argsFile := stubClaudeRecorder(t)
	got := runClaudeAndReadArgs(t, executor.Config{Command: "claude", Reasoning: "high"}, argsFile)
	if !strings.Contains(got, "--effort\nhigh") {
		t.Errorf("expected --effort high in args:\n%s", got)
	}
}

func TestReasoningEffort_ClaudeClamp(t *testing.T) {
	argsFile := stubClaudeRecorder(t)
	// claude has no "minimal" — it clamps to "low".
	got := runClaudeAndReadArgs(t, executor.Config{Command: "claude", Reasoning: "minimal"}, argsFile)
	if !strings.Contains(got, "--effort\nlow") {
		t.Errorf("expected clamp minimal→low:\n%s", got)
	}
	if strings.Contains(got, "--effort\nminimal") {
		t.Errorf("must not forward unclamped minimal:\n%s", got)
	}
}

func TestReasoningEffort_EmptyOmits(t *testing.T) {
	argsFile := stubClaudeRecorder(t)
	got := runClaudeAndReadArgs(t, executor.Config{Command: "claude"}, argsFile)
	if strings.Contains(got, "--effort") {
		t.Errorf("empty reasoning must not add --effort:\n%s", got)
	}
}

func TestReasoningEffort_NotDuplicated(t *testing.T) {
	argsFile := stubClaudeRecorder(t)
	got := runClaudeAndReadArgs(t, executor.Config{
		Command:   "claude",
		ExtraArgs: []string{"--effort", "medium"},
		Reasoning: "high",
	}, argsFile)
	if n := strings.Count(got, "--effort"); n != 1 {
		t.Errorf("expected exactly one --effort, got %d:\n%s", n, got)
	}
}

func TestReasoningEffort_NotDuplicatedEqualsForm(t *testing.T) {
	argsFile := stubClaudeRecorder(t)
	// Caller supplied the equals form (--effort=medium); reasoning must not append a second --effort.
	got := runClaudeAndReadArgs(t, executor.Config{
		Command:   "claude",
		ExtraArgs: []string{"--effort=medium"},
		Reasoning: "high",
	}, argsFile)
	if strings.Contains(got, "--effort\nhigh") {
		t.Errorf("must not append a bare --effort when --effort= already present:\n%s", got)
	}
	if n := strings.Count(got, "--effort"); n != 1 {
		t.Errorf("expected exactly one --effort token, got %d:\n%s", n, got)
	}
}

func TestReasoningEffort_NonClaudeCommandSkipped(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	// A non-claude command (bash recorder) must NOT receive --effort even when Reasoning is set.
	script := "for a in \"$@\"; do printf '%s\\n' \"$a\"; done > " + argsFile
	got := runClaudeAndReadArgs(t, executor.Config{
		Command:   "bash",
		ExtraArgs: []string{"-c", script, "--"},
		Reasoning: "high",
	}, argsFile)
	if strings.Contains(got, "--effort") {
		t.Errorf("non-claude command must not get --effort:\n%s", got)
	}
}
