package executor

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/orchestrator/verify"
	"github.com/akopichin/afm/schema"
)

const verdictOKJSON = `{"schema_version":1,"verdict":"pass","summary":"ok"}`

type verifyRun struct {
	outcome verify.RunOutcome
	err     error
	result  string
}

func runVerify(t *testing.T, cfg Config) verifyRun {
	t.Helper()
	dir := t.TempDir()
	logFile, resultFile := filepath.Join(dir, "agent.log"), filepath.Join(dir, "raw-result.json")
	cfg.IdleTimeout = 30 * time.Second
	var got verifyRun
	got.outcome, got.err = New(cfg).RunVerifyAgent(context.Background(), "claude", "s1", "verify it", logFile, resultFile)
	data, _ := os.ReadFile(resultFile)
	got.result = string(data)
	return got
}

func claudeVerifySchema(t *testing.T) *NativeSchema {
	t.Helper()
	raw, ok := schema.NativeJSON(schema.VerifyResult, schema.DialectClaude)
	if !ok {
		t.Fatal("verify-result has no claude variant")
	}
	return &NativeSchema{Runtime: SchemaRuntimeClaude, Schema: raw}
}

// A native verify run decodes the CLI's structured payload: reasoning text before it is not the answer.
func TestRunVerifyAgent_NativeClaudeTakesTheStructuredPayload(t *testing.T) {
	argsFile := fakeClaudeOnPath(t, `echo '{"type":"assistant","message":{"content":[{"type":"text","text":"Let me check the files first."}]}}'
echo '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_1","name":"StructuredOutput","input":`+verdictOKJSON+`}]}}'
echo '{"type":"result","subtype":"success","is_error":false,"structured_output":`+verdictOKJSON+`}'`)
	got := runVerify(t, Config{NativeSchema: claudeVerifySchema(t)})

	if got.err != nil {
		t.Fatalf("RunVerifyAgent: %v", got.err)
	}
	o := got.outcome
	if !o.ProcessOK || o.ProtocolErr != nil || o.Result == nil || o.Result.Verdict != verify.VerdictPass {
		t.Fatalf("outcome = %+v, want the decoded payload", o)
	}
	if !o.NativeStarted || !o.StructuredPayload || o.NativeEnforced || o.NativeFallback != "" {
		t.Fatalf("outcome = %+v, want a volunteered native payload", o)
	}
	if got.result != verdictOKJSON {
		t.Fatalf("raw result = %q, want the payload", got.result)
	}
	if args := fileLines(t, argsFile); !slices.Contains(args, "--json-schema") {
		t.Fatalf("args = %q, want --json-schema", args)
	}
}

// The executor reports a forced pass as it is; refusing it is the gate's decision.
func TestRunVerifyAgent_ForcedPassIsReportedAsEnforced(t *testing.T) {
	fakeClaudeOnPath(t, claudeEnforcedStream(verdictOKJSON))
	got := runVerify(t, Config{NativeSchema: claudeVerifySchema(t)})

	o := got.outcome
	if !o.NativeEnforced || !o.StructuredPayload || o.Result == nil || o.Result.Verdict != verify.VerdictPass {
		t.Fatalf("outcome = %+v, want the forced pass flagged as enforced", o)
	}
}

// A provider refusing native mode is a failed process that started in native mode, never an answer.
func TestRunVerifyAgent_NativeRefusalIsAFailedNativeProcess(t *testing.T) {
	fakeClaudeOnPath(t, `echo '{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error","api_error_status":400,"result":"API Error: 400 schema rejected"}'
exit 1`)
	got := runVerify(t, Config{NativeSchema: claudeVerifySchema(t)})

	o := got.outcome
	if o.ProcessOK || !o.NativeStarted || o.Result != nil || o.StructuredPayload {
		t.Fatalf("outcome = %+v, want a failed process that started in native mode", o)
	}
}

// An unsupported request runs prompt-only and says why; the text answer is decoded as before.
func TestRunVerifyAgent_UnsupportedNativeRequestRunsPromptOnly(t *testing.T) {
	argsFile := fakeClaudeOnPath(t, `cat <<'EOF'
{"type":"assistant","message":{"content":[{"type":"text","text":"`+strings.ReplaceAll(verdictOKJSON, `"`, `\"`)+`"}]}}
EOF
echo '{"type":"result","subtype":"success"}'`)
	got := runVerify(t, Config{NativeSchema: &NativeSchema{Runtime: SchemaRuntimeClaude, Schema: schema.JSON(schema.VerifyResult)}})

	o := got.outcome
	if o.NativeFallback != string(NativeSchemaUnsupportedShape) || o.NativeStarted || o.Result == nil {
		t.Fatalf("outcome = %+v, want a prompt-only run that names the fallback", o)
	}
	if args := fileLines(t, argsFile); slices.Contains(args, "--json-schema") {
		t.Fatalf("args = %q, want no --json-schema", args)
	}
}

// Without a request nothing changes: no flag, no fallback reason, the last text block is the answer.
func TestRunVerifyAgent_WithoutNativeRequestKeepsTheTextAnswer(t *testing.T) {
	argsFile := fakeClaudeOnPath(t, `cat <<'EOF'
{"type":"assistant","message":{"content":[{"type":"text","text":"`+strings.ReplaceAll(verdictOKJSON, `"`, `\"`)+`"}]}}
EOF
echo '{"type":"result","subtype":"success","structured_output":{"schema_version":1,"verdict":"needs_changes","summary":"ignored"}}'`)
	got := runVerify(t, Config{})

	o := got.outcome
	if o.NativeFallback != "" || o.NativeStarted || o.StructuredPayload || o.Result == nil || o.Result.Verdict != verify.VerdictPass {
		t.Fatalf("outcome = %+v, want the text answer of a plain run", o)
	}
	if args := fileLines(t, argsFile); slices.Contains(args, "--json-schema") {
		t.Fatalf("args = %q, want no --json-schema", args)
	}
}

// Codex verify gets the strict variant as a schema file next to CODEX_VERIFY; its final message is the answer.
func TestRunVerifyAgent_CodexGetsTheStrictSchemaFile(t *testing.T) {
	dir := t.TempDir()
	copyFile, envFile := filepath.Join(dir, "schema-copy.json"), filepath.Join(dir, "verify-env.txt")
	script := writeScript(t, dir, "codex-agent", `cp "$`+OutputSchemaFileEnv+`" `+copyFile+"\n"+
		`printf '%s' "$CODEX_VERIFY" > `+envFile+"\n"+
		"cat <<'EOF'\n"+`{"type":"assistant","message":{"content":[{"type":"text","text":"`+strings.ReplaceAll(verdictOKJSON, `"`, `\"`)+`"}]}}`+"\nEOF\n"+
		`echo '{"type":"result","subtype":"success"}'`+"\n")
	strict, _ := schema.NativeJSON(schema.VerifyResult, schema.DialectStrict)
	got := runVerify(t, Config{Command: script, NativeSchema: &NativeSchema{Runtime: SchemaRuntimeCodex, Schema: strict}})

	o := got.outcome
	if !o.NativeStarted || o.StructuredPayload || o.Result == nil || o.Result.Verdict != verify.VerdictPass {
		t.Fatalf("outcome = %+v, want native mode with the final message as the answer", o)
	}
	if data, err := os.ReadFile(copyFile); err != nil || string(data) != strict {
		t.Fatalf("schema file = %q (%v), want the strict variant", data, err)
	}
	if data, _ := os.ReadFile(envFile); string(data) != "1" {
		t.Fatalf("CODEX_VERIFY = %q, want 1", data)
	}
}
