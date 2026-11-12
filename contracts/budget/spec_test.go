package budget

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParseTokens(t *testing.T) {
	ok := map[string]uint64{
		"500k": 500_000, "1m": 1_000_000, "1.5m": 1_500_000, "0.5k": 500, "1.234k": 1_234,
		"0.000001m": 1, "2K": 2_000, "9007199254740k": 9_007_199_254_740_000,
	}
	for in, want := range ok {
		if got, err := ParseTokens(in); err != nil || got != want {
			t.Errorf("ParseTokens(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	bad := []string{
		"", "k", "1000", "-1k", "0k", "0.0m", "1.2345k", "1.0000001m", ".5k", "1.k", "1e3k", "1 k", " 1k",
		"1.5", "abc", "9007199254741k", "99999999999999999999k", "10000000000m", "1,5m", "+1k",
	}
	for _, in := range bad {
		if got, err := ParseTokens(in); err == nil {
			t.Errorf("ParseTokens(%q) = %d, want error", in, got)
		}
	}
}

func TestParseMoney(t *testing.T) {
	ok := map[string]float64{"10$": 10, "0.50$": 0.5, "1000000000$": 1e9, "0.000001$": 0.000001}
	for in, want := range ok {
		if got, err := ParseMoney(in); err != nil || got != want {
			t.Errorf("ParseMoney(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	bad := []string{"", "$", "10", "0$", "-1$", "10€", "10 $", "$10", "NaN$", "Inf$", "1e3$", "1.$", ".5$", "1000000001$", "10 USD", "1,5$"}
	for _, in := range bad {
		if got, err := ParseMoney(in); err == nil {
			t.Errorf("ParseMoney(%q) = %v, want error", in, got)
		}
	}
}

func TestSpecUnmarshalYAML(t *testing.T) {
	ok := map[string]Spec{
		"tokens: 1m\nmoney: 10$\nstop_policy: medium": {Tokens: 1_000_000, MoneyUSD: 10, StopPolicy: PolicyMedium},
		"tokens: 500000":                    {Tokens: 500_000, StopPolicy: PolicyHard},
		"money: 0.50$":                      {MoneyUSD: 0.5, StopPolicy: PolicyHard},
		"money: 2":                          {MoneyUSD: 2, StopPolicy: PolicyHard},
		"money: 2.5":                        {MoneyUSD: 2.5, StopPolicy: PolicyHard},
		"tokens: 1000.0":                    {Tokens: 1000, StopPolicy: PolicyHard},
		"tokens: '1.5m'\nstop_policy: info": {Tokens: 1_500_000, StopPolicy: PolicyInfo},
		"{tokens: 7, stop_policy: hard}":    {Tokens: 7, StopPolicy: PolicyHard},
	}
	for in, want := range ok {
		var got Spec
		if err := yaml.Unmarshal([]byte(in), &got); err != nil || got != want {
			t.Errorf("yaml %q = %+v, %v; want %+v", in, got, err, want)
		}
	}
	bad := []string{
		"{}", "stop_policy: hard", "tokens: 0", "tokens: -5", "money: 0", "money: -1", "money: 0$",
		"tokens: 1.5", "tokens: '1000'", "tokens: abc", "tokens: [1]", "tokens: true",
		"tokens: 9007199254740992", "tokens: 99999999999999999999", "tokens: 1e300", "tokens: .inf",
		"money: .nan", "money: .inf", "money: 10€", "money: 1e10", "money: true", "money: ''",
		"tokens: 1\nunknown: 2", "tokens: 1\ntokens: 2", "tokens: ~", "tokens: 1\nmoney: null",
		"tokens: 1\nstop_policy: soft", "tokens: 1\nstop_policy: ''", "tokens: 1\nstop_policy: 1",
		"tokens: 1\nstop_policy: null", "[1, 2]", "10$", "tokens: 1\ntokens_x: 1", "tokens: 1\nMoney: 1$",
	}
	for _, in := range bad {
		var got Spec
		if err := yaml.Unmarshal([]byte(in), &got); err == nil {
			t.Errorf("yaml %q = %+v, want error", in, got)
		}
	}
}

func TestSpecYAMLAlias(t *testing.T) {
	var doc struct {
		A Spec `yaml:"a"`
		B Spec `yaml:"b"`
	}
	in := "a: &x\n  tokens: 1k\nb: *x\n"
	if err := yaml.Unmarshal([]byte(in), &doc); err != nil || doc.A != doc.B || doc.B.Tokens != 1000 {
		t.Fatalf("alias: %+v, %v", doc, err)
	}
}

// Keys count as yaml.v3 decodes them: an alias key is its target, never its anchor name.
func TestSpecYAMLKeyIdentity(t *testing.T) {
	type doc struct {
		Budget Spec `yaml:"budget"`
	}
	ok := map[string]Spec{
		"x: &k tokens\nbudget: {*k : 1}":          {Tokens: 1, StopPolicy: PolicyHard},
		"x: &tokens money\nbudget: {*tokens : 1}": {MoneyUSD: 1, StopPolicy: PolicyHard},
		"budget: {!!binary dG9rZW5z: 1}":          {Tokens: 1, StopPolicy: PolicyHard},
		"budget: {!!merge tokens: 1}":             {Tokens: 1, StopPolicy: PolicyHard},
	}
	for in, want := range ok {
		var got doc
		if err := yaml.Unmarshal([]byte(in), &got); err != nil || got.Budget != want {
			t.Errorf("yaml %q = %+v, %v; want %+v", in, got.Budget, err, want)
		}
	}
	bad := []string{
		"x: &k tokens\nbudget: {tokens: 1, *k : 2}",
		"budget: {tokens: 1, !!binary dG9rZW5z: 2}",
		"x: &tokens unknown\nbudget: {*tokens : 1}",
		"x: &k tokens\nbudget: {*k : null, money: 1}",
		"x: &n null\nbudget: {tokens: *n}",
		"x: &k [a]\nbudget: {*k : 1}",
		"budget: {<<: {tokens: 1}}",
	}
	for _, in := range bad {
		var got doc
		if err := yaml.Unmarshal([]byte(in), &got); err == nil {
			t.Errorf("yaml %q = %+v, want error", in, got.Budget)
		}
	}
}

func TestSpecYAMLNullPointerStaysNil(t *testing.T) {
	var doc struct {
		B *Spec `yaml:"b"`
	}
	if err := yaml.Unmarshal([]byte("b: null\n"), &doc); err != nil || doc.B != nil {
		t.Fatalf("null pointer: %+v, %v (containers reject null themselves)", doc, err)
	}
}

func TestSpecDefaultPolicyComparesEqual(t *testing.T) {
	var explicit, omitted Spec
	if err := yaml.Unmarshal([]byte("tokens: 1k\nstop_policy: hard"), &explicit); err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal([]byte("tokens: 1k"), &omitted); err != nil {
		t.Fatal(err)
	}
	if explicit != omitted || omitted.StopPolicy != PolicyHard {
		t.Fatalf("explicit %+v != omitted %+v", explicit, omitted)
	}
}

func TestSpecYAMLRoundTrip(t *testing.T) {
	in := Spec{Tokens: 1_500_000, MoneyUSD: 2.5, StopPolicy: PolicyInfo}
	data, err := yaml.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Spec
	if err := yaml.Unmarshal(data, &out); err != nil || out != in {
		t.Fatalf("round trip %q = %+v, %v", data, out, err)
	}
	pointer, err := yaml.Marshal(&in)
	if err != nil || string(pointer) != string(data) {
		t.Fatalf("pointer marshal %q != %q (%v)", pointer, data, err)
	}
	if _, err := yaml.Marshal(Spec{}); err == nil {
		t.Fatal("marshalling an empty spec must fail")
	}
}

func TestSpecJSON(t *testing.T) {
	data, err := json.Marshal(Spec{Tokens: 5, StopPolicy: ""})
	if err != nil || string(data) != `{"tokens":5,"stop_policy":"hard"}` {
		t.Fatalf("marshal = %s, %v", data, err)
	}
	data, err = json.Marshal(Spec{Tokens: 5, MoneyUSD: 1.5, StopPolicy: PolicyMedium})
	if err != nil || string(data) != `{"tokens":5,"money_usd":1.5,"stop_policy":"medium"}` {
		t.Fatalf("marshal = %s, %v", data, err)
	}
	var got Spec
	if err := json.Unmarshal([]byte(`{"money_usd":2}`), &got); err != nil || got != (Spec{MoneyUSD: 2, StopPolicy: PolicyHard}) {
		t.Fatalf("unmarshal = %+v, %v", got, err)
	}
	bad := []string{
		`{}`, `null`, `[]`, `5`, `{"stop_policy":"hard"}`, `{"tokens":0}`, `{"tokens":-1}`, `{"tokens":1.5}`,
		`{"tokens":"1k"}`, `{"tokens":9007199254740992}`, `{"money_usd":0}`, `{"money_usd":-1}`, `{"money_usd":1e10}`,
		`{"tokens":1,"stop_policy":"soft"}`, `{"tokens":1,"extra":1}`, `{"tokens":1,"money_usd":null}`,
		`{"tokens":1} {}`, `{"tokens":1,"money":2}`,
	}
	for _, in := range bad {
		var s Spec
		if err := json.Unmarshal([]byte(in), &s); err == nil {
			t.Errorf("json %s = %+v, want error", in, s)
		}
	}
}

func TestSpecValidate(t *testing.T) {
	bad := []Spec{{}, {StopPolicy: PolicyHard}, {Tokens: 1, StopPolicy: "x"}, {Tokens: MaxTokens + 1}, {MoneyUSD: -1}, {MoneyUSD: 2e9}}
	for _, s := range bad {
		if s.Validate() == nil {
			t.Errorf("%+v must be invalid", s)
		}
	}
	if err := (Spec{Tokens: MaxTokens, MoneyUSD: MaxMoneyUSD}).Validate(); err != nil {
		t.Errorf("limits must be inclusive: %v", err)
	}
}

type exampleAssistant struct {
	Budget Spec `yaml:"budget"`
}

type exampleStage struct {
	Budget     Spec               `yaml:"budget"`
	Assistants []exampleAssistant `yaml:"assistants"`
}

type exampleFlow struct {
	Budget Spec           `yaml:"budget"`
	Stages []exampleStage `yaml:"stages"`
}

func ExampleSpec_unmarshalYAML() {
	const doc = `
budget:
  tokens: 1m
  money: 10$
  stop_policy: hard
stages:
  - id: implement
    budget:
      tokens: 500k
      money: 5$
    assistants:
      - profile: reviewer
        budget:
          tokens: 100k
          money: 1$
          stop_policy: info
`
	var parsed exampleFlow
	if err := yaml.Unmarshal([]byte(doc), &parsed); err != nil {
		panic(err)
	}
	stage := parsed.Stages[0]
	fmt.Println(parsed.Budget.Tokens, stage.Budget.MoneyUSD, stage.Assistants[0].Budget.StopPolicy)
	// Output: 1000000 5 info
}

func TestNoForbiddenImports(t *testing.T) {
	out, err := goListDeps()
	if err != nil {
		t.Fatal(err)
	}
	for _, dep := range strings.Fields(out) {
		first, _, _ := strings.Cut(dep, "/")
		if !strings.Contains(first, ".") || dep == "gopkg.in/yaml.v3" || dep == "github.com/akopichin/afm/contracts/budget" {
			continue // stdlib or the one allowed dependency
		}
		t.Errorf("budget must import only stdlib and yaml.v3, found %q", dep)
	}
}
