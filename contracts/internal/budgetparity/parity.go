// Package budgetparity holds the shared test corpora of budget declarations: YAML values the parser and the
// JSON schemas must agree on, and the JSON container matrix. Test support only, not public contracts API.
package budgetparity

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

// Case is one budget value (a YAML flow node) and what the parser and the schema must say about it.
type Case struct {
	Name   string
	Budget string
	// ParserOK is the parser's verdict.
	ParserOK bool
	// SchemaGap, when set, documents a value the schema accepts but the parser rejects because JSON
	// Schema cannot express the rule. Every other case must get the same verdict from both.
	SchemaGap string
}

// SchemaOK is the verdict the schema must give.
func (c Case) SchemaOK() bool { return c.ParserOK || c.SchemaGap != "" }

// Cases is the table of budget values shared by the flow and config parity tests.
var Cases = []Case{
	{Name: "integer tokens", Budget: "{tokens: 500000}", ParserOK: true},
	{Name: "integer-valued float tokens", Budget: "{tokens: 5.0}", ParserOK: true},
	{Name: "k tokens", Budget: "{tokens: 500k}", ParserOK: true},
	{Name: "upper-case K tokens", Budget: "{tokens: 500K}", ParserOK: true},
	{Name: "k tokens with 3 decimals", Budget: "{tokens: 1.500k}", ParserOK: true},
	{Name: "m tokens", Budget: "{tokens: 1.5m}", ParserOK: true},
	{Name: "m tokens with 6 decimals", Budget: "{tokens: 1.123456m}", ParserOK: true},
	{Name: "numeric money", Budget: "{money: 3.5}", ParserOK: true},
	{Name: "integer money", Budget: "{money: 10}", ParserOK: true},
	{Name: "dollar money", Budget: "{money: 10$}", ParserOK: true},
	{Name: "dollar money with cents", Budget: "{money: 0.50$}", ParserOK: true},
	{Name: "tokens with leading zeros", Budget: "{tokens: 007k}", ParserOK: true},
	{Name: "smallest k tokens", Budget: "{tokens: 0.001k}", ParserOK: true},
	{Name: "smallest m tokens", Budget: "{tokens: 0.000001m}", ParserOK: true},
	{Name: "tiny dollar money", Budget: "{money: 0.01$}", ParserOK: true},
	{Name: "both limits and policy", Budget: "{tokens: 1m, money: 5$, stop_policy: medium}", ParserOK: true},
	{Name: "policy hard", Budget: "{tokens: 1k, stop_policy: hard}", ParserOK: true},
	{Name: "policy info", Budget: "{money: 1, stop_policy: info}", ParserOK: true},

	{Name: "null budget", Budget: "null"},
	{Name: "tilde budget", Budget: "~"},
	{Name: "empty object", Budget: "{}"},
	{Name: "policy only", Budget: "{stop_policy: info}"},
	{Name: "unknown key", Budget: "{tokens: 1k, ttl: 5}"},
	{Name: "duplicate key", Budget: "{tokens: 1k, tokens: 2k}"},
	{Name: "null tokens", Budget: "{tokens: null}"},
	{Name: "null money", Budget: "{money: ~}"},
	{Name: "null policy", Budget: "{tokens: 1k, stop_policy: ~}"},
	{Name: "unknown policy", Budget: "{tokens: 1k, stop_policy: soft}"},
	{Name: "scalar budget", Budget: "5k"},
	{Name: "list budget", Budget: "[5k]"},
	{Name: "zero tokens", Budget: "{tokens: 0}"},
	{Name: "negative tokens", Budget: "{tokens: -1}"},
	{Name: "fractional tokens", Budget: "{tokens: 1.5}"},
	{Name: "tokens above the maximum", Budget: "{tokens: 9007199254740992}"},
	{Name: "tokens string without suffix", Budget: "{tokens: '500'}"},
	{Name: "k tokens with 4 decimals", Budget: "{tokens: 1.5555k}"},
	{Name: "m tokens with 7 decimals", Budget: "{tokens: 1.1234567m}"},
	{Name: "k tokens with a bare point", Budget: "{tokens: 1.k}"},
	{Name: "zero k tokens", Budget: "{tokens: 0k}"},
	{Name: "zero k tokens with decimals", Budget: "{tokens: 0.000k}"},
	{Name: "zero m tokens with decimals", Budget: "{tokens: 0.000000m}"},
	{Name: "zero with leading zeros", Budget: "{tokens: 00k}"},
	{Name: "zero dollar money", Budget: "{money: 0$}"},
	{Name: "zero dollar money with decimals", Budget: "{money: 0.00$}"},
	{Name: "zero money", Budget: "{money: 0}"},
	{Name: "negative money", Budget: "{money: -1}"},
	{Name: "money above the maximum", Budget: "{money: 1000000001}"},
	{Name: "money string without dollar", Budget: "{money: '10'}"},
	{Name: "money with only a dollar", Budget: "{money: '$'}"},
	{Name: "money with a bare point", Budget: "{money: '10.$'}"},

	{Name: "k tokens beyond the maximum", Budget: "{tokens: 9007199254741k}", SchemaGap: "numeric range of a string form"},
	{Name: "m tokens beyond the maximum", Budget: "{tokens: 9007199255m}", SchemaGap: "numeric range of a string form"},
	{Name: "dollar money beyond the maximum", Budget: "{money: 2000000000$}", SchemaGap: "numeric range of a string form"},
}

// SchemaAccepts reports whether the YAML document validates against the schema file. A document that is
// not even decodable (a duplicate key) counts as rejected, as it does for the parser.
func SchemaAccepts(t *testing.T, schemaPath, doc string) bool {
	t.Helper()
	var decoded any
	if err := yaml.Unmarshal([]byte(doc), &decoded); err != nil {
		return false
	}
	body, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("marshal %q: %v", doc, err)
	}
	instance, err := jsonschema.UnmarshalJSON(strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("instance %q: %v", doc, err)
	}
	return compile(t, schemaPath).Validate(instance) == nil
}

func compile(t *testing.T, schemaPath string) *jsonschema.Schema {
	t.Helper()
	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("%s: %v", schemaPath, err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft7)
	if err := c.AddResource(schemaPath, doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile(schemaPath)
	if err != nil {
		t.Fatalf("compile %s: %v", schemaPath, err)
	}
	return s
}
