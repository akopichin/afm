package executor_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/executor"
)

// Синтетические значения: тесты проверяют, что они доходят до процесса или нет, и что нигде не печатаются.
var runtimeEnvValues = map[string]string{ //nolint:gosec // test fixture, not real secrets
	"AFM_SECRET_FAKE_RECIPE":    "own-secret-value",
	"AFM_SYSPROMPT_FAKE_RECIPE": "own-sysprompt-value",
	"AFM_SECRET_OTHER":          "foreign-secret-value",
	"AFM_SYSPROMPT_OTHER":       "foreign-sysprompt-value",
	"AFM_HOOK_SECRET_0_0":       "hook-secret-value",
	"AFM_MESH_TOKEN":            "mesh-token-value",
	"AFM_STAGE_DIR":             "/parent/stage",
	"AFM_BROWSER_DESCRIPTOR":    "/parent/browser.json",
	"AFM_STOP_HOOK_DESCRIPTOR":  "/parent/stop-hook.json",
	"CLAUDE_CODE_OAUTH_TOKEN":   "ambient-oauth-value",
	"ANTHROPIC_BASE_URL":        "https://ambient.example",
	"ORDINARY_VAR":              "ordinary-value",
	"AFM_CUSTOM_PLAIN":          "plain-afm-value",
}

// runtimeEnvOf запускает fake-команду name (shell-скрипт в WrapperDir) через run и возвращает её окружение.
func runtimeEnvOf(t *testing.T, name string, run func(ex *executor.Executor, dir string) error, cfg executor.Config) string {
	t.Helper()
	for k, v := range runtimeEnvValues {
		t.Setenv(k, v)
	}
	dir := t.TempDir()
	wrapDir := t.TempDir()
	envFile := filepath.Join(dir, "env.txt")
	script := "#!/bin/bash\n" + `env > ` + envFile + "\n" +
		emitAssistantTextScript(t, helperValidResultJSON) + "\n" + resultSuccessLine() + "\n"
	if err := os.WriteFile(filepath.Join(wrapDir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg.Command, cfg.WrapperDir, cfg.IdleTimeout = name, wrapDir, 5*time.Second
	if err := run(executor.New(cfg), dir); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"helper.log", "helper.jsonl", "helper.stderr.log", "verify.log", "result.json"} {
		raw, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			continue
		}
		for k, v := range runtimeEnvValues {
			if strings.Contains(string(raw), v) {
				t.Errorf("%s printed the value of %s", f, k)
			}
		}
	}
	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func runHelper(ex *executor.Executor, dir string) error {
	_, err := ex.RunHelperAgent(context.Background(), "t1", "reviewer", "review",
		filepath.Join(dir, "helper.log"), filepath.Join(dir, "result.json"))
	return err
}

func runVerify(ex *executor.Executor, dir string) error {
	_, err := ex.RunVerifyAgent(context.Background(), "codex", "s1", "verify",
		filepath.Join(dir, "verify.log"), filepath.Join(dir, "result.json"))
	return err
}

func assertEnv(t *testing.T, env string, present, absent []string) {
	t.Helper()
	for _, k := range present {
		if !envContainsLine(env, k+"="+runtimeEnvValues[k]) {
			t.Errorf("env must keep %s", k)
		}
	}
	for _, k := range absent {
		if strings.Contains(env, k+"=") {
			t.Errorf("env must not contain %s", k)
		}
	}
}

// foreignTransport — всё, что helper не должен унаследовать ни в одном runtime-режиме.
var foreignTransport = []string{
	"AFM_SECRET_OTHER", "AFM_SYSPROMPT_OTHER", "AFM_HOOK_SECRET_0_0", "AFM_MESH_TOKEN",
	"AFM_STAGE_DIR", "AFM_BROWSER_DESCRIPTOR", "AFM_STOP_HOOK_DESCRIPTOR",
}

func TestHelperRuntimeEnv_Isolated(t *testing.T) {
	env := runtimeEnvOf(t, "fake-recipe", runHelper, executor.Config{})
	assertEnv(t, env,
		[]string{"AFM_SECRET_FAKE_RECIPE", "AFM_SYSPROMPT_FAKE_RECIPE", "ORDINARY_VAR", "AFM_CUSTOM_PLAIN"},
		append([]string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_BASE_URL"}, foreignTransport...))
}

func TestHelperRuntimeEnv_Wrapper(t *testing.T) {
	env := runtimeEnvOf(t, "fake-recipe", runHelper, executor.Config{HelperWrapperRuntime: true})
	assertEnv(t, env,
		[]string{"AFM_SECRET_FAKE_RECIPE", "AFM_SYSPROMPT_FAKE_RECIPE", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_BASE_URL", "ORDINARY_VAR", "AFM_CUSTOM_PLAIN"},
		foreignTransport)
}

// Bare claude держит собственные Claude-креды без wrapper-флага: другого агента, от которого их прятать, нет.
func TestHelperRuntimeEnv_BareClaudeKeepsAmbientAuth(t *testing.T) {
	env := runtimeEnvOf(t, "claude", runHelper, executor.Config{})
	assertEnv(t, env, []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_BASE_URL", "ORDINARY_VAR"}, foreignTransport)
}

// Регрессия verify: свой sysprompt и чужой Claude auth по-прежнему срезаны, флаг helper-а игнорируется.
func TestVerifyEnv_UnchangedByHelperRuntime(t *testing.T) {
	for _, wrapper := range []bool{false, true} {
		env := runtimeEnvOf(t, "fake-recipe", runVerify, executor.Config{HelperWrapperRuntime: wrapper})
		assertEnv(t, env,
			[]string{"AFM_SECRET_FAKE_RECIPE", "ORDINARY_VAR"},
			append([]string{"AFM_SYSPROMPT_FAKE_RECIPE", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_BASE_URL"}, foreignTransport...))
	}
}

// Значение секрета, которое агент сам напечатал, не доходит до логов и ответа (редакция из окружения afm).
func TestHelperRuntimeEnv_PrintedSecretIsRedacted(t *testing.T) {
	const secret = "own-secret-value"
	t.Setenv("AFM_SECRET_FAKE_RECIPE", secret)
	dir := t.TempDir()
	wrapDir := t.TempDir()
	script := "#!/bin/bash\n" +
		`echo "token is $AFM_SECRET_FAKE_RECIPE" >&2` + "\n" +
		emitAssistantTextScript(t, `{"schema_version":1,"kind":"result","verdict":"pass","summary":"saw `+secret+`"}`) + "\n" +
		resultSuccessLine() + "\n"
	if err := os.WriteFile(filepath.Join(wrapDir, "fake-recipe"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ex := executor.New(executor.Config{
		Command: "fake-recipe", WrapperDir: wrapDir, IdleTimeout: 5 * time.Second,
		Secrets: executor.SecretValuesForRedaction,
	})
	outcome, err := ex.RunHelperAgent(context.Background(), "t1", "reviewer", "review",
		filepath.Join(dir, "helper.log"), filepath.Join(dir, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(outcome.RawText, secret) {
		t.Errorf("RawText carries the secret: %s", outcome.RawText)
	}
	for _, f := range []string{"helper.log", "helper.jsonl", "helper.stderr.log", "result.json"} {
		raw, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if strings.Contains(string(raw), secret) {
			t.Errorf("%s carries the secret:\n%s", f, raw)
		}
	}
}

const notLoggedInResult = `{"type":"result","subtype":"success","is_error":true,"result":"Not logged in · Please run /login"}`

func cliErrorOutcome(t *testing.T, exit, resultLine string, cfg executor.Config) (executor.HelperOutcome, string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/bash\ncat <<'EOF'\n" + resultLine + "\nEOF\nexit " + exit + "\n"
	path := filepath.Join(dir, "fake-cli")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg.Command, cfg.IdleTimeout = path, 5*time.Second
	resultFile := filepath.Join(dir, "result.json")
	outcome, err := executor.New(cfg).RunHelperAgent(context.Background(), "t1", "reviewer", "review",
		filepath.Join(dir, "helper.log"), resultFile)
	if err != nil {
		t.Fatalf("RunHelperAgent: %v", err)
	}
	return outcome, resultFile
}

func TestRunHelperAgent_CLIReportedError(t *testing.T) {
	for _, exit := range []string{"0", "1"} {
		t.Run("exit "+exit, func(t *testing.T) {
			outcome, resultFile := cliErrorOutcome(t, exit, notLoggedInResult, executor.Config{})
			if outcome.ProcessOK || outcome.ProtocolErr != nil {
				t.Fatalf("outcome = %+v, want a failed process without a protocol error", outcome)
			}
			var cliErr *executor.CLIReportedError
			if !errors.As(outcome.ProcessErr, &cliErr) {
				t.Fatalf("ProcessErr = %v, want *CLIReportedError", outcome.ProcessErr)
			}
			if cliErr.Subtype != "success" || cliErr.Message != "Not logged in · Please run /login" {
				t.Errorf("CLIReportedError = %+v", cliErr)
			}
			if _, err := os.Stat(resultFile); err == nil {
				t.Error("no answer must be written for a CLI-reported error")
			}
		})
	}
}

func TestRunHelperAgent_CLIReportedErrorKeepsExitCause(t *testing.T) {
	outcome, _ := cliErrorOutcome(t, "1", notLoggedInResult, executor.Config{})
	if outcome.ProcessErr == nil || !strings.Contains(outcome.ProcessErr.Error(), "Not logged in") {
		t.Fatalf("ProcessErr = %v", outcome.ProcessErr)
	}
	var exitErr interface{ ExitCode() int }
	if !errors.As(outcome.ProcessErr, &exitErr) || exitErr.ExitCode() != 1 {
		t.Errorf("Unwrap must keep the exit status, got %v", errors.Unwrap(outcome.ProcessErr))
	}
}

func TestRunHelperAgent_CLIReportedErrorRedactedAndTruncated(t *testing.T) {
	const secret = "leaky-token-value" //nolint:gosec // test fixture, not a real secret
	long := strings.Repeat("x", 2000)
	line := `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"bad ` + secret + ` ` + long + `"}`
	outcome, _ := cliErrorOutcome(t, "0", line, executor.Config{
		Redact: func(s string) string { return strings.ReplaceAll(s, secret, "[REDACTED]") },
	})
	var cliErr *executor.CLIReportedError
	if !errors.As(outcome.ProcessErr, &cliErr) {
		t.Fatalf("ProcessErr = %v, want *CLIReportedError", outcome.ProcessErr)
	}
	if strings.Contains(cliErr.Message, secret) || !strings.HasPrefix(cliErr.Message, "bad [REDACTED]") {
		t.Errorf("message not redacted: %.60q", cliErr.Message)
	}
	if n := len([]rune(cliErr.Message)); n > 401 {
		t.Errorf("message has %d runes, want it truncated", n)
	}
}

func TestRunHelperAgent_NotCLIReportedError(t *testing.T) {
	// exit != 0 без structured result-ошибки и успешный result после ошибочного остаются прежними исходами.
	outcome, _ := cliErrorOutcome(t, "1", `{"type":"result","subtype":"success","is_error":false,"result":"ok"}`, executor.Config{})
	var cliErr *executor.CLIReportedError
	if outcome.ProcessErr == nil || errors.As(outcome.ProcessErr, &cliErr) {
		t.Errorf("plain non-zero exit must stay a process error, got %v", outcome.ProcessErr)
	}
	outcome, _ = cliErrorOutcome(t, "0", notLoggedInResult+"\n"+`{"type":"result","subtype":"success","is_error":false,"result":"ok"}`, executor.Config{})
	if errors.As(outcome.ProcessErr, &cliErr) {
		t.Errorf("a later successful result clears the error, got %v", outcome.ProcessErr)
	}
}

func TestHelperClaudeArgs(t *testing.T) {
	def := executor.DefaultClaudeArgs()
	tests := []struct {
		name  string
		extra []string
		want  []string
	}{
		{"nothing", nil, nil},
		{"pairs", []string{"--model", "m1", "--effort", "high"}, []string{"--model", "m1", "--effort", "high"}},
		{"equals form", []string{"--model=m1", "--fallback-model=m2", "--effort=low"}, []string{"--model=m1", "--fallback-model=m2", "--effort=low"}},
		{"same value is kept", []string{"--model", "m", "--fallback-model", "m"}, []string{"--model", "m", "--fallback-model", "m"}},
		{"others are dropped", []string{"--resume", "s1", "--mcp-config", "/x", "--verbose-x", "--model", "m1", "--bare"}, []string{"--model", "m1"}},
		{"dangling flag", []string{"--model"}, nil},
		{"flag followed by flag", []string{"--model", "--effort", "high"}, []string{"--effort", "high"}},
		{"value of a dropped flag is not a flag", []string{"--add-dir", "--model"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := executor.HelperModelArgs(tt.extra); !slices.Equal(got, tt.want) {
				t.Errorf("HelperModelArgs(%q) = %q, want %q", tt.extra, got, tt.want)
			}
			got := executor.HelperClaudeArgs(tt.extra)
			if want := append(slices.Clone(def), tt.want...); !slices.Equal(got, want) {
				t.Errorf("HelperClaudeArgs(%q) = %q, want %q", tt.extra, got, want)
			}
			if again := executor.HelperClaudeArgs(executor.HelperModelArgs(tt.extra)); !slices.Equal(again, got) {
				t.Errorf("filtering must be idempotent: %q vs %q", again, got)
			}
		})
	}
}

// The helper process gets the stream-json defaults, the allowlisted model pairs once each and, for bare claude only,
// one --effort from the reasoning: an --effort of the extra arguments is not repeated.
func TestRunHelperAgent_ArgvReachesTheProcess(t *testing.T) {
	cases := []struct {
		name, effort string
		bare         bool
		extra        []string
	}{
		{name: "bare claude", effort: "--effort high", bare: true, extra: []string{"--model", "m", "--fallback-model", "m"}},
		{name: "bare claude with its own effort", effort: "--effort low", bare: true, extra: []string{"--model", "m", "--fallback-model", "m", "--effort", "low"}},
		{name: "another command", extra: []string{"--model", "m", "--fallback-model", "m"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			argsFile := filepath.Join(dir, "args.txt")
			script := helperScript(t, argsFile, filepath.Join(dir, "env.txt"), helperValidResultJSON)
			cfg := executor.Config{Command: script, ExtraArgs: executor.HelperClaudeArgs(tc.extra), Reasoning: "high", IdleTimeout: 5 * time.Second}
			if tc.bare {
				claude := filepath.Join(filepath.Dir(script), "claude")
				if err := os.Rename(script, claude); err != nil {
					t.Fatal(err)
				}
				cfg.Command, cfg.WrapperDir = "claude", filepath.Dir(claude)
			}
			if _, err := executor.New(cfg).RunHelperAgent(context.Background(), "t1", "reviewer", "review", filepath.Join(dir, "log"), filepath.Join(dir, "result.json")); err != nil {
				t.Fatalf("RunHelperAgent: %v", err)
			}
			raw, err := os.ReadFile(argsFile)
			if err != nil {
				t.Fatal(err)
			}
			args := " " + strings.TrimSpace(string(raw)) + " "
			for _, want := range append(executor.DefaultClaudeArgs(), "--model m ", "--fallback-model m ") {
				if !strings.Contains(args, " "+want) {
					t.Errorf("argv %q lacks %q", args, want)
				}
			}
			if n := strings.Count(args, " --model "); n != 1 {
				t.Errorf("argv %q has --model %d times, want once", args, n)
			}
			if n := strings.Count(args, "--effort"); tc.effort == "" && n != 0 || tc.effort != "" && (n != 1 || !strings.Contains(args, tc.effort)) {
				t.Errorf("argv %q: --effort %d times, want %q", args, n, tc.effort)
			}
		})
	}
}

func runAuthor(ex *executor.Executor, dir string) error {
	return ex.RunAgent(context.Background(), "implementation", "s1", "go", filepath.Join(dir, "author.log"))
}

// Только helper с потребителем активности получает живой вывод адаптера: format-repair (тот же ModeHelper, но без
// OnActivity), verify и author продолжают работать по прежнему контракту, а унаследованный флаг не доходит никуда.
func TestStreamExtensionEnv_OnlyForHelperWithActivityConsumer(t *testing.T) {
	onActivity := func(executor.ActivityEvent) {}
	cases := []struct {
		name string
		run  func(ex *executor.Executor, dir string) error
		cfg  executor.Config
		want bool
	}{
		{"helper with OnActivity", runHelper, executor.Config{OnActivity: onActivity}, true},
		{"helper without OnActivity (format repair)", runHelper, executor.Config{}, false},
		{"verify with OnActivity configured", runVerify, executor.Config{OnActivity: onActivity}, false},
		{"author with OnActivity configured", runAuthor, executor.Config{OnActivity: onActivity}, false},
		{"author", runAuthor, executor.Config{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(executor.StreamExtensionEnv, "inherited")
			env := runtimeEnvOf(t, "fake-recipe", tc.run, tc.cfg)
			if got := envContainsLine(env, executor.StreamExtensionEnv+"=1"); got != tc.want {
				t.Errorf("%s=1 present = %v, want %v", executor.StreamExtensionEnv, got, tc.want)
			}
			if strings.Contains(env, executor.StreamExtensionEnv+"=inherited") {
				t.Errorf("an inherited %s reached the process", executor.StreamExtensionEnv)
			}
		})
	}
}

// CODEX_VERIFY задаёт политику, флаг расширения — только формат вывода: одно не включает другое.
func TestStreamExtensionEnv_IsIndependentOfCodexVerify(t *testing.T) {
	env := runtimeEnvOf(t, "fake-recipe", runHelper, executor.Config{OnActivity: func(executor.ActivityEvent) {}})
	if !envContainsLine(env, "CODEX_VERIFY=1") || !envContainsLine(env, executor.StreamExtensionEnv+"=1") {
		t.Errorf("a helper with OnActivity needs both CODEX_VERIFY=1 and the extension flag:\n%s", env)
	}
	env = runtimeEnvOf(t, "fake-recipe", runVerify, executor.Config{})
	if !envContainsLine(env, "CODEX_VERIFY=1") || strings.Contains(env, executor.StreamExtensionEnv+"=") {
		t.Errorf("verify keeps CODEX_VERIFY=1 and never gets the extension flag:\n%s", env)
	}
}
