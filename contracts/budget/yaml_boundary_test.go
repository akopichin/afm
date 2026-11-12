package budget

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestEffectiveYAMLValue(t *testing.T) {
	cases := []struct {
		name, doc, wantTag string
		wantSet            bool
	}{
		{name: "absent", doc: "a: 1"},
		{name: "null stays visible", doc: "budget: null", wantTag: "!!null", wantSet: true},
		{name: "direct key wins over merge", doc: "<<: {budget: null}\nbudget: {tokens: 1}", wantTag: "!!map", wantSet: true},
		{name: "first merge source wins", doc: "<<: [{budget: ~}, {budget: {tokens: 1}}]", wantTag: "!!null", wantSet: true},
		{name: "alias key", doc: "x: &k budget\n*k : null", wantTag: "!!null", wantSet: true},
		{name: "value alias", doc: "x: &n null\nbudget: *n", wantTag: "!!null", wantSet: true},
		{name: "binary key", doc: "!!binary YnVkZ2V0: null", wantTag: "!!null", wantSet: true},
		{name: "quoted << is an ordinary key", doc: "'<<': {budget: null}"},
		{name: "alias to << is an ordinary key", doc: "x: &m <<\n*m : {budget: null}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value, set, err := EffectiveYAMLValue(parseYAML(t, tc.doc), "budget")
			if err != nil || set != tc.wantSet || (set && value.ShortTag() != tc.wantTag) {
				t.Errorf("value %s, set %v, err %v; want %s, %v", value.ShortTag(), set, err, tc.wantTag, tc.wantSet)
			}
		})
	}
	if _, _, err := EffectiveYAMLValue(parseYAML(t, "x: &a {<<: *a}\n<<: *a"), "budget"); err == nil {
		t.Error("a merge cycle must be an error")
	}
}

func parseYAML(t *testing.T, doc string) *yaml.Node {
	t.Helper()
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(doc), &root); err != nil {
		t.Fatal(err)
	}
	return root.Content[0]
}
