// Package schema embeds the same output contracts used by decoders and agent prompts.
package schema

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

type Contract string

const (
	AssistantResult     Contract = "assistant-result"
	AssistantResultOnly Contract = "assistant-result-only"
	VerifyResult        Contract = "verify-result"
	Question            Contract = "question"
)

//go:embed assistant-result.schema.json verify-result.schema.json question.schema.json
var outputSchemas embed.FS

// JSON supplies the exact asset to prompt and native structured-output callers.
func JSON(contract Contract) string {
	if contract == AssistantResultOnly {
		var doc map[string]json.RawMessage
		if err := json.Unmarshal([]byte(JSON(AssistantResult)), &doc); err != nil {
			panic(err)
		}
		delete(doc, "oneOf")
		delete(doc, "description")
		var definitions map[string]json.RawMessage
		if err := json.Unmarshal(doc["definitions"], &definitions); err != nil {
			panic(err)
		}
		for _, name := range []string{"delegate", "delegateChild", "continuation"} {
			delete(definitions, name)
		}
		doc["definitions"], _ = json.Marshal(definitions)
		doc["$ref"] = json.RawMessage(`"#/definitions/result"`)
		raw, err := json.Marshal(doc)
		if err != nil {
			panic(err)
		}
		return string(raw)
	}
	raw, err := outputSchemas.ReadFile(string(contract) + ".schema.json")
	if err != nil {
		panic(err)
	}
	return string(raw)
}

var compiledOutputs = sync.OnceValues(func() (map[Contract]*jsonschema.Schema, error) {
	compiled := make(map[Contract]*jsonschema.Schema)
	for _, contract := range []Contract{AssistantResult, AssistantResultOnly, VerifyResult, Question} {
		compiler := jsonschema.NewCompiler()
		doc, err := jsonschema.UnmarshalJSON(strings.NewReader(JSON(contract)))
		if err != nil {
			return nil, err
		}
		url := "https://afm.invalid/" + string(contract) + ".schema.json"
		if err := compiler.AddResource(url, doc); err != nil {
			return nil, err
		}
		sch, err := compiler.Compile(url)
		if err != nil {
			return nil, err
		}
		compiled[contract] = sch
	}
	return compiled, nil
})

// Validate checks shape after the caller has rejected duplicate keys and bounded input size.
func Validate(contract Contract, raw []byte) error {
	compiled, err := compiledOutputs()
	if err != nil {
		return fmt.Errorf("compile output schemas: %w", err)
	}
	sch, ok := compiled[contract]
	if !ok {
		return fmt.Errorf("unknown output contract %q", contract)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	return sch.Validate(doc)
}
