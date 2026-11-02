package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/akopichin/afm/schema"
)

const (
	streamEventResult = "result" // stream-json event type that carries a run's final answer
	schemaTypeObject  = "object" // JSON Schema "type" of an object
)

// SchemaRuntime is the CLI family whose own structured-output mode a verify, helper or format call may use. Any
// other command keeps the schema in the prompt.
type SchemaRuntime string

const (
	SchemaRuntimeClaude      SchemaRuntime = "claude"       // claude CLI: --json-schema, the answer arrives in result.structured_output
	SchemaRuntimeCodex       SchemaRuntime = "codex"        // codex-as-claude: --output-schema, the answer is the final message
	SchemaRuntimeOpenAIAgent SchemaRuntime = "openai-agent" // openai-agent-as-claude: checks its final message against the schema file itself
)

// NativeSchema asks one isolated call to constrain its final answer with the CLI's own schema mode. It is an
// extra on top of the schema in the prompt and of the server-side validation, never a replacement for either.
type NativeSchema struct {
	Runtime SchemaRuntime // set by the caller, who knows the command's family
	Schema  string        // JSON Schema of the answer, in the form the runtime takes
}

// NativeSchemaFallback says why a requested native schema mode was not used. The call then runs with the schema
// in the prompt alone.
type NativeSchemaFallback string

const (
	NativeSchemaUnknownRuntime   NativeSchemaFallback = "unknown_runtime"   // the caller did not name a supported CLI family
	NativeSchemaUnsupportedShape NativeSchemaFallback = "unsupported_shape" // the schema uses constructs the provider API does not take
)

// OutputSchemaFileEnv names the file with the JSON Schema for the shims: codex passes it to `codex exec
// --output-schema`, openai-agent checks its final message against it.
const OutputSchemaFileEnv = "AFM_OUTPUT_SCHEMA_FILE"

// nativeSchemaFallback reports why the requested native mode cannot be used, or "" when it can. Only the schema's
// shape is checked: a provider behind the CLI that refuses the mode fails the process, and the caller reruns the
// call prompt-only.
func nativeSchemaFallback(ns *NativeSchema) NativeSchemaFallback {
	switch ns.Runtime {
	case SchemaRuntimeClaude:
		if !objectRootSchema(ns.Schema) {
			return NativeSchemaUnsupportedShape
		}
	case SchemaRuntimeCodex:
		if !strictSchema(ns.Schema) {
			return NativeSchemaUnsupportedShape
		}
	case SchemaRuntimeOpenAIAgent:
		// The shim's own validator takes every construct of the original schema.
	default:
		return NativeSchemaUnknownRuntime
	}
	return ""
}

// selectNativeSchema decides, once per isolated call, whether the requested native mode is used; the result is
// kept in cfg for run. It returns the fallback reason, "" when native mode is used or was not requested.
func (e *Executor) selectNativeSchema() NativeSchemaFallback {
	if e.cfg.NativeSchema == nil {
		return ""
	}
	if reason := nativeSchemaFallback(e.cfg.NativeSchema); reason != "" {
		return reason
	}
	e.cfg.nativeSchemaOn = true
	return ""
}

// claudeNativeSchema reports whether the call runs in Claude's native mode, where the answer is the payload of the
// result event. In the shims' modes the final message is the answer, so there is nothing separate to take.
func (c Config) claudeNativeSchema() bool {
	return c.nativeSchemaOn && c.NativeSchema.Runtime == SchemaRuntimeClaude
}

// nativeSchemaRun is what native mode adds to one process: command-line flags (claude) or an environment variable
// naming a schema file (the codex and openai-agent shims). The zero value adds nothing.
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
func objectRootSchema(raw string) bool {
	var root map[string]any
	if json.Unmarshal([]byte(raw), &root) != nil || root["type"] != schemaTypeObject {
		return false
	}
	for _, key := range []string{"oneOf", "allOf", "anyOf"} {
		if _, exists := root[key]; exists {
			return false
		}
	}
	return true
}

// strictSchema reports whether the schema fits OpenAI's strict mode, which `codex exec --output-schema` always
// uses: only keywords from the strict allowlist, and every object is closed and lists all its properties as required.
func strictSchema(raw string) bool {
	var root map[string]any
	return json.Unmarshal([]byte(raw), &root) == nil && root["type"] == schemaTypeObject && strictNode(root)
}

// strictNode checks one schema node and the schemas nested in it. Only schema positions are walked, so a property
// that happens to be named like a keyword is not mistaken for one.
func strictNode(node any) bool {
	n, ok := node.(map[string]any)
	if !ok {
		return true
	}
	for key := range n {
		if !schema.IsStrictKeyword(key) {
			return false
		}
	}
	properties, _ := n["properties"].(map[string]any)
	if allowsObject(n["type"]) && !closedWithAllRequired(n, properties) {
		return false
	}
	nested := slices.AppendSeq([]any{n["items"]}, maps.Values(properties))
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
