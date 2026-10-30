package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/accounting"
)

const (
	terminalErrorResult   = `{"type":"result","subtype":"error_during_execution","is_error":true,"api_error_status":500,"result":"upstream exploded","usage":{"input_tokens":3,"output_tokens":4}}`
	terminalSuccessResult = `{"type":"result","subtype":"success","is_error":false,"result":"done","usage":{"input_tokens":3,"output_tokens":4}}`
	toolErrorLine         = `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","is_error":true,"content":"command failed"}]}}`
)

func terminalExecutor(script string, onUsage func(accounting.Observation)) *Executor {
	return New(Config{
		Command:     "bash",
		ExtraArgs:   []string{"-c", script},
		IdleTimeout: 5 * time.Second,
		OnUsage:     onUsage,
	})
}

func TestRunAgent_TerminalResultError(t *testing.T) {
	tests := []struct {
		name      string
		script    string
		wantError bool
	}{
		{name: "root is_error with exit 0", script: emitScript("0", terminalErrorResult), wantError: true},
		{name: "failed tool then successful root result", script: emitScript("0", toolErrorLine, terminalSuccessResult)},
		{name: "error result followed by a successful one", script: emitScript("0", terminalErrorResult, terminalSuccessResult)},
		{
			name: "subagent result is not the root result",
			script: emitScript("0", `{"type":"result","subtype":"error_during_execution","is_error":true,"parent_tool_use_id":"toolu_1","result":"subagent failed"}`,
				terminalSuccessResult),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logFile := filepath.Join(t.TempDir(), "agent.log")
			err := terminalExecutor(tc.script, nil).RunAgent(context.Background(), "implementation", "s1", "go", logFile)

			var terminal *TerminalResultError
			if got := errors.As(err, &terminal); got != tc.wantError {
				t.Fatalf("errors.As(*TerminalResultError) = %v, want %v (err = %v)", got, tc.wantError, err)
			}
			if !tc.wantError {
				if err != nil {
					t.Fatalf("RunAgent() = %v, want nil", err)
				}
				return
			}
			if terminal.Subtype != "error_during_execution" || terminal.Message != "upstream exploded" || terminal.APIErrorStatus != 500 {
				t.Errorf("terminal error = %+v", terminal)
			}
		})
	}
}

func TestRunAgent_TerminalResultKeepsCurrentStderrOnly(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "agent.log")
	stderrLog := strings.TrimSuffix(logFile, ".log") + ".stderr.log"
	if err := os.WriteFile(stderrLog, []byte("stale line from a previous attempt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "echo 'Background tasks still running after 600s; terminating' >&2\n" + emitScript("0", terminalErrorResult)

	err := terminalExecutor(script, nil).RunAgent(context.Background(), "implementation", "s1", "go", logFile)

	if err == nil {
		t.Fatal("RunAgent() = nil, want a terminal result error")
	}
	if !strings.Contains(err.Error(), "Background tasks still running") {
		t.Errorf("error lost the current stderr: %v", err)
	}
	if strings.Contains(err.Error(), "stale line") {
		t.Errorf("error leaked stderr of a previous attempt: %v", err)
	}
}

func TestRunAgent_UsageLimitBeatsTerminalResult(t *testing.T) {
	err := runLimitAgent(t, emitScript("0", rejectedEventSec, limitAssistant, limitResult))

	var limit *UsageLimitError
	if !errors.As(err, &limit) {
		t.Fatalf("err = %v, want *UsageLimitError", err)
	}
	var terminal *TerminalResultError
	if errors.As(err, &terminal) {
		t.Errorf("usage limit must not be reported as a terminal result error: %v", err)
	}
}

func TestTurnOutcome_KeepsInterruptAndCancelAhead(t *testing.T) {
	for _, runErr := range []error{ErrUserInterrupted, context.Canceled, ErrIdleTimeout} {
		t.Run(runErr.Error(), func(t *testing.T) {
			e := terminalExecutor("", nil)
			sc := newUsageLimitScanner(e.clk().Now, e.cfg)
			sc.observe(terminalErrorResult)

			outcome := e.turnOutcome(sc, runErr, filepath.Join(t.TempDir(), "agent.log"), 0)

			if !errors.Is(outcome, runErr) {
				t.Errorf("turnOutcome() = %v, want %v", outcome, runErr)
			}
		})
	}
}

// Log banner and accounting must describe the same failed turn that the runner returns.
func TestRunners_TerminalResultSameOutcomeInLogAndAccounting(t *testing.T) {
	script := emitScript("0", terminalErrorResult)
	tests := []struct {
		name string
		run  func(e *Executor, logFile string) error
	}{
		{"RunAgent", func(e *Executor, logFile string) error {
			return e.RunAgent(context.Background(), "implementation", "s1", "go", logFile)
		}},
		{"RunPlanning", func(e *Executor, logFile string) error {
			return e.RunPlanning(context.Background(), "s1", "go", filepath.Join(filepath.Dir(logFile), "plan.md"), logFile)
		}},
		{"RunHelperAgent", func(e *Executor, logFile string) error {
			outcome, err := e.RunHelperAgent(context.Background(), "t1", "reviewer", "go", logFile, filepath.Join(filepath.Dir(logFile), "result.txt"))
			if err == nil && outcome.ProcessOK {
				t.Error("helper outcome must not be ProcessOK after a terminal result error")
			}
			if err == nil {
				err = outcome.ProcessErr
			}
			return err
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var observations []accounting.Observation
			e := terminalExecutor(script, func(o accounting.Observation) { observations = append(observations, o) })
			logFile := filepath.Join(t.TempDir(), "agent.log")

			err := tc.run(e, logFile)

			var terminal *TerminalResultError
			if !errors.As(err, &terminal) {
				t.Fatalf("err = %v, want *TerminalResultError", err)
			}
			data, readErr := os.ReadFile(logFile)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !strings.Contains(string(data), "FAILED") || !strings.Contains(string(data), "upstream exploded") {
				t.Errorf("log must end with the terminal error, got %q", data)
			}
			if len(observations) != 1 {
				t.Fatalf("OnUsage calls = %d, want 1", len(observations))
			}
			if !observations[0].Metered || observations[0].Tokens.UncachedInput != 3 {
				t.Errorf("usage of the failed turn must still be recorded: %+v", observations[0])
			}
		})
	}
}

func TestRunVerifyAgent_TerminalResultIsNotAVerdict(t *testing.T) {
	finalText := `{"type":"assistant","message":{"content":[{"type":"text","text":"{\"verdict\":\"pass\"}"}]}}`
	e := terminalExecutor(emitScript("0", finalText, terminalErrorResult), nil)
	dir := t.TempDir()

	outcome, err := e.RunVerifyAgent(context.Background(), "verify", "s1", "go", filepath.Join(dir, "verify.log"), filepath.Join(dir, "result.json"))

	if err != nil {
		t.Fatalf("RunVerifyAgent() error = %v", err)
	}
	if outcome.ProcessOK || outcome.Result != nil {
		t.Errorf("a verifier that ended with an error result must not produce a verdict: %+v", outcome)
	}
}
