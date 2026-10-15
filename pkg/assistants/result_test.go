package assistants_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/assistants/assistantstest"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const assistantResultSchemaPath = "../../schema/assistant-result.schema.json"

func compileAssistantResultSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	abs, err := filepath.Abs(assistantResultSchemaPath)
	if err != nil {
		t.Fatalf("absolute schema path: %v", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft7)
	sch, err := compiler.Compile(abs)
	if err != nil {
		t.Fatalf("compile schema %s: %v", assistantResultSchemaPath, err)
	}
	return sch
}

func mustUnmarshalJSON(t *testing.T, raw string) any {
	t.Helper()
	v, err := jsonschema.UnmarshalJSON(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("decode JSON instance: %v\n%s", err, raw)
	}
	return v
}

// TestResultSchema_AcceptsValidFixtures проверяет, что все "result"/"delegate"
// фикстуры из T0.3 (общая тестовая опора для всех последующих этапов) валидны
// относительно schema/assistant-result.schema.json.
func TestResultSchema_AcceptsValidFixtures(t *testing.T) {
	sch := compileAssistantResultSchema(t)

	fixtures := map[string]string{
		"valid":        assistantstest.FixtureValidResult,
		"withFindings": assistantstest.FixtureResultWithFindings,
		"needsChanges": assistantstest.FixtureNeedsChangesResult,
		"withSecret":   assistantstest.FixtureResultWithSecret,
		"delegate":     assistantstest.FixtureDelegateResponse,
	}
	for name, raw := range fixtures {
		t.Run(name, func(t *testing.T) {
			if err := sch.Validate(mustUnmarshalJSON(t, raw)); err != nil {
				t.Fatalf("schema rejected fixture %s: %v", name, err)
			}
		})
	}
}

// TestResultSchema_RejectsUnknownFields проверяет additionalProperties:false —
// опечатка/самовольное поле модели не должна молча проглатываться.
func TestResultSchema_RejectsUnknownFields(t *testing.T) {
	sch := compileAssistantResultSchema(t)

	const withUnknownField = `{
		"schema_version": 1,
		"kind": "result",
		"verdict": "pass",
		"summary": "ok",
		"confidence": 0.9
	}`
	if err := sch.Validate(mustUnmarshalJSON(t, withUnknownField)); err == nil {
		t.Fatal("schema accepted an unknown field, want rejection")
	}
}

// TestResultSchema_RejectsVerdictFindingsMismatch — verdict:"pass" с находкой
// severity:"blocker" внутренне противоречив и должен быть отвергнут.
func TestResultSchema_RejectsVerdictFindingsMismatch(t *testing.T) {
	sch := compileAssistantResultSchema(t)

	const verdictOkWithBlockerFinding = `{
		"schema_version": 1,
		"kind": "result",
		"verdict": "pass",
		"summary": "looks fine",
		"findings": [
			{"id": "f1", "severity": "blocker", "detail": "actually not fine"}
		]
	}`
	if err := sch.Validate(mustUnmarshalJSON(t, verdictOkWithBlockerFinding)); err == nil {
		t.Fatal("schema accepted verdict:pass with a blocker finding, want rejection")
	}
}

// TestDecodeEnvelope_AcceptsFixtures проверяет Go-side декодер (result.go)
// тем же набором фикстур, которым проверена схема выше — оба описывают один
// контракт и не должны разойтись.
func TestDecodeEnvelope_AcceptsFixtures(t *testing.T) {
	cases := map[string]assistants.Kind{
		assistantstest.FixtureValidResult:        assistants.KindResult,
		assistantstest.FixtureResultWithFindings: assistants.KindResult,
		assistantstest.FixtureNeedsChangesResult: assistants.KindResult,
		assistantstest.FixtureResultWithSecret:   assistants.KindResult,
		assistantstest.FixtureDelegateResponse:   assistants.KindDelegate,
	}
	for raw, wantKind := range cases {
		env, err := assistants.DecodeEnvelope([]byte(raw))
		if err != nil {
			t.Fatalf("DecodeEnvelope(%s): %v", raw, err)
		}
		if env.Kind != wantKind {
			t.Fatalf("DecodeEnvelope(%s).Kind = %q, want %q", raw, env.Kind, wantKind)
		}
		if wantKind == assistants.KindResult && env.Result == nil {
			t.Fatalf("DecodeEnvelope(%s).Result is nil for kind:result", raw)
		}
		if wantKind == assistants.KindDelegate && env.Delegate == nil {
			t.Fatalf("DecodeEnvelope(%s).Delegate is nil for kind:delegate", raw)
		}
	}
}

// TestDecodeEnvelope_RejectsMalformed проверяет, что декодер отвергает ровно
// то, что отвергает схема: неизвестные поля и verdict/findings-рассогласование.
func TestDecodeEnvelope_RejectsMalformed(t *testing.T) {
	cases := map[string]string{
		"invalidJSON":                 assistantstest.FixtureInvalidJSON,
		"markdownFenced":              assistantstest.FixtureMarkdownFenced,
		"unknownField":                `{"schema_version":1,"kind":"result","verdict":"pass","summary":"ok","confidence":0.9}`,
		"verdictOkWithBlockerFinding": `{"schema_version":1,"kind":"result","verdict":"pass","summary":"ok","findings":[{"id":"f1","severity":"blocker","detail":"d"}]}`,
		"missingKind":                 `{"schema_version":1,"verdict":"pass","summary":"ok"}`,
		"unknownKind":                 `{"schema_version":1,"kind":"opinion","verdict":"pass","summary":"ok"}`,
		"unsupportedVersion":          `{"schema_version":2,"kind":"result","verdict":"pass","summary":"ok"}`,
		"delegateWithoutChild":        `{"schema_version":1,"kind":"delegate","children":[]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := assistants.DecodeEnvelope([]byte(raw)); err == nil {
				t.Fatalf("DecodeEnvelope(%s) = nil error, want rejection", name)
			}
		})
	}
}
