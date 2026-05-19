package executor_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitlab.ae-rus.net/bx/ai-flow-manager/pkg/executor"
)

func TestParseToolAction(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		wantTool   string
		wantDetail string
		wantOK     bool
	}{
		{
			name:       "write tool",
			line:       `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"pkg/auth/jwt.go","content":"..."}}]}}`,
			wantTool:   "Write",
			wantDetail: "pkg/auth/jwt.go",
			wantOK:     true,
		},
		{
			name:       "bash tool",
			line:       `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git commit -m \"feat: jwt\""}}]}}`,
			wantTool:   "Bash",
			wantDetail: "git commit -m \"feat: jwt\"",
			wantOK:     true,
		},
		{
			name:       "read tool",
			line:       `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"pkg/flow/flow.go"}}]}}`,
			wantTool:   "Read",
			wantDetail: "pkg/flow/flow.go",
			wantOK:     true,
		},
		{
			name:       "text content",
			line:       `{"type":"assistant","message":{"content":[{"type":"text","text":"I will implement the auth module now"}]}}`,
			wantTool:   "text",
			wantDetail: "I will implement the auth module now",
			wantOK:     true,
		},
		{
			name:   "result event ignored",
			line:   `{"type":"result","subtype":"success"}`,
			wantOK: false,
		},
		{
			name:   "non-json ignored",
			line:   "not json",
			wantOK: false,
		},
		{
			name:       "edit tool",
			line:       `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"pkg/auth/jwt.go","old_string":"old","new_string":"new"}}]}}`,
			wantTool:   "Edit",
			wantDetail: "pkg/auth/jwt.go",
			wantOK:     true,
		},
		{
			name:       "glob tool",
			line:       `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Glob","input":{"pattern":"**/*.go"}}]}}`,
			wantTool:   "Glob",
			wantDetail: "**/*.go",
			wantOK:     true,
		},
		{
			name:       "default tool with command",
			line:       `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"WebSearch","input":{"query":"golang testing"}}]}}`,
			wantTool:   "WebSearch",
			wantDetail: "golang testing", // falls back to raw Input
			wantOK:     true,
		},
		{
			name:       "text truncation over 100 chars",
			line:       fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"text","text":"%s"}]}}`, strings.Repeat("x", 120)),
			wantTool:   "text",
			wantDetail: strings.Repeat("x", 100) + "...",
			wantOK:     true,
		},
		{
			name:       "bash truncation over 80 chars",
			line:       fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"%s"}}]}}`, strings.Repeat("echo ", 20)),
			wantTool:   "Bash",
			wantDetail: strings.Repeat("echo ", 16), // 80 chars
			wantOK:     true,
		},
		{
			name:   "empty text ignored",
			line:   `{"type":"assistant","message":{"content":[{"type":"text","text":""}]}}`,
			wantOK: false,
		},
		{
			name:       "multiple content blocks logs first action",
			line:       `{"type":"assistant","message":{"content":[{"type":"text","text":"I will do X"},{"type":"tool_use","name":"Write","input":{"file_path":"pkg/main.go"}}]}}`,
			wantTool:   "text",
			wantDetail: "I will do X",
			wantOK:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tool, detail, ok := executor.ParseToolAction(tc.line)
			if ok != tc.wantOK {
				t.Fatalf("ok=%v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if tool != tc.wantTool {
				t.Errorf("tool=%q, want %q", tool, tc.wantTool)
			}
			if !strings.Contains(detail, tc.wantDetail) {
				t.Errorf("detail=%q, want to contain %q", detail, tc.wantDetail)
			}
		})
	}
}

func TestRunPlanningCapturesOutput(t *testing.T) {
	dir := t.TempDir()
	outFile := filepath.Join(dir, "plan.md")
	logFile := filepath.Join(dir, "planning.log")

	ex := executor.New(executor.Config{
		Command: "bash",
		ExtraArgs: []string{"-c",
			`echo '{"type":"assistant","message":{"content":[{"type":"text","text":"# Plan\n\nstep 1"}]}}'
	echo '{"type":"result","subtype":"success"}'`},
		IdleTimeout: 5 * time.Second,
	})

	err := ex.RunPlanning(context.Background(), "auth-module", "generate a plan", outFile, logFile)
	if err != nil {
		t.Fatalf("RunPlanning: %v", err)
	}

	// plan.md содержит текст
	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read plan output: %v", err)
	}
	if !strings.Contains(string(data), "Plan") {
		t.Errorf("plan output missing: %q", string(data))
	}

	// .log содержит баннер
	logData, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	logContent := string(logData)
	if !strings.Contains(logContent, "planning agent") {
		t.Errorf("log missing planning agent: %q", logContent)
	}
	if !strings.Contains(logContent, "auth-module") {
		t.Errorf("log missing stage name: %q", logContent)
	}
	if !strings.Contains(logContent, "completed") {
		t.Errorf("log missing completion: %q", logContent)
	}

	// .jsonl существует
	jsonlFile := strings.TrimSuffix(logFile, ".log") + ".jsonl"
	raw, err := os.ReadFile(jsonlFile)
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	if len(raw) == 0 {
		t.Error("jsonl file should not be empty")
	}
}

func TestRunAgentLogsOutput(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "impl.log")

	ex := executor.New(executor.Config{
		Command: "bash",
		ExtraArgs: []string{"-c",
			`echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"pkg/foo.go"}}]}}'
	echo '{"type":"result","subtype":"success"}'`},
		IdleTimeout: 5 * time.Second,
	})

	err := ex.RunAgent(context.Background(), "implementation", "auth-module", "implement the plan", logFile)
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	// human-readable log
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "implementation agent") {
		t.Errorf("log missing agent type: %q", content)
	}
	if !strings.Contains(content, "auth-module") {
		t.Errorf("log missing stage name: %q", content)
	}
	if !strings.Contains(content, "Write") {
		t.Errorf("log missing tool action: %q", content)
	}
	if !strings.Contains(content, "pkg/foo.go") {
		t.Errorf("log missing file detail: %q", content)
	}
	if !strings.Contains(content, "completed") {
		t.Errorf("log missing completion: %q", content)
	}

	// raw jsonl
	jsonlFile := strings.TrimSuffix(logFile, ".log") + ".jsonl"
	raw, err := os.ReadFile(jsonlFile)
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	if !strings.Contains(string(raw), `"type":"assistant"`) {
		t.Errorf("jsonl missing raw events: %q", string(raw))
	}
}

// TestRunPlanningAgentWritesTool воспроизводит баг: если плановый агент пишет план
// через Write tool вместо вывода текста, RunPlanning перезаписывает plan.md пустой
// строкой (textBuf пуст, нет type=="text" блоков).
func TestRunPlanningAgentWritesTool(t *testing.T) {
	dir := t.TempDir()
	outFile := filepath.Join(dir, "plan.md")
	logFile := filepath.Join(dir, "planning.log")

	planContent := "# Plan\n\n- step 1\n- step 2\n"

	// Симулируем subprocess, который:
	// 1. Выводит tool_use Write событие (без text блоков — textBuf остаётся пустым)
	// 2. Реально записывает outFile (как это делает claude при выполнении Write tool)
	// После этого RunPlanning вызывает os.WriteFile(outFile, "", 0644) и затирает план.
	script := fmt.Sprintf(
		`printf '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"%s","content":"..."}}]}}\n'`+
			"\nprintf '%%s' %q > %q"+
			"\nprintf '{\"type\":\"result\",\"subtype\":\"success\"}\n'",
		outFile, planContent, outFile,
	)

	ex := executor.New(executor.Config{
		Command:     "bash",
		ExtraArgs:   []string{"-c", script},
		IdleTimeout: 5 * time.Second,
	})

	if err := ex.RunPlanning(context.Background(), "init", "generate a plan", outFile, logFile); err != nil {
		t.Fatalf("RunPlanning: %v", err)
	}

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read plan.md: %v", err)
	}
	// Баг: plan.md пустой, хотя агент записал план через Write tool.
	// После фикса (не перезаписывать outFile если textBuf пуст) тест должен пройти.
	if !strings.Contains(string(data), "Plan") {
		t.Errorf("plan.md overwritten with empty content (textBuf empty): got %q", string(data))
	}
}

func TestIdleTimeout(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "impl.log")

	ex := executor.New(executor.Config{
		Command:     "bash",
		ExtraArgs:   []string{"-c", "sleep 10"},
		IdleTimeout: 100 * time.Millisecond,
	})

	err := ex.RunAgent(context.Background(), "implementation", "test-stage", "do work", logFile)
	if err == nil {
		t.Error("expected timeout error, got nil")
	}

	// log должен содержать FAILED
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(data), "FAILED") {
		t.Errorf("log should contain FAILED on timeout: %q", string(data))
	}
}
