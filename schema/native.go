package schema

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"slices"
	"strings"

	"github.com/akopichin/afm/pkg/jsonstrict"
)

// Dialect is the structured-output mode of a CLI that a native variant of a contract is derived for.
type Dialect string

const (
	// DialectClaude is Claude's StructuredOutput tool: an object root without combinators; nested ones are fine.
	DialectClaude Dialect = "claude"
	// DialectStrict is OpenAI's strict structured output (codex --output-schema): a keyword allowlist, closed
	// objects and every property required.
	DialectStrict Dialect = "strict"
)

// strictKeywords are the only keywords a strict variant keeps; everything else is dropped from every node.
var strictKeywords = []string{"type", "properties", "required", "additionalProperties", "items", "anyOf", "enum", "title", "description"}

// IsStrictKeyword reports whether OpenAI's strict mode is known to take the schema keyword.
func IsStrictKeyword(key string) bool { return slices.Contains(strictKeywords, key) }

// NativeJSON derives the variant a CLI's native schema mode takes; it only guides the model, and rules it cannot
// express stay in the original contract, the only acceptance gate. Delegation has no native form, hence no variant.
func NativeJSON(contract Contract, dialect Dialect) (string, bool) {
	root, ok := nativeRoot(contract)
	if !ok {
		return "", false
	}
	if dialect == DialectStrict {
		root = strictNode(root)
	}
	raw, err := json.Marshal(root)
	if err != nil {
		panic(err)
	}
	return string(raw), true
}

// nativeRoot is the contract's own answer schema with every $ref inlined, the root keywords that no native mode
// takes dropped and every const and enum typed.
func nativeRoot(contract Contract) (map[string]any, bool) {
	if contract == AssistantResult {
		return nil, false
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(JSON(contract)), &doc); err != nil {
		panic(err)
	}
	definitions, _ := doc["definitions"].(map[string]any)
	root := inlineRefs(doc, definitions).(map[string]any)
	for _, key := range []string{"$schema", "$id", "$ref", "definitions", "oneOf", "anyOf", "allOf", "not", "if", "then", "else"} {
		delete(root, key)
	}
	typeConstAndEnum(root)
	return root, true
}

// typeConstAndEnum gives every subschema that pins its values with const or enum but has no "type" the type those
// values share: a model shown {"const":1} alone may write "1", which the native mode rejects on every try.
func typeConstAndEnum(node any) {
	switch n := node.(type) {
	case map[string]any:
		if _, typed := n["type"]; !typed {
			if valueType := sharedValueType(n); valueType != "" {
				n["type"] = valueType
			}
		}
		for key, child := range n {
			switch key {
			case "const", "enum", "default", "examples":
				// Values, not subschemas.
			case "properties":
				properties, _ := child.(map[string]any)
				for _, property := range properties {
					typeConstAndEnum(property)
				}
			default:
				typeConstAndEnum(child)
			}
		}
	case []any:
		for _, child := range n {
			typeConstAndEnum(child)
		}
	default:
		// A scalar holds no subschema.
	}
}

// sharedValueType is the type of a node's const and enum values when all of them have the same scalar type, and
// empty otherwise: a null, a list or an object, or a mix of types, leaves the node as it is.
func sharedValueType(node map[string]any) string {
	values, _ := node["enum"].([]any)
	if value, found := node["const"]; found {
		values = append(slices.Clone(values), value)
	}
	shared := ""
	for i, value := range values {
		valueType := scalarType(value)
		if valueType == "" || (i > 0 && valueType != shared) {
			return ""
		}
		shared = valueType
	}
	return shared
}

// scalarType names the JSON type of a decoded string, boolean or number, telling integers from other numbers.
func scalarType(value any) string {
	switch v := value.(type) {
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64:
		if v == math.Trunc(v) {
			return "integer"
		}
		return "number"
	default:
		return ""
	}
}

// inlineRefs returns node with every "#/definitions/<name>" reference replaced by a copy of that definition.
func inlineRefs(node any, definitions map[string]any) any {
	switch n := node.(type) {
	case map[string]any:
		if ref, isRef := n["$ref"].(string); isRef {
			name := strings.TrimPrefix(ref, "#/definitions/")
			return inlineRefs(definitions[name], definitions)
		}
		out := make(map[string]any, len(n))
		for key, child := range n {
			if key != "definitions" {
				out[key] = inlineRefs(child, definitions)
			}
		}
		return out
	case []any:
		out := make([]any, len(n))
		for i, child := range n {
			out[i] = inlineRefs(child, definitions)
		}
		return out
	}
	return node
}

// strictNode rewrites one schema node and its subschemas for strict mode.
func strictNode(node map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range strictKeywords {
		if value, found := node[key]; found {
			out[key] = value
		}
	}
	// Strict mode documents a single allowed value as an enum, not as const.
	if value, found := node["const"]; found {
		out["enum"] = []any{value}
	}
	if items, ok := out["items"].(map[string]any); ok {
		out["items"] = strictNode(items)
	}
	if anyOf, ok := out["anyOf"].([]any); ok {
		branches := make([]any, len(anyOf))
		for i, branch := range anyOf {
			branches[i] = strictNode(branch.(map[string]any))
		}
		out["anyOf"] = branches
	}
	properties, hasProperties := out["properties"].(map[string]any)
	if !hasProperties && !allowsObject(out["type"]) {
		return out
	}
	required, _ := node["required"].([]any)
	names := make([]string, 0, len(properties))
	closed := make(map[string]any, len(properties))
	for name, child := range properties {
		names = append(names, name)
		property := strictNode(child.(map[string]any))
		if !slices.Contains(required, any(name)) {
			property = nullable(property)
		}
		closed[name] = property
	}
	slices.Sort(names)
	out["properties"] = closed
	out["required"] = names
	out["additionalProperties"] = false
	return out
}

// nullable lets an optional property be null: strict mode makes the model send every property, so null stands for
// "absent", and NormalizeNative drops it again before the original contract is checked.
func nullable(node map[string]any) map[string]any {
	if acceptsNull(node) {
		return node
	}
	if anyOf, ok := node["anyOf"].([]any); ok {
		node["anyOf"] = append(slices.Clone(anyOf), map[string]any{"type": "null"})
		return node
	}
	switch t := node["type"].(type) {
	case string:
		node["type"] = []any{t, "null"}
	case []any:
		node["type"] = append(slices.Clone(t), "null")
	default:
		// No type constraint: the enum below, if any, decides.
	}
	if enum, ok := node["enum"].([]any); ok {
		node["enum"] = append(slices.Clone(enum), nil)
	}
	return node
}

// acceptsNull reports whether a schema node takes null. A node that constrains neither type, values nor
// alternatives takes anything.
func acceptsNull(node map[string]any) bool {
	if anyOf, ok := node["anyOf"].([]any); ok {
		return slices.ContainsFunc(anyOf, func(branch any) bool {
			b, isNode := branch.(map[string]any)
			return isNode && acceptsNull(b)
		})
	}
	if enum, ok := node["enum"].([]any); ok && !slices.Contains(enum, nil) {
		return false
	}
	switch t := node["type"].(type) {
	case string:
		return t == "null"
	case []any:
		return slices.Contains(t, any("null"))
	default:
		return true
	}
}

// allowsObject reports whether a schema "type" is, or lists, "object".
func allowsObject(typeValue any) bool {
	if list, isList := typeValue.([]any); isList {
		return slices.Contains(list, any("object"))
	}
	return typeValue == "object"
}

// NormalizeNative turns an answer written against the strict variant back into the contract's form: an optional
// property sent as null although the contract does not take null is dropped. Anything else, including an answer
// that is not a single JSON value without duplicate keys, comes back byte for byte for the contract to judge.
func NormalizeNative(contract Contract, raw []byte) []byte {
	root, ok := nativeRoot(contract)
	if !ok || jsonstrict.DecodeNoDuplicates(raw) != nil {
		return raw
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if dec.Decode(&value) != nil {
		return raw
	}
	// Re-encoding would silently drop anything after the first value.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) || !dropRejectedNulls(value, root) {
		return raw
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if enc.Encode(value) != nil {
		return raw
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n"))
}

// dropRejectedNulls removes, at any depth, null-valued optional properties the schema node does not take null
// for, and reports whether it removed any.
func dropRejectedNulls(value any, node map[string]any) bool {
	changed := false
	switch v := value.(type) {
	case map[string]any:
		properties, _ := node["properties"].(map[string]any)
		required, _ := node["required"].([]any)
		for name, item := range v {
			property, known := properties[name].(map[string]any)
			if !known {
				continue
			}
			if item == nil && !slices.Contains(required, any(name)) && !acceptsNull(property) {
				delete(v, name)
				changed = true
				continue
			}
			changed = dropRejectedNulls(item, property) || changed
		}
	case []any:
		if items, ok := node["items"].(map[string]any); ok {
			for _, item := range v {
				changed = dropRejectedNulls(item, items) || changed
			}
		}
	default:
		// A scalar has no properties.
	}
	return changed
}
