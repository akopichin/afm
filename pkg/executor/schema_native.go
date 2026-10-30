package executor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/akopichin/afm/pkg/config"
)

const (
	streamEventResult = "result" // stream-json event type that carries a run's final answer
	schemaTypeObject  = "object" // JSON Schema "type" of an object
)

// SchemaRuntime is the CLI family whose own structured-output mode a format-only call may use. Only the families
// checked against the installed CLIs are listed; any other command keeps the schema in the prompt.
type SchemaRuntime string

const (
	SchemaRuntimeClaude SchemaRuntime = "claude" // claude CLI: --json-schema, the answer arrives in result.structured_output
	SchemaRuntimeCodex  SchemaRuntime = "codex"  // codex-as-claude: --output-schema, the answer is the final message
)

// NativeSchema asks one format-only call to constrain its final answer with the CLI's own schema mode. It is an
// extra on top of the schema in the prompt and of the server-side validation, never a replacement for either.
type NativeSchema struct {
	Runtime SchemaRuntime // set by the caller, who knows the command's family
	Schema  string        // JSON Schema of the answer
}

// NativeSchemaFallback says why a requested native schema mode was not used. The call then runs with the schema
// in the prompt alone.
type NativeSchemaFallback string

const (
	NativeSchemaUnknownRuntime     NativeSchemaFallback = "unknown_runtime"     // the caller did not name a verified CLI family
	NativeSchemaUnverifiedCommand  NativeSchemaFallback = "unverified_command"  // a recipe or wrapper, not the claude CLI itself (GLM)
	NativeSchemaUnverifiedProvider NativeSchemaFallback = "unverified_provider" // claude is pointed at a provider other than Anthropic
	NativeSchemaUnsupportedShape   NativeSchemaFallback = "unsupported_shape"   // the schema uses constructs the provider API does not take
)

// OutputSchemaFileEnv names the file with the JSON Schema the shim passes to `codex exec --output-schema`.
const OutputSchemaFileEnv = "AFM_OUTPUT_SCHEMA_FILE"

// thirdPartyProviderEnv switches the claude CLI to a provider whose structured-output support is not verified.
var thirdPartyProviderEnv = []string{"ANTHROPIC_BASE_URL", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"}

// claudeSettingsSelectProvider reports whether a settings file the claude CLI reads may redirect it to another
// provider through its "env" block: the user's, the project's (under the agent's directory) and the managed one.
// A file that exists but cannot be understood counts as a redirect: the provider is then unverified.
func claudeSettingsSelectProvider(agentDir string) bool {
	userDir := os.Getenv("CLAUDE_CONFIG_DIR")
	if home, err := os.UserHomeDir(); userDir == "" && err == nil {
		userDir = filepath.Join(home, ".claude")
	}
	files := []string{
		filepath.Join(userDir, "settings.json"),
		filepath.Join(agentDir, ".claude", "settings.json"),
		filepath.Join(agentDir, ".claude", "settings.local.json"),
		"/etc/claude-code/managed-settings.json",
		"/Library/Application Support/ClaudeCode/managed-settings.json",
	}
	return slices.ContainsFunc(files, settingsFileSelectsProvider)
}

func settingsFileSelectsProvider(path string) bool {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	var settings struct {
		Env map[string]any `json:"env"`
	}
	if err != nil || json.Unmarshal(data, &settings) != nil {
		return true
	}
	return slices.ContainsFunc(thirdPartyProviderEnv, func(name string) bool {
		value, set := settings.Env[name]
		return set && value != "" && value != nil
	})
}

// nativeSchemaFallback reports why the requested native mode cannot be used, or "" when it can. Verified means
// the CLI/provider pair was exercised end to end; anything unknown is a fallback.
func (e *Executor) nativeSchemaFallback(ns *NativeSchema) NativeSchemaFallback {
	switch ns.Runtime {
	case SchemaRuntimeClaude:
		// A recipe (GLM and the like) is a wrapper around claude, and the wrapper decides the provider.
		if e.cfg.Command != config.ClaudeCommand || e.commandPath() != e.cfg.Command {
			return NativeSchemaUnverifiedCommand
		}
		if slices.ContainsFunc(thirdPartyProviderEnv, func(name string) bool { return os.Getenv(name) != "" }) ||
			claudeSettingsSelectProvider(e.cfg.Dir) {
			return NativeSchemaUnverifiedProvider
		}
		if !objectRootSchema(ns.Schema) {
			return NativeSchemaUnsupportedShape
		}
	case SchemaRuntimeCodex:
		if !strictSchema(ns.Schema) {
			return NativeSchemaUnsupportedShape
		}
	default:
		return NativeSchemaUnknownRuntime
	}
	return ""
}

// selectNativeSchema decides, once per helper call, whether the requested native mode is used; the result is
// kept in cfg for run. It returns the fallback reason, "" when native mode is used or was not requested.
func (e *Executor) selectNativeSchema() NativeSchemaFallback {
	if e.cfg.NativeSchema == nil {
		return ""
	}
	if reason := e.nativeSchemaFallback(e.cfg.NativeSchema); reason != "" {
		return reason
	}
	e.cfg.nativeSchemaOn = true
	return ""
}

// claudeNativeSchema reports whether the call runs in Claude's native mode, where the answer is the payload of the
// result event. In Codex's mode the final message is the answer, so there is nothing separate to take.
func (c Config) claudeNativeSchema() bool {
	return c.nativeSchemaOn && c.NativeSchema.Runtime == SchemaRuntimeClaude
}

// nativeSchemaRun is what native mode adds to one process: command-line flags (claude) or an environment variable
// naming a schema file (the codex shim). The zero value adds nothing.
type nativeSchemaRun struct {
	args, env []string
	cleanup   func()
}

// startNativeSchema prepares the schema for the process; the caller defers cleanup.
func (e *Executor) startNativeSchema() (nativeSchemaRun, error) {
	if !e.cfg.nativeSchemaOn {
		return nativeSchemaRun{cleanup: func() {}}, nil
	}
	ns := e.cfg.NativeSchema
	if ns.Runtime == SchemaRuntimeClaude {
		var compact bytes.Buffer
		if err := json.Compact(&compact, []byte(ns.Schema)); err != nil {
			return nativeSchemaRun{}, fmt.Errorf("native schema: %w", err)
		}
		return nativeSchemaRun{args: []string{"--json-schema", compact.String()}, cleanup: func() {}}, nil
	}
	dir, err := os.MkdirTemp("", "afm-output-schema-")
	if err != nil {
		return nativeSchemaRun{}, fmt.Errorf("native schema: %w", err)
	}
	path := filepath.Join(dir, "schema.json")
	if err := os.WriteFile(path, []byte(ns.Schema), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return nativeSchemaRun{}, fmt.Errorf("native schema: %w", err)
	}
	return nativeSchemaRun{env: []string{OutputSchemaFileEnv + "=" + path}, cleanup: func() { _ = os.RemoveAll(dir) }}, nil
}

// structuredPayload returns the answer of a Claude result event written under --json-schema. The payload is only
// in that event: no text block carries it. A failed or payload-less result gives none, so nothing is invented.
func structuredPayload(line string) (string, bool) {
	var ev struct {
		Type             string          `json:"type"`
		IsError          bool            `json:"is_error"`
		StructuredOutput json.RawMessage `json:"structured_output"`
	}
	if json.Unmarshal([]byte(line), &ev) != nil || ev.Type != streamEventResult || ev.IsError {
		return "", false
	}
	if len(ev.StructuredOutput) == 0 || bytes.Equal(ev.StructuredOutput, []byte("null")) {
		return "", false
	}
	return string(ev.StructuredOutput), true
}

// Claude's structured-output tool rejects root combinators even alongside type: object.
// Nested combinators do not impose that restriction on the tool's input schema.
func objectRootSchema(schema string) bool {
	var root map[string]any
	if json.Unmarshal([]byte(schema), &root) != nil || root["type"] != schemaTypeObject {
		return false
	}
	for _, key := range []string{"oneOf", "allOf", "anyOf"} {
		if _, exists := root[key]; exists {
			return false
		}
	}
	return true
}

// strictKeywords are the constructs OpenAI's strict structured output does not take; a schema using one stays
// in the prompt.
var strictKeywords = []string{"oneOf", "allOf", "not", "if", "then", "else", "patternProperties", "$ref"}

// strictSchema reports whether the schema fits OpenAI's strict mode, which `codex exec --output-schema` always
// uses: every object is closed and lists all its properties as required.
func strictSchema(schema string) bool {
	var root map[string]any
	return json.Unmarshal([]byte(schema), &root) == nil && root["type"] == schemaTypeObject && strictNode(root)
}

// strictNode checks one schema node and the schemas nested in it. Only schema positions are walked, so a property
// that happens to be named like a keyword is not mistaken for one.
func strictNode(node any) bool {
	n, ok := node.(map[string]any)
	if !ok {
		return true
	}
	if slices.ContainsFunc(strictKeywords, func(keyword string) bool { _, found := n[keyword]; return found }) {
		return false
	}
	properties, _ := n["properties"].(map[string]any)
	if allowsObject(n["type"]) && !closedWithAllRequired(n, properties) {
		return false
	}
	nested := []any{n["items"]}
	for _, key := range []string{"properties", "definitions", "$defs"} {
		group, _ := n[key].(map[string]any)
		nested = slices.AppendSeq(nested, maps.Values(group))
	}
	anyOf, _ := n["anyOf"].([]any)
	nested = append(nested, anyOf...)
	return !slices.ContainsFunc(nested, func(child any) bool { return !strictNode(child) })
}

// allowsObject reports whether a schema "type" is, or lists, "object".
func allowsObject(typeValue any) bool {
	if list, isList := typeValue.([]any); isList {
		return slices.Contains(list, any(schemaTypeObject))
	}
	return typeValue == schemaTypeObject
}

// closedWithAllRequired reports whether an object schema forbids extra properties and requires every property.
func closedWithAllRequired(object, props map[string]any) bool {
	required, _ := object["required"].([]any)
	if closed, isBool := object["additionalProperties"].(bool); !isBool || closed || len(required) != len(props) {
		return false
	}
	for _, name := range required {
		key, _ := name.(string)
		if _, found := props[key]; !found {
			return false
		}
	}
	return true
}
