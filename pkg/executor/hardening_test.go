package executor_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/executor"
)

// helperScript writes a shell script that dumps argv/env and emits one final assistant text block plus a
// success result line — the same shape the verify tests use for the real agent protocol.
func helperScript(t *testing.T, argsFile, envFile, text string) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/bash\n" +
		`echo "$@" > ` + argsFile + "\n" +
		`env > ` + envFile + "\n" +
		emitAssistantTextScript(t, text) + "\n" +
		resultSuccessLine() + "\n"
	scriptPath := filepath.Join(dir, "agent.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return scriptPath
}

const helperValidResultJSON = `{"schema_version":1,"kind":"result","verdict":"pass","summary":"looks fine"}`

func TestHelperEnv_DropsForeignTransportSecrets(t *testing.T) {
	t.Setenv("AFM_SECRET_OTHERCMD", "leaked-other-secret")
	t.Setenv("AFM_SYSPROMPT_OTHERCMD", "leaked-other-sysprompt")
	t.Setenv("AFM_HOOK_SECRET_0_0", "leaked-hook-secret")

	dir := t.TempDir()
	logFile := filepath.Join(dir, "helper.log")
	resultFile := filepath.Join(dir, "result.json")
	envFile := filepath.Join(dir, "env.txt")
	argsFile := filepath.Join(dir, "args.txt")

	scriptPath := helperScript(t, argsFile, envFile, helperValidResultJSON)
	ex := executor.New(executor.Config{Command: scriptPath, IdleTimeout: 5 * time.Second})

	if _, err := ex.RunHelperAgent(context.Background(), "t1", "reviewer", "do the thing", logFile, resultFile); err != nil {
		t.Fatalf("RunHelperAgent: %v", err)
	}

	envData, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read env: %v", err)
	}
	got := string(envData)
	for _, leaked := range []string{"AFM_SECRET_OTHERCMD=leaked-other-secret", "AFM_SYSPROMPT_OTHERCMD=leaked-other-sysprompt", "AFM_HOOK_SECRET_0_0=leaked-hook-secret"} {
		if envContainsLine(got, leaked) {
			t.Errorf("helper env leaked foreign transport secret %q:\n%s", leaked, got)
		}
	}
}

func TestHelperEnv_KeepsOwnSecret(t *testing.T) {
	t.Setenv("AFM_SECRET_MYHELPER", "own-helper-token")

	wrapDir := t.TempDir()
	dir := t.TempDir()
	logFile := filepath.Join(dir, "helper.log")
	resultFile := filepath.Join(dir, "result.json")
	envFile := filepath.Join(dir, "env.txt")
	argsFile := filepath.Join(dir, "args.txt")

	script := "#!/bin/bash\n" +
		`echo "$@" > ` + argsFile + "\n" +
		`env > ` + envFile + "\n" +
		emitAssistantTextScript(t, helperValidResultJSON) + "\n" +
		resultSuccessLine() + "\n"
	scriptPath := filepath.Join(wrapDir, "myhelper")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	ex := executor.New(executor.Config{Command: "myhelper", WrapperDir: wrapDir, IdleTimeout: 5 * time.Second})
	if _, err := ex.RunHelperAgent(context.Background(), "t1", "reviewer", "do the thing", logFile, resultFile); err != nil {
		t.Fatalf("RunHelperAgent: %v", err)
	}

	envData, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read env: %v", err)
	}
	if !envContainsLine(string(envData), "AFM_SECRET_MYHELPER=own-helper-token") {
		t.Errorf("helper must keep its OWN secret AFM_SECRET_MYHELPER, got:\n%s", envData)
	}
}

// TestHelperEnv_ScratchDir: a helper learns where its own notes directory is through AFM_SCRATCH_DIR; a
// helper without one gets no such variable, even if afm's own environment carries it.
func TestHelperEnv_ScratchDir(t *testing.T) {
	t.Setenv("AFM_SCRATCH_DIR", "/inherited/scratch")
	for _, scratch := range []string{"/run/assistants/t1/scratch", ""} {
		dir := t.TempDir()
		envFile := filepath.Join(dir, "env.txt")
		scriptPath := helperScript(t, filepath.Join(dir, "args.txt"), envFile, helperValidResultJSON)
		ex := executor.New(executor.Config{Command: scriptPath, IdleTimeout: 5 * time.Second, ScratchDir: scratch})
		if _, err := ex.RunHelperAgent(context.Background(), "t1", "reviewer", "do the thing", filepath.Join(dir, "helper.log"), filepath.Join(dir, "result.json")); err != nil {
			t.Fatalf("RunHelperAgent: %v", err)
		}
		envData, err := os.ReadFile(envFile)
		if err != nil {
			t.Fatal(err)
		}
		got := string(envData)
		if scratch != "" && !envContainsLine(got, "AFM_SCRATCH_DIR="+scratch) {
			t.Errorf("AFM_SCRATCH_DIR=%s missing from the helper env:\n%s", scratch, got)
		}
		if strings.Contains(got, "/inherited/scratch") {
			t.Errorf("an inherited AFM_SCRATCH_DIR must not reach the helper:\n%s", got)
		}
	}
}

func TestHelperEnv_DropsBrowserTransport(t *testing.T) {
	t.Setenv("AFM_BROWSER_DESCRIPTOR", "/leaked/descriptor")
	t.Setenv("AFM_BROWSER_TRANSPORT_TOKEN", "leaked-token")

	dir := t.TempDir()
	logFile := filepath.Join(dir, "helper.log")
	resultFile := filepath.Join(dir, "result.json")
	envFile := filepath.Join(dir, "env.txt")
	argsFile := filepath.Join(dir, "args.txt")

	scriptPath := helperScript(t, argsFile, envFile, helperValidResultJSON)
	ex := executor.New(executor.Config{Command: scriptPath, IdleTimeout: 5 * time.Second})

	if _, err := ex.RunHelperAgent(context.Background(), "t1", "reviewer", "do the thing", logFile, resultFile); err != nil {
		t.Fatalf("RunHelperAgent: %v", err)
	}

	envData, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read env: %v", err)
	}
	if strings.Contains(string(envData), "AFM_BROWSER_") {
		t.Errorf("no AFM_BROWSER_* must reach a helper agent, got:\n%s", envData)
	}
}

func TestHelperEnv_NoStageDir(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "helper.log")
	resultFile := filepath.Join(dir, "result.json")
	envFile := filepath.Join(dir, "env.txt")
	argsFile := filepath.Join(dir, "args.txt")

	scriptPath := helperScript(t, argsFile, envFile, helperValidResultJSON)
	ex := executor.New(executor.Config{Command: scriptPath, IdleTimeout: 5 * time.Second, StageDir: "/leaked/stage/dir"})

	if _, err := ex.RunHelperAgent(context.Background(), "t1", "reviewer", "do the thing", logFile, resultFile); err != nil {
		t.Fatalf("RunHelperAgent: %v", err)
	}

	envData, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read env: %v", err)
	}
	if strings.Contains(string(envData), "AFM_STAGE_DIR=") {
		t.Errorf("helper must not receive AFM_STAGE_DIR, got:\n%s", envData)
	}
}

func TestHelperRun_FreshSessionAlways(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "helper.log")
	resultFile := filepath.Join(dir, "result.json")
	envFile := filepath.Join(dir, "env.txt")
	argsFile := filepath.Join(dir, "args.txt")

	scriptPath := helperScript(t, argsFile, envFile, helperValidResultJSON)
	ex := executor.New(executor.Config{
		Command:     scriptPath,
		IdleTimeout: 5 * time.Second,
		SessionID:   "AUTHOR-SESSION-SHOULD-NOT-LEAK",
		Resume:      true,
	})

	if _, err := ex.RunHelperAgent(context.Background(), "t1", "reviewer", "do the thing", logFile, resultFile); err != nil {
		t.Fatalf("RunHelperAgent: %v", err)
	}

	argsData, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	args := string(argsData)
	if strings.Contains(args, "--resume") {
		t.Errorf("argv must not contain --resume: %q", args)
	}
	if strings.Contains(args, "AUTHOR-SESSION-SHOULD-NOT-LEAK") {
		t.Errorf("argv must not contain the reused session id: %q", args)
	}
	if strings.Contains(args, "--session-id") {
		t.Errorf("argv must not contain --session-id for a fresh helper run: %q", args)
	}
}

func TestHelperRun_HostSetsCodexVerifyEnv(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "helper.log")
	resultFile := filepath.Join(dir, "result.json")
	envFile := filepath.Join(dir, "env.txt")
	argsFile := filepath.Join(dir, "args.txt")

	scriptPath := helperScript(t, argsFile, envFile, helperValidResultJSON)
	ex := executor.New(executor.Config{Command: scriptPath, IdleTimeout: 5 * time.Second})

	if _, err := ex.RunHelperAgent(context.Background(), "t1", "reviewer", "do the thing", logFile, resultFile); err != nil {
		t.Fatalf("RunHelperAgent: %v", err)
	}

	envData, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read env: %v", err)
	}
	// Full key=value comparison: CODEX_VERIFY=10 must not false-match CODEX_VERIFY=1.
	if !envContainsLine(string(envData), "CODEX_VERIFY=1") {
		t.Errorf("helper subprocess env missing exact CODEX_VERIFY=1, got:\n%s", envData)
	}
}

// TestVerifyEnv_Unchanged is the regression guard for T3.1's extraction: every pre-existing VerifyMode env
// behavior (own secret kept, foreign secrets/credentials dropped, browser dropped, CODEX_VERIFY=1 set) must
// still hold byte-for-byte after filterInheritedEnv was refactored to switch on effectiveMode() instead of
// cfg.VerifyMode directly.
func TestVerifyEnv_Unchanged(t *testing.T) {
	t.Setenv("AFM_SECRET_OTHERCMD", "leaked-other-secret")
	t.Setenv("AFM_BROWSER_DESCRIPTOR", "/leaked/descriptor")
	t.Setenv("AFM_SECRET_MYVERIFIER", "own-secret-value")

	wrapDir := t.TempDir()
	dir := t.TempDir()
	logFile := filepath.Join(dir, "verify.log")
	resultFile := filepath.Join(dir, "result.json")
	envFile := filepath.Join(dir, "env.txt")

	script := "#!/bin/bash\n" +
		`env > ` + envFile + "\n" +
		emitAssistantTextScript(t, `{"schema_version":1,"verdict":"pass","summary":"ok","findings":[]}`) + "\n" +
		resultSuccessLine() + "\n"
	scriptPath := filepath.Join(wrapDir, "myverifier")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	ex := executor.New(executor.Config{Command: "myverifier", WrapperDir: wrapDir, IdleTimeout: 5 * time.Second})
	if _, err := ex.RunVerifyAgent(context.Background(), "codex", "s1", "verify the stage", logFile, resultFile); err != nil {
		t.Fatalf("RunVerifyAgent: %v", err)
	}

	envData, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read env: %v", err)
	}
	got := string(envData)
	if envContainsLine(got, "AFM_SECRET_OTHERCMD=leaked-other-secret") {
		t.Errorf("verify env leaked foreign secret:\n%s", got)
	}
	if strings.Contains(got, "AFM_BROWSER_") {
		t.Errorf("verify env leaked browser transport:\n%s", got)
	}
	if !envContainsLine(got, "AFM_SECRET_MYVERIFIER=own-secret-value") {
		t.Errorf("verify env must keep its own secret AFM_SECRET_MYVERIFIER:\n%s", got)
	}
	if !envContainsLine(got, "CODEX_VERIFY=1") {
		t.Errorf("verify env must still set CODEX_VERIFY=1:\n%s", got)
	}
}

// TestRunHelperAgent_RedactsEverythingItWrites: a secret the agent prints — in a tool call, an error line, on
// stderr or in its final answer — never reaches the raw stream, the action log, the stderr log, the raw answer
// file, OnAction or the returned RawText. Redact is called at write time.
func TestRunHelperAgent_RedactsEverythingItWrites(t *testing.T) {
	const secret = "s3cr3t-value"
	dir := t.TempDir()
	logFile := filepath.Join(dir, "agent.log")
	resultFile := filepath.Join(dir, "result.json.raw")
	toolLine := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"echo ` + secret + `"}}]}}`
	script := "#!/bin/bash\n" +
		"cat <<'EOF'\n" + toolLine + "\nEOF\n" +
		`echo 'Error: token ` + secret + ` rejected'` + "\n" +
		`echo 'stderr carries ` + secret + `' >&2` + "\n" +
		emitAssistantTextScript(t, `{"schema_version":1,"kind":"result","verdict":"pass","summary":"saw `+secret+`"}`) + "\n" +
		resultSuccessLine() + "\n"
	scriptPath := filepath.Join(dir, "agent.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	var actions []string
	ex := executor.New(executor.Config{
		Command: scriptPath, IdleTimeout: 5 * time.Second,
		Redact:   func(s string) string { return strings.ReplaceAll(s, secret, "[REDACTED]") },
		OnAction: func(tool, detail string) { actions = append(actions, tool+" "+detail) },
	})

	outcome, err := ex.RunHelperAgent(context.Background(), "t1", "reviewer", "review", logFile, resultFile)
	if err != nil {
		t.Fatalf("RunHelperAgent: %v", err)
	}
	if strings.Contains(outcome.RawText, secret) {
		t.Errorf("RawText carries the secret: %s", outcome.RawText)
	}
	for _, a := range actions {
		if strings.Contains(a, secret) {
			t.Errorf("OnAction got the secret: %s", a)
		}
	}
	for _, f := range []string{logFile, filepath.Join(dir, "agent.jsonl"), filepath.Join(dir, "agent.stderr.log"), resultFile} {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if strings.Contains(string(raw), secret) {
			t.Errorf("%s carries the secret:\n%s", filepath.Base(f), raw)
		}
		if !strings.Contains(string(raw), "[REDACTED]") {
			t.Errorf("%s lacks the redaction marker — was the line written at all?\n%s", filepath.Base(f), raw)
		}
	}
}

// TestRunHelperAgent_BackgroundChildHoldingStderrDoesNotHang: with Redact set, stderr goes through a pipe; a
// background process the agent started that keeps that pipe open must not hold the turn once the agent exited.
func TestRunHelperAgent_BackgroundChildHoldingStderrDoesNotHang(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/bash\n" +
		"(sleep 30 >/dev/null &)\n" +
		emitAssistantTextScript(t, helperValidResultJSON) + "\n" +
		resultSuccessLine() + "\n"
	scriptPath := filepath.Join(dir, "agent.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ex := executor.New(executor.Config{Command: scriptPath, IdleTimeout: 60 * time.Second, Redact: func(s string) string { return s }})

	start := time.Now()
	outcome, err := ex.RunHelperAgent(context.Background(), "t1", "reviewer", "review", filepath.Join(dir, "agent.log"), filepath.Join(dir, "raw"))
	if err != nil {
		t.Fatalf("RunHelperAgent: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Fatalf("the turn waited %s for a background child's stderr", elapsed)
	}
	if !outcome.ProcessOK {
		t.Fatalf("outcome = %+v, want a clean finish", outcome)
	}
}

// runHelperScript runs body as a helper agent in a fresh directory and returns the outcome and that directory.
func runHelperScript(t *testing.T, cfg executor.Config, body string) (executor.HelperOutcome, string) {
	t.Helper()
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "agent.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg.Command = scriptPath
	cfg.IdleTimeout = 10 * time.Second
	outcome, err := executor.New(cfg).RunHelperAgent(context.Background(), "t1", "reviewer", "review",
		filepath.Join(dir, "agent.log"), filepath.Join(dir, "result.json.raw"))
	if err != nil {
		t.Fatalf("RunHelperAgent: %v", err)
	}
	return outcome, dir
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// TestRunHelperAgent_RedactsJSONEscapedSecrets: a secret with a quote or a backslash is written JSON-escaped
// — once in the inner answer, twice in the stream line that carries it. Neither escaped form, nor the decoded
// value, may survive in agent.jsonl, the raw answer file or RawText. Redact here is a plain text replacer, so
// only decoding the JSON can find the escaped forms.
func TestRunHelperAgent_RedactsJSONEscapedSecrets(t *testing.T) {
	for name, secret := range map[string]string{"quote": `ab"cd-tok`, "backslash": `ab\cd-tok`} {
		t.Run(name, func(t *testing.T) {
			answer, err := json.Marshal(map[string]any{"schema_version": 1, "kind": "result", "verdict": "pass", "summary": "saw " + secret})
			if err != nil {
				t.Fatal(err)
			}
			cfg := executor.Config{Redact: func(s string) string { return strings.ReplaceAll(s, secret, "[REDACTED]") }}
			outcome, dir := runHelperScript(t, cfg, emitAssistantTextScript(t, string(answer))+"\n"+resultSuccessLine()+"\n")

			escaped, err := json.Marshal(secret)
			if err != nil {
				t.Fatal(err)
			}
			once := string(escaped[1 : len(escaped)-1])
			twice, err := json.Marshal(once)
			if err != nil {
				t.Fatal(err)
			}
			forms := []string{secret, once, string(twice[1 : len(twice)-1])}
			sinks := map[string]string{
				"RawText":     outcome.RawText,
				"raw answer":  readFile(t, filepath.Join(dir, "result.json.raw")),
				"agent.jsonl": readFile(t, filepath.Join(dir, "agent.jsonl")),
			}
			for sink, got := range sinks {
				for _, form := range forms {
					if strings.Contains(got, form) {
						t.Errorf("%s carries the secret as %q:\n%s", sink, form, got)
					}
				}
				if !strings.Contains(got, "[REDACTED]") {
					t.Errorf("%s lacks the redaction marker:\n%s", sink, got)
				}
			}
			if !json.Valid([]byte(outcome.RawText)) {
				t.Errorf("the redacted answer is no longer JSON: %s", outcome.RawText)
			}
		})
	}
}

// TestRunHelperAgent_RedactsSecretsAcrossStderrFraming: with Secrets known, stderr is redacted as a stream,
// not line by line — a secret split across two writes, one split across the old 64 KiB flush point, and a
// multiline secret never reach agent.stderr.log.
func TestRunHelperAgent_RedactsSecretsAcrossStderrFraming(t *testing.T) {
	cases := map[string]struct {
		secret string
		stderr string // bash that writes the secret to stderr
	}{
		"split across writes": {
			secret: "split-secret-value",
			stderr: "printf 'before split-sec' >&2\nsleep 0.2\nprintf 'ret-value after\\n' >&2\n",
		},
		"split across the 64 KiB flush": {
			secret: "flush-secret-value",
			stderr: "head -c 65530 /dev/zero | tr '\\0' x >&2\nprintf 'flush-sec' >&2\nsleep 0.2\nprintf 'ret-value\\n' >&2\n",
		},
		"multiline": {
			secret: "first-line\nsecond-line",
			stderr: "printf 'key: first-line\\nsecond-line end\\n' >&2\n",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := executor.Config{Secrets: func() []string { return []string{tc.secret} }}
			_, dir := runHelperScript(t, cfg, tc.stderr+emitAssistantTextScript(t, helperValidResultJSON)+"\n"+resultSuccessLine()+"\n")
			got := readFile(t, filepath.Join(dir, "agent.stderr.log"))
			if strings.Contains(got, tc.secret) {
				t.Errorf("agent.stderr.log carries the secret:\n%.300s", got[max(0, len(got)-300):])
			}
			if !strings.Contains(got, "[REDACTED]") {
				t.Errorf("agent.stderr.log lacks the redaction marker:\n%.300s", got[max(0, len(got)-300):])
			}
		})
	}
}

// TestRunHelperAgent_RedactsToolNames: a model-supplied tool name carrying a secret is masked before both
// agent.log and OnAction.
func TestRunHelperAgent_RedactsToolNames(t *testing.T) {
	const secret = "tool-secret-value"
	toolLine := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash` + secret + `","input":{"command":"ls"}}]}}`
	var actions []string
	cfg := executor.Config{
		Redact:   func(s string) string { return strings.ReplaceAll(s, secret, "[REDACTED]") },
		OnAction: func(tool, detail string) { actions = append(actions, tool+" "+detail) },
	}
	_, dir := runHelperScript(t, cfg, "cat <<'EOF'\n"+toolLine+"\nEOF\n"+emitAssistantTextScript(t, helperValidResultJSON)+"\n"+resultSuccessLine()+"\n")
	if len(actions) == 0 {
		t.Fatal("OnAction was never called")
	}
	for _, a := range actions {
		if strings.Contains(a, secret) {
			t.Errorf("OnAction got the secret: %s", a)
		}
	}
	if got := readFile(t, filepath.Join(dir, "agent.log")); strings.Contains(got, secret) {
		t.Errorf("agent.log carries the secret:\n%s", got)
	}
}

// TestRunHelperAgent_RedactsEscapedSecretInProse: an answer that is not one JSON value (prose around the
// envelope) cannot be decoded, so with known values the JSON-escaped spelling of a secret is masked as text.
func TestRunHelperAgent_RedactsEscapedSecretInProse(t *testing.T) {
	const value = `ab"cd\ef-tok`
	answer := `Here is my answer: {"summary":"saw ab\"cd\\ef-tok"}`
	cfg := executor.Config{Secrets: func() []string { return []string{value} }}
	outcome, _ := runHelperScript(t, cfg, emitAssistantTextScript(t, answer)+"\n"+resultSuccessLine()+"\n")
	if strings.Contains(outcome.RawText, `ab\"cd\\ef-tok`) || !strings.Contains(outcome.RawText, "[REDACTED]") {
		t.Errorf("RawText = %s, want the escaped secret masked", outcome.RawText)
	}
}
