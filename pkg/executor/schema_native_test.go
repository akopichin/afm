package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/accounting"
	"github.com/akopichin/afm/schema"
)

const (
	// objectSchema is accepted by both runtimes: an object root, every property required, nothing else allowed.
	objectSchema  = `{"type":"object","properties":{"verdict":{"type":"string"}},"required":["verdict"],"additionalProperties":false}`
	optionalField = `{"type":"object","properties":{"verdict":{"type":"string"},"note":{"type":"string"}},"required":["verdict"],"additionalProperties":false}`
	payloadJSON   = `{"verdict":"pass"}`

	// claudeStructuredStream is a Claude CLI 2.1.304 stream for --json-schema: the model calls the synthetic
	// StructuredOutput tool and the payload comes only in the result event, never in a text block.
	claudeStructuredStream = `echo '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_1","name":"StructuredOutput","input":{"verdict":"pass"}}]}}'
echo '{"type":"result","subtype":"success","is_error":false,"result":"{\"verdict\":\"pass\"}","structured_output":{"verdict":"pass"},"usage":{"input_tokens":11,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":7}}'`
)

// fakeClaudeOnPath installs a `claude` that records its argv (one per line) and then runs body.
func fakeClaudeOnPath(t *testing.T, body string) (argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args.txt")
	writeScript(t, dir, "claude", `printf '%s\n' "$@" > `+argsFile+"\n"+body+"\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	userClaudeHome(t)
	for _, name := range thirdPartyProviderEnv {
		t.Setenv(name, "")
	}
	return argsFile
}

type helperRun struct {
	outcome HelperOutcome
	err     error
	log     string
	result  string
	usage   accounting.Observation
}

func runHelper(t *testing.T, cfg Config) helperRun {
	t.Helper()
	dir := t.TempDir()
	logFile, resultFile := filepath.Join(dir, "helper.log"), filepath.Join(dir, "result.json")
	var got helperRun
	cfg.IdleTimeout = 30 * time.Second
	cfg.OnUsage = func(o accounting.Observation) { got.usage = o }
	got.outcome, got.err = New(cfg).RunHelperAgent(context.Background(), "t1", "formatter", "format it", logFile, resultFile)
	logData, _ := os.ReadFile(logFile)
	resultData, _ := os.ReadFile(resultFile)
	got.log, got.result = string(logData), string(resultData)
	return got
}

// Claude gets the schema as one --json-schema flag, and the answer is the CLI's structured payload: the
// stream has no text block with it. Usage still comes from the result event.
func TestNativeSchema_ClaudeTakesTheStructuredPayload(t *testing.T) {
	argsFile := fakeClaudeOnPath(t, claudeStructuredStream)
	got := runHelper(t, Config{NativeSchema: &NativeSchema{Runtime: SchemaRuntimeClaude, Schema: objectSchema}})
	if got.err != nil {
		t.Fatalf("RunHelperAgent: %v", got.err)
	}

	args := fileLines(t, argsFile)
	if i := slices.Index(args, "--json-schema"); i < 0 || i+1 >= len(args) || args[i+1] != objectSchema {
		t.Fatalf("args = %q, want --json-schema followed by the schema", args)
	}
	if !got.outcome.ProcessOK || got.outcome.RawText != payloadJSON || !got.outcome.StructuredPayload || got.outcome.NativeFallback != "" {
		t.Fatalf("outcome = %+v, want the payload as RawText from native mode", got.outcome)
	}
	if got.result != payloadJSON {
		t.Fatalf("result file = %q, want the payload", got.result)
	}
	if got.usage.Tokens.UncachedInput != 11 || got.usage.Tokens.Output != 7 {
		t.Fatalf("usage = %+v, want the result event's tokens", got.usage.Tokens)
	}
}

// A CLI that ends without a structured payload (the model skipped the tool, or the payload failed the schema)
// gives no result: the text answer stays what it was, for the server-side validator to judge.
func TestNativeSchema_ClaudeWithoutPayloadKeepsTheTextAnswer(t *testing.T) {
	fakeClaudeOnPath(t, `echo '{"type":"assistant","message":{"content":[{"type":"text","text":"not json at all"}]}}'
echo '{"type":"result","subtype":"success","is_error":false,"result":"not json at all","structured_output":null}'`)
	got := runHelper(t, Config{NativeSchema: &NativeSchema{Runtime: SchemaRuntimeClaude, Schema: objectSchema}})

	if got.outcome.StructuredPayload || got.outcome.RawText != "not json at all" {
		t.Fatalf("outcome = %+v, want the plain text and no structured payload", got.outcome)
	}
	if !strings.Contains(got.log, "no structured payload") {
		t.Fatalf("log = %q, want a note that native mode returned no payload", got.log)
	}
}

// A provider refusal is an ordinary failed process: the error text is never taken for a structured answer.
func TestNativeSchema_ClaudeRefusalIsAFailureNotAnAnswer(t *testing.T) {
	fakeClaudeOnPath(t, `echo '{"type":"assistant","message":{"content":[{"type":"text","text":"API Error: 400 schema not supported"}]}}'
echo '{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error","api_error_status":400,"result":"API Error: 400 schema not supported","structured_output":null}'
exit 1`)
	got := runHelper(t, Config{NativeSchema: &NativeSchema{Runtime: SchemaRuntimeClaude, Schema: objectSchema}})

	if got.outcome.ProcessOK || got.outcome.ProcessErr == nil || got.outcome.StructuredPayload {
		t.Fatalf("outcome = %+v, want a failed process without a structured payload", got.outcome)
	}
}

// An errored result never counts as a payload even when it carries the field.
func TestNativeSchema_ClaudeIgnoresPayloadOfErroredResult(t *testing.T) {
	fakeClaudeOnPath(t, `echo '{"type":"result","subtype":"success","is_error":true,"structured_output":{"verdict":"pass"}}'
exit 1`)
	got := runHelper(t, Config{NativeSchema: &NativeSchema{Runtime: SchemaRuntimeClaude, Schema: objectSchema}})

	if got.outcome.StructuredPayload || got.outcome.RawText != "" {
		t.Fatalf("outcome = %+v, want no answer", got.outcome)
	}
}

// Anything not verified keeps the schema in the prompt only: no flag, the usual text answer, and the reason is
// reported instead of being silently dropped.
func TestNativeSchema_FallsBackWhenNotVerified(t *testing.T) {
	textAnswer := `echo '{"type":"assistant","message":{"content":[{"type":"text","text":"{\"verdict\":\"pass\"}"}]}}'
echo '{"type":"result","subtype":"success"}'`
	tests := []struct {
		name   string
		schema NativeSchema
		cfg    func(t *testing.T, cfg *Config, argsFile string)
		want   NativeSchemaFallback
	}{
		{name: "unknown runtime", schema: NativeSchema{Schema: objectSchema}, want: NativeSchemaUnknownRuntime},
		{
			name:   "recipe command such as GLM",
			schema: NativeSchema{Runtime: SchemaRuntimeClaude, Schema: objectSchema},
			cfg: func(t *testing.T, cfg *Config, argsFile string) {
				cfg.Command = writeScript(t, t.TempDir(), "glm-agent", `printf '%s\n' "$@" > `+argsFile+"\n"+textAnswer+"\n")
			},
			want: NativeSchemaUnverifiedCommand,
		},
		{
			name:   "claude shadowed by a generated wrapper",
			schema: NativeSchema{Runtime: SchemaRuntimeClaude, Schema: objectSchema},
			cfg: func(t *testing.T, cfg *Config, argsFile string) {
				cfg.WrapperDir = t.TempDir()
				writeScript(t, cfg.WrapperDir, "claude", `printf '%s\n' "$@" > `+argsFile+"\n"+textAnswer+"\n")
			},
			want: NativeSchemaUnverifiedCommand,
		},
		{
			name:   "third-party provider endpoint",
			schema: NativeSchema{Runtime: SchemaRuntimeClaude, Schema: objectSchema},
			cfg: func(t *testing.T, _ *Config, _ string) {
				t.Setenv("ANTHROPIC_BASE_URL", "https://glm.example/api/anthropic")
			},
			want: NativeSchemaUnverifiedProvider,
		},
		{
			name:   "provider endpoint only in the user's Claude settings",
			schema: NativeSchema{Runtime: SchemaRuntimeClaude, Schema: objectSchema},
			cfg: func(t *testing.T, _ *Config, _ string) {
				writeClaudeSettings(t, filepath.Join(userClaudeHome(t), ".claude"), "settings.json", `{"env":{"ANTHROPIC_BASE_URL":"https://glm.example/api/anthropic"}}`)
			},
			want: NativeSchemaUnverifiedProvider,
		},
		{
			name:   "provider endpoint only in CLAUDE_CONFIG_DIR",
			schema: NativeSchema{Runtime: SchemaRuntimeClaude, Schema: objectSchema},
			cfg: func(t *testing.T, _ *Config, _ string) {
				configDir := t.TempDir()
				writeClaudeSettings(t, configDir, "settings.json", `{"env":{"ANTHROPIC_BASE_URL":"https://glm.example/api/anthropic"}}`)
				t.Setenv("CLAUDE_CONFIG_DIR", configDir)
			},
			want: NativeSchemaUnverifiedProvider,
		},
		{
			name:   "provider endpoint only in the project's local Claude settings",
			schema: NativeSchema{Runtime: SchemaRuntimeClaude, Schema: objectSchema},
			cfg: func(t *testing.T, cfg *Config, _ string) {
				userClaudeHome(t)
				cfg.Dir = t.TempDir()
				writeClaudeSettings(t, filepath.Join(cfg.Dir, ".claude"), "settings.local.json", `{"env":{"CLAUDE_CODE_USE_BEDROCK":"1"}}`)
			},
			want: NativeSchemaUnverifiedProvider,
		},
		{
			name:   "Claude settings that cannot be read",
			schema: NativeSchema{Runtime: SchemaRuntimeClaude, Schema: objectSchema},
			cfg: func(t *testing.T, _ *Config, _ string) {
				writeClaudeSettings(t, filepath.Join(userClaudeHome(t), ".claude"), "settings.json", `{"env":`)
			},
			want: NativeSchemaUnverifiedProvider,
		},
		{
			name:   "claude schema without an object root",
			schema: NativeSchema{Runtime: SchemaRuntimeClaude, Schema: `{"oneOf":[{"type":"object"}]}`},
			want:   NativeSchemaUnsupportedShape,
		},
		{
			name:   "codex schema with an optional property",
			schema: NativeSchema{Runtime: SchemaRuntimeCodex, Schema: optionalField},
			want:   NativeSchemaUnsupportedShape,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			argsFile := fakeClaudeOnPath(t, textAnswer)
			cfg := Config{NativeSchema: &tt.schema}
			if tt.cfg != nil {
				tt.cfg(t, &cfg, argsFile)
			}
			got := runHelper(t, cfg)

			if got.outcome.NativeFallback != tt.want {
				t.Fatalf("NativeFallback = %q, want %q", got.outcome.NativeFallback, tt.want)
			}
			if args, _ := os.ReadFile(argsFile); strings.Contains(string(args), "--json-schema") {
				t.Fatalf("args = %q: no native flag without a verified capability", args)
			}
			if got.outcome.RawText != payloadJSON || got.outcome.StructuredPayload {
				t.Fatalf("outcome = %+v, want the text answer as before", got.outcome)
			}
		})
	}
}

// Author, verify and planning runs never get the native mode: only the isolated format-only call does.
func TestNativeSchema_AuthorRunIgnoresTheRequest(t *testing.T) {
	argsFile := fakeClaudeOnPath(t, `echo '{"type":"result","subtype":"success"}'`)
	runAuthor(t, Config{NativeSchema: &NativeSchema{Runtime: SchemaRuntimeClaude, Schema: objectSchema}, IdleTimeout: 30 * time.Second})

	if args, _ := os.ReadFile(argsFile); strings.Contains(string(args), "--json-schema") {
		t.Fatalf("args = %q: an author run must not be constrained to a schema", args)
	}
}

// Codex takes the schema as a file named by the environment (the shim turns it into --output-schema); the file
// exists while the process runs and is gone afterwards. The final message already is the answer, so nothing
// else about reading it changes.
func TestNativeSchema_CodexGetsASchemaFile(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "env.txt")
	copyFile := filepath.Join(dir, "schema-copy.json")
	script := writeScript(t, dir, "codex-agent", `printf '%s' "$`+OutputSchemaFileEnv+`" > `+envFile+"\n"+
		`cp "$`+OutputSchemaFileEnv+`" `+copyFile+"\n"+
		`echo '{"type":"assistant","message":{"content":[{"type":"text","text":"{\"verdict\":\"pass\"}"}]}}'`+"\n"+
		`echo '{"type":"result","subtype":"success"}'`+"\n")
	t.Setenv(OutputSchemaFileEnv, "/stale/ambient.json")
	got := runHelper(t, Config{Command: script, NativeSchema: &NativeSchema{Runtime: SchemaRuntimeCodex, Schema: objectSchema}})

	if got.outcome.NativeFallback != "" || got.outcome.RawText != payloadJSON {
		t.Fatalf("outcome = %+v, want native mode and the final message as the answer", got.outcome)
	}
	if strings.Contains(got.log, "no structured payload") {
		t.Fatalf("log = %q: the final message is the answer in Codex mode, not a missing payload", got.log)
	}
	path, _ := os.ReadFile(envFile)
	if string(path) == "" || string(path) == "/stale/ambient.json" {
		t.Fatalf("%s = %q, want the file afm wrote, not the ambient value", OutputSchemaFileEnv, path)
	}
	if data, err := os.ReadFile(copyFile); err != nil || string(data) != objectSchema {
		t.Fatalf("schema file = %q (%v), want the schema", data, err)
	}
	if _, err := os.Stat(string(path)); !os.IsNotExist(err) {
		t.Fatalf("schema file %s survived the run (stat err %v)", path, err)
	}
}

// Without a request, and for unsupported shapes, the codex process sees no schema file at all.
func TestNativeSchema_CodexWithoutSupportGetsNoFile(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "env.txt")
	script := writeScript(t, dir, "codex-agent", `printf '%s' "${`+OutputSchemaFileEnv+`:-unset}" > `+envFile+"\n"+
		`echo '{"type":"result","subtype":"success"}'`+"\n")
	t.Setenv(OutputSchemaFileEnv, "/stale/ambient.json")
	for _, ns := range []*NativeSchema{nil, {Runtime: SchemaRuntimeCodex, Schema: optionalField}} {
		runHelper(t, Config{Command: script, NativeSchema: ns})
		if got, _ := os.ReadFile(envFile); string(got) != "unset" {
			t.Fatalf("%s = %q, want unset (request %+v)", OutputSchemaFileEnv, got, ns)
		}
	}
}

// Only Question has a plain object root; assistant-result uses oneOf, result-only $ref, and verify-result allOf.
// Codex's strict mode also rejects $ref, oneOf/allOf, and optional properties.
func TestNativeSchema_CapabilityOfTheRealContracts(t *testing.T) {
	tests := []struct {
		contract     schema.Contract
		claude, strc bool
	}{
		{schema.AssistantResult, false, false},
		{schema.AssistantResultOnly, false, false},
		{schema.VerifyResult, false, false},
		{schema.Question, true, false},
	}
	for _, tt := range tests {
		raw := schema.JSON(tt.contract)
		if got := objectRootSchema(raw); got != tt.claude {
			t.Errorf("%s: objectRootSchema = %v, want %v", tt.contract, got, tt.claude)
		}
		if got := strictSchema(raw); got != tt.strc {
			t.Errorf("%s: strictSchema = %v, want %v", tt.contract, got, tt.strc)
		}
	}
}

func TestNativeSchema_ClaudeRootCombinators(t *testing.T) {
	fakeClaudeOnPath(t, "")
	e := New(Config{Command: "claude", Dir: t.TempDir()})
	for _, tc := range []struct {
		name, raw string
		want      NativeSchemaFallback
	}{
		{"verify result", schema.JSON(schema.VerifyResult), NativeSchemaUnsupportedShape},
		{"question", schema.JSON(schema.Question), ""},
		{"object allOf", `{"type":"object","allOf":[{"properties":{"a":{"type":"string"}}}]}`, NativeSchemaUnsupportedShape},
		{"object anyOf", `{"type":"object","anyOf":[{"properties":{"a":{"type":"string"}}}]}`, NativeSchemaUnsupportedShape},
		{"object oneOf", `{"type":"object","oneOf":[{"properties":{"a":{"type":"string"}}}]}`, NativeSchemaUnsupportedShape},
		{"nested allOf", `{"type":"object","properties":{"a":{"allOf":[{"type":"string"}]}}}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := e.nativeSchemaFallback(&NativeSchema{Runtime: SchemaRuntimeClaude, Schema: tc.raw}); got != tc.want {
				t.Fatalf("fallback = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStrictSchema(t *testing.T) {
	tests := []struct {
		name, schema string
		want         bool
	}{
		{"flat object", objectSchema, true},
		{"nested object and array of objects", `{"type":"object","properties":{"a":{"type":"object","properties":{"b":{"type":"string"}},"required":["b"],"additionalProperties":false},"c":{"type":"array","items":{"type":"object","properties":{},"required":[],"additionalProperties":false}}},"required":["a","c"],"additionalProperties":false}`, true},
		{"optional property", optionalField, false},
		{"additional properties allowed", `{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}`, false},
		{"nested object not closed", `{"type":"object","properties":{"a":{"type":"object","properties":{"b":{"type":"string"}},"required":["b"]}},"required":["a"],"additionalProperties":false}`, false},
		{"one-of", `{"type":"object","properties":{"a":{"oneOf":[{"type":"string"}]}},"required":["a"],"additionalProperties":false}`, false},
		{"reference", `{"type":"object","properties":{"a":{"$ref":"#/definitions/a"}},"required":["a"],"additionalProperties":false}`, false},
		{"not an object root", `{"type":"string"}`, false},
		{"not JSON", `{`, false},
	}
	for _, tt := range tests {
		if got := strictSchema(tt.schema); got != tt.want {
			t.Errorf("%s: strictSchema = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// The shim hands the schema file to `codex exec --output-schema` for the isolated format-only call (the shim's
// verify mode). An author run never constrains its answer, so the variable is ignored there.
func TestCodexAsClaude_ForwardsOutputSchemaOnlyInFormatOnlyMode(t *testing.T) {
	requireShimTools(t)
	tests := []struct {
		name string
		env  []string
		want bool
	}{
		{"format-only call", []string{"CODEX_VERIFY=1", OutputSchemaFileEnv + "=/run/afm/schema.json"}, true},
		{"author run", []string{OutputSchemaFileEnv + "=/run/afm/schema.json"}, false},
		{"format-only call without a schema", []string{"CODEX_VERIFY=1"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake, argsFile, _ := writeRecordingFakeCodex(t, codexOneTurn)
			if out, err := runCodexScript(t, fake, "go", tt.env...); err != nil {
				t.Fatalf("script failed: %v\n%s", err, out)
			}
			args := fileLines(t, argsFile)
			i := slices.Index(args, "--output-schema")
			if tt.want && (i < 0 || args[i+1] != "/run/afm/schema.json") {
				t.Fatalf("args = %q, want --output-schema /run/afm/schema.json", args)
			}
			if !tt.want && i >= 0 {
				t.Fatalf("args = %q, want no --output-schema", args)
			}
		})
	}
}

// userClaudeHome points the CLI's user configuration at an empty directory, so the developer's own
// ~/.claude/settings.json never decides a test.
func userClaudeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	return home
}

func writeClaudeSettings(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Settings files without a provider override, even with an empty one, leave the verified Anthropic pair intact.
// The CLI's config home is chosen after the fake CLI is installed, which resets it.
func TestNativeSchema_ClaudeSettingsWithoutProviderKeepNativeMode(t *testing.T) {
	const providerFree = `{"env":{"DEBUG":"1","ANTHROPIC_BASE_URL":"","CLAUDE_CODE_USE_BEDROCK":null},"model":"opus"}`
	tests := []struct {
		name  string
		setup func(t *testing.T, cfg *Config)
	}{
		{"no settings at all", func(*testing.T, *Config) {}},
		{"user settings", func(t *testing.T, _ *Config) {
			writeClaudeSettings(t, filepath.Join(userClaudeHome(t), ".claude"), "settings.json", providerFree)
		}},
		{"project settings", func(t *testing.T, cfg *Config) {
			cfg.Dir = t.TempDir()
			writeClaudeSettings(t, filepath.Join(cfg.Dir, ".claude"), "settings.json", providerFree)
			writeClaudeSettings(t, filepath.Join(cfg.Dir, ".claude"), "settings.local.json", `{}`)
		}},
		{"override only in the ignored ~/.claude when CLAUDE_CONFIG_DIR is set", func(t *testing.T, _ *Config) {
			writeClaudeSettings(t, filepath.Join(userClaudeHome(t), ".claude"), "settings.json", `{"env":{"ANTHROPIC_BASE_URL":"https://glm.example/api/anthropic"}}`)
			t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			argsFile := fakeClaudeOnPath(t, claudeStructuredStream)
			cfg := Config{Dir: t.TempDir(), NativeSchema: &NativeSchema{Runtime: SchemaRuntimeClaude, Schema: objectSchema}}
			tt.setup(t, &cfg)

			got := runHelper(t, cfg)

			if got.outcome.NativeFallback != "" {
				t.Fatalf("NativeFallback = %q, want native mode", got.outcome.NativeFallback)
			}
			if args, _ := os.ReadFile(argsFile); !strings.Contains(string(args), "--json-schema") {
				t.Fatalf("args = %q, want --json-schema", args)
			}
		})
	}
}

// Native mode counts as used only when the process started with it: the selection alone is not enough.
func TestNativeSchema_StartedOnlyWhenTheProcessStartsInNativeMode(t *testing.T) {
	refusal := `echo '{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error","api_error_status":400,"result":"API Error: 400 schema rejected"}'
exit 1`
	for _, tc := range []struct {
		name          string
		schema        string
		startFails    bool
		wantStarted   bool
		wantDelivered int
	}{
		{"native process failed", objectSchema, false, true, 1},
		{"start failed in native mode", objectSchema, true, false, 0},
		{"process ran without native mode", schema.JSON(schema.VerifyResult), false, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			argsFile := fakeClaudeOnPath(t, refusal)
			if tc.startFails {
				// Found on PATH, but exec fails after native mode was selected.
				if err := os.WriteFile(filepath.Join(filepath.Dir(argsFile), "claude"), []byte("#!/afm-no-such-interpreter\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			delivered := 0
			got := runHelper(t, Config{NativeSchema: &NativeSchema{Runtime: SchemaRuntimeClaude, Schema: tc.schema}, OnDelivered: func() { delivered++ }})
			if got.outcome.NativeStarted != tc.wantStarted || delivered != tc.wantDelivered || got.outcome.ProcessErr == nil {
				t.Fatalf("outcome=%+v delivered=%d, want NativeStarted=%v delivered=%d", got.outcome, delivered, tc.wantStarted, tc.wantDelivered)
			}
			if tc.startFails != errors.Is(got.outcome.ProcessErr, ErrAgentStart) {
				t.Fatalf("ProcessErr=%v, ErrAgentStart expected only for the start failure", got.outcome.ProcessErr)
			}
		})
	}
}
