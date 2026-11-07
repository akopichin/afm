// Package schema embeds the same output contracts used by decoders and agent prompts.
package schema

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

type Contract string

const (
	// AssistantResult is a helper's answer where verdict is optional: a result without one is a neutral report.
	AssistantResult Contract = "assistant-result"
	// AssistantResultOnly is AssistantResult without the delegate form.
	AssistantResultOnly Contract = "assistant-result-only"
	// AssistantResultRequired and AssistantResultOnlyRequired are the same answers for a required helper, where
	// only an explicit verdict can close the gate.
	AssistantResultRequired     Contract = "assistant-result-required"
	AssistantResultOnlyRequired Contract = "assistant-result-only-required"
	VerifyResult                Contract = "verify-result"
	Question                    Contract = "question"
)

// IsAssistantResult reports whether the contract is one of a helper's turn-answer variants.
func IsAssistantResult(c Contract) bool {
	switch c {
	case AssistantResult, AssistantResultOnly, AssistantResultRequired, AssistantResultOnlyRequired:
		return true
	default:
		return false
	}
}

//go:embed assistant-result.schema.json verify-result.schema.json question.schema.json
var outputSchemas embed.FS

// JSON supplies the exact asset to prompt and native structured-output callers.
func JSON(contract Contract) string {
	switch contract {
	case AssistantResultRequired:
		return assistantResultVariant(false, true)
	case AssistantResultOnly:
		return assistantResultVariant(true, false)
	case AssistantResultOnlyRequired:
		return assistantResultVariant(true, true)
	}
	raw, err := outputSchemas.ReadFile(string(contract) + ".schema.json")
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// assistantResultVariant derives a helper answer schema from the embedded one, which has an optional verdict and
// the delegate form: resultOnly drops delegation, verdictRequired makes the result carry a verdict.
func assistantResultVariant(resultOnly, verdictRequired bool) string {
	raw, err := outputSchemas.ReadFile(string(AssistantResult) + ".schema.json")
	if err != nil {
		panic(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		panic(err)
	}
	var definitions map[string]json.RawMessage
	if err := json.Unmarshal(doc["definitions"], &definitions); err != nil {
		panic(err)
	}
	if verdictRequired {
		var result map[string]json.RawMessage
		if err := json.Unmarshal(definitions["result"], &result); err != nil {
			panic(err)
		}
		var required []string
		if err := json.Unmarshal(result["required"], &required); err != nil {
			panic(err)
		}
		// Keep the field order of the document: verdict follows kind.
		at := slices.Index(required, "kind") + 1
		result["required"], _ = json.Marshal(slices.Insert(required, at, "verdict"))
		definitions["result"], _ = json.Marshal(result)
	}
	if resultOnly {
		delete(doc, "oneOf")
		delete(doc, "description")
		for _, name := range []string{"delegate", "delegateChild", "continuation"} {
			delete(definitions, name)
		}
		doc["$ref"] = json.RawMessage(`"#/definitions/result"`)
	}
	doc["definitions"], _ = json.Marshal(definitions)
	out, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	return string(out)
}

var compiledOutputs = sync.OnceValues(func() (map[Contract]*jsonschema.Schema, error) {
	compiled := make(map[Contract]*jsonschema.Schema)
	for _, contract := range []Contract{AssistantResult, AssistantResultOnly, AssistantResultRequired, AssistantResultOnlyRequired, VerifyResult, Question} {
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
