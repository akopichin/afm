package config_test

import (
	"strings"
	"testing"

	"github.com/akopichin/afm/contracts/internal/budgetparity"
)

const configSchema = "../schema/config.schema.json"

// A collaboration profile's budget gets the same verdict from the parser and the config schema.
func TestProfileBudgetSchemaParity(t *testing.T) {
	const tmpl = "collaboration:\n  profiles:\n    qa:\n      command: /abs/x.sh\n      budget: %s\n"
	for _, tc := range budgetparity.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			doc := strings.Replace(tmpl, "%s", tc.Budget, 1)
			_, err := loadModeLayers(t, "", doc)
			if got := err == nil; got != tc.ParserOK {
				t.Errorf("parser accepts = %v (err %v), want %v", got, err, tc.ParserOK)
			}
			if got := budgetparity.SchemaAccepts(t, configSchema, doc); got != tc.SchemaOK() {
				t.Errorf("schema accepts = %v, want %v (gap %q)", got, tc.SchemaOK(), tc.SchemaGap)
			}
		})
	}
}

// A profile budget that arrives through an alias key or a << merge gets the same verdict too. The anchor
// sits on the profile title, a field the schema allows.
func TestProfileBudgetSchemaParity_EffectiveKeys(t *testing.T) {
	const profile = "collaboration:\n  profiles:\n    qa:\n      title: &k budget\n      command: /abs/x.sh\n"
	cases := []struct {
		name, fields string
		parserOK     bool
		schemaGap    string
	}{
		{name: "key alias", fields: "*k : {tokens: 1k}", parserOK: true},
		{name: "key alias null", fields: "*k : null"},
		{name: "merged null", fields: "<<: {budget: null}"},
		{name: "direct over merged null", fields: "<<: {budget: null}\n      budget: {tokens: 1k}", parserOK: true},
		{name: "first merge source valid", fields: "<<: [{budget: {tokens: 1k}}, {budget: null}]", parserOK: true},
		{name: "first merge source null", fields: "<<: [{budget: null}, {budget: {tokens: 1k}}]"},
		{name: "merge tag on the key", fields: "!!merge budget: null"},
		{
			name: "key repeated through an alias", fields: "budget: {tokens: 1k}\n      *k : {tokens: 2k}",
			schemaGap: "decoding YAML for the schema keeps the last of the repeated keys",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := profile + "      " + tc.fields + "\n"
			_, err := loadModeLayers(t, "", doc)
			if got := err == nil; got != tc.parserOK {
				t.Errorf("parser accepts = %v (err %v), want %v", got, err, tc.parserOK)
			}
			if got, want := budgetparity.SchemaAccepts(t, configSchema, doc), tc.parserOK || tc.schemaGap != ""; got != want {
				t.Errorf("schema accepts = %v, want %v (gap %q)", got, want, tc.schemaGap)
			}
		})
	}
}
