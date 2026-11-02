package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testObjectSchema = `{"type":"object","required":["verdict"],"properties":{"verdict":{"enum":["pass","fail"]}}}`

func writeSchemaFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "schema.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// runSchemaValidate executes `afm schema validate` through the root command, as the shim calls it, and returns
// stdout, stderr and the exit code.
func runSchemaValidate(t *testing.T, stdin string, args ...string) stageRun {
	t.Helper()
	cmd := newRootCmd()
	var out, errOut bytes.Buffer
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(append([]string{"schema", "validate"}, args...))
	err := cmd.Execute()
	res := stageRun{stdout: out.String(), stderr: errOut.String()}
	var exitErr *ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		res.code = exitErr.Code
		if !exitErr.Silent {
			t.Fatalf("schema validate must be silent on failure, got %v", err)
		}
	default:
		t.Fatalf("unexpected error type: %v", err)
	}
	return res
}

func TestSchemaValidate_ValidDocumentExitsZero(t *testing.T) {
	schemaFile := writeSchemaFile(t, testObjectSchema)
	res := runSchemaValidate(t, `{"verdict":"pass"}`, "--schema", schemaFile)
	if res.code != 0 || res.stderr != "" {
		t.Fatalf("%+v", res)
	}
}

func TestSchemaValidate_InvalidDocumentReportsOutputInvalid(t *testing.T) {
	schemaFile := writeSchemaFile(t, testObjectSchema)
	cases := map[string]string{
		"violates schema": `{"verdict":"maybe"}`,
		"not JSON":        `I am done, the answer is pass`,
		"empty":           ``,
		"trailing data":   `{"verdict":"pass"} extra`,
	}
	for name, stdin := range cases {
		t.Run(name, func(t *testing.T) {
			res := runSchemaValidate(t, stdin, "--schema", schemaFile)
			if res.code != 1 || res.stdout != "" {
				t.Fatalf("%+v", res)
			}
			got := decodeJSON(t, res.stderr)
			if got["error"] != "output_invalid" || got["message"] == "" {
				t.Fatalf("stderr = %s", res.stderr)
			}
		})
	}
}

func TestSchemaValidate_SchemaProblemsAreNotOutputInvalid(t *testing.T) {
	cases := map[string][]string{
		"schema_unreadable": {"--schema", filepath.Join(t.TempDir(), "missing.json")},
		"schema_invalid":    {"--schema", writeSchemaFile(t, `{"type":"nonsense"}`)},
		"schema_not_json":   {"--schema", writeSchemaFile(t, `not json`)},
	}
	for code, args := range cases {
		t.Run(code, func(t *testing.T) {
			res := runSchemaValidate(t, `{"verdict":"pass"}`, args...)
			if res.code != 1 {
				t.Fatalf("%+v", res)
			}
			wantCode := code
			if code == "schema_not_json" {
				wantCode = "schema_invalid"
			}
			if got := decodeJSON(t, res.stderr); got["error"] != wantCode {
				t.Fatalf("stderr = %s, want error %s", res.stderr, wantCode)
			}
		})
	}
}

func TestSchemaValidate_RequiresSchemaFlag(t *testing.T) {
	res := runSchemaValidate(t, `{}`)
	if res.code != 1 || decodeJSON(t, res.stderr)["error"] != "schema_unreadable" {
		t.Fatalf("%+v", res)
	}
}

func TestSchemaValidate_AcceptsEmbeddedContractSchema(t *testing.T) {
	// The shim validates against schemas that carry $id and $ref/definitions, like the verify contract.
	schemaFile := writeSchemaFile(t, `{"$id":"https://example.invalid/x.json","$ref":"#/definitions/r","definitions":{"r":{"type":"object","required":["a"],"properties":{"a":{"type":"integer"}},"additionalProperties":false}}}`)
	if res := runSchemaValidate(t, `{"a":1}`, "--schema", schemaFile); res.code != 0 {
		t.Fatalf("%+v", res)
	}
	if res := runSchemaValidate(t, `{"a":"x"}`, "--schema", schemaFile); res.code != 1 {
		t.Fatalf("%+v", res)
	}
}
