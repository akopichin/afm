package executor_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
)

const (
	rootErrorResult   = `{"type":"result","subtype":"success","is_error":true,"result":"You've hit your limit","usage":{"input_tokens":10,"output_tokens":5}}`
	rootSuccessResult = `{"type":"result","subtype":"success","is_error":false,"result":"ok","usage":{"input_tokens":10,"output_tokens":5}}`
	toolErrorLine     = `{"type":"user","message":{"content":[{"type":"tool_result","is_error":true,"content":"command failed"}]}}`
)

// runTerminalAgent запускает RunAgent с фиксированным stdout и возвращает ошибку, лог и все OnUsage.
func runTerminalAgent(t *testing.T, phase string, lines ...string) (string, []accounting.Observation, error) {
	t.Helper()
	logFile := filepath.Join(t.TempDir(), "autonomous.log")
	var obs []accounting.Observation
	// Строки stdout передаём аргументами скрипта, чтобы кавычки JSON не ломали shell.
	ex := executor.New(executor.Config{
		Command:     testCmdShell,
		ExtraArgs:   append([]string{testFlagC, `printf '%s\n' "$@"`, testSeparator}, lines...),
		IdleTimeout: 5 * time.Second,
		Phase:       phase,
		OnUsage:     func(o accounting.Observation) { obs = append(obs, o) },
	})
	err := ex.RunAgent(context.Background(), "autonomous", "s1", "do work", logFile)
	logData, _ := os.ReadFile(logFile)
	return string(logData), obs, err
}

func TestTerminalResult_ErrorResultWithExitZeroIsTypedError(t *testing.T) {
	logText, obs, err := runTerminalAgent(t, string(flow.PhaseAutonomous), rootErrorResult)

	var tre *executor.TerminalResultError
	if !errors.As(err, &tre) {
		t.Fatalf("err = %v, want *TerminalResultError", err)
	}
	if !strings.Contains(err.Error(), "hit your limit") {
		t.Errorf("error text %q lost the provider message needed for retry classification", err)
	}
	// Сырой текст провайдера уже лежит в логе из потока; итог вызова — это баннер конца.
	if !strings.Contains(logText, "=== FAILED") || strings.Contains(logText, "=== completed") {
		t.Errorf("log must record the failed outcome, not a completion: %q", logText)
	}
	if len(obs) != 1 {
		t.Fatalf("OnUsage calls = %d, want exactly 1", len(obs))
	}
	if !obs[0].Metered || obs[0].Tokens.UncachedInput != 10 {
		t.Errorf("usage of the failed invocation must still be metered once: %+v", obs[0])
	}
}

func TestTerminalResult_IntermediateErrorThenSuccessIsSuccess(t *testing.T) {
	_, obs, err := runTerminalAgent(t, string(flow.PhaseAutonomous), rootErrorResult, toolErrorLine, rootSuccessResult)
	if err != nil {
		t.Fatalf("a later successful root result must win, got %v", err)
	}
	if len(obs) != 1 {
		t.Fatalf("OnUsage calls = %d, want exactly 1", len(obs))
	}
}

func TestTerminalResult_ToolErrorIsNotRootFailure(t *testing.T) {
	if _, _, err := runTerminalAgent(t, string(flow.PhaseAutonomous), toolErrorLine, rootSuccessResult); err != nil {
		t.Fatalf("tool/subagent error must not fail the invocation, got %v", err)
	}
}

func TestTerminalResult_OtherPhasesKeepOldBehaviour(t *testing.T) {
	if _, _, err := runTerminalAgent(t, "implementation", rootErrorResult); err != nil {
		t.Fatalf("non-autonomous phases are unchanged, got %v", err)
	}
}

func TestTerminalResult_RealProcessErrorKeepsPriority(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "autonomous.log")
	ex := executor.New(executor.Config{
		Command:     testCmdShell,
		ExtraArgs:   []string{testFlagC, `printf '%s\n' "$1"; exit 3`, testSeparator, rootErrorResult},
		IdleTimeout: 5 * time.Second,
		Phase:       string(flow.PhaseAutonomous),
	})
	err := ex.RunAgent(context.Background(), "autonomous", "s1", "do work", logFile)
	var tre *executor.TerminalResultError
	if err == nil || errors.As(err, &tre) {
		t.Fatalf("err = %v, want the process exit error, not the normalized terminal error", err)
	}
}

func TestTerminalResult_LongNonASCIIMessageIsCutOnACharacterBoundary(t *testing.T) {
	long := strings.Repeat("€", 600) // 3 байта на символ: срез по байтам разрезал бы символ
	line := `{"type":"result","subtype":"success","is_error":true,"result":"` + long + `"}`
	_, _, err := runTerminalAgent(t, string(flow.PhaseAutonomous), line)

	var tre *executor.TerminalResultError
	if !errors.As(err, &tre) {
		t.Fatalf("err = %v, want *TerminalResultError", err)
	}
	if !utf8.ValidString(tre.Message) {
		t.Fatalf("message is not valid UTF-8: %q", tre.Message)
	}
	if got := utf8.RuneCountInString(tre.Message); got != 501 { // 500 символов + многоточие
		t.Fatalf("message has %d characters, want 501", got)
	}
}
