package budget

import (
	"encoding/json"
	"math"
	"testing"
)

func TestSpecJSONIsPresenceAware(t *testing.T) {
	bad := []string{
		`{"tokens":1,"tokens":2}`, `{"money_usd":1,"money_usd":1}`,
		`{"tokens":0,"money_usd":1}`, `{"tokens":1,"money_usd":0}`,
		`{"tokens":1,"stop_policy":""}`, `{"tokens":1,"stop_policy":null}`,
		`{"tokens":null,"money_usd":1}`, `null`, `[]`, `{"tokens":1} {}`,
	}
	for _, in := range bad {
		var s Spec
		if err := json.Unmarshal([]byte(in), &s); err == nil {
			t.Errorf("Unmarshal(%s) = %+v, want error", in, s)
		}
	}
	var ok Spec
	if err := json.Unmarshal([]byte(`{"tokens":1,"money_usd":2}`), &ok); err != nil || ok.StopPolicy != PolicyHard {
		t.Errorf("omitted policy: %+v, %v; want hard", ok, err)
	}
}

func TestParseTokensRangeAfterFractionExpansion(t *testing.T) {
	cases := map[string]bool{
		"9007199254740.991k": true, "9007199254740.992k": false,
		"9007199254.740991m": true, "9007199254.740992m": false,
	}
	for in, ok := range cases {
		if _, err := ParseTokens(in); (err == nil) != ok {
			t.Errorf("ParseTokens(%q) err = %v, want ok=%v", in, err, ok)
		}
	}
}

func TestSpendAddSaturatesAndStaysReached(t *testing.T) {
	big := Spend{MoneyNanoUSD: math.MaxInt64 - 1, Tokens: math.MaxUint64 - 1}
	sum := big.Add(big)
	if sum.MoneyNanoUSD != math.MaxInt64 || sum.Tokens != math.MaxUint64 {
		t.Fatalf("sum = %+v, want saturated", sum)
	}
	if e := Evaluate(Spec{MoneyUSD: 1, Tokens: 1}, sum); e.Money != LevelReached || e.Tokens != LevelReached {
		t.Fatalf("evaluation = %+v, want reached", e)
	}
}

func TestUpdateAndResumeRequireExplicitFields(t *testing.T) {
	badUpdate := []string{
		`{"scope":"run","expected_revision":4}`,
		`{"scope":"run","expected_revision":4,"override":null}`,
		`{"scope":"run","expected_revision":4,"override":{}}`,
		`{"scope":"run","override":{"kind":"none"}}`,
		`{"scope":"run","expected_revision":null,"override":{"kind":"none"}}`,
		`{"scope":"run","expected_revision":1,"expected_revision":1,"override":{"kind":"none"}}`,
		`{"scope":"run","expected_revision":1,"override":{"kind":"limit","spec":null}}`,
		`{"scope":"run","expected_revision":1,"override":{"kind":"limit","spec":{"tokens":0}}}`,
		`{"scope":"run","expected_revision":1,"override":{"kind":"none","kind":"none"}}`,
	}
	for _, in := range badUpdate {
		var r UpdateRequest
		if err := json.Unmarshal([]byte(in), &r); err == nil {
			t.Errorf("UpdateRequest %s accepted: %+v", in, r)
		}
	}
	var r UpdateRequest
	if err := json.Unmarshal([]byte(`{"scope":"run","expected_revision":0,"override":{"kind":"none"}}`), &r); err != nil {
		t.Errorf("explicit zero revision with none override: %v", err)
	}
	for _, in := range []string{`{"scope":"run"}`, `{"scope":"run","expected_revision":null}`, `{"expected_revision":1}`} {
		var rr ResumeRequest
		if err := json.Unmarshal([]byte(in), &rr); err == nil {
			t.Errorf("ResumeRequest %s accepted", in)
		}
	}
}

func TestViewDrainingRoundTrips(t *testing.T) {
	data, err := json.Marshal(View{Scope: "run", Kind: ViewRun, Draining: true})
	if err != nil {
		t.Fatal(err)
	}
	var v View
	if err := json.Unmarshal(data, &v); err != nil || !v.Draining {
		t.Fatalf("round trip = %+v, %v from %s", v, err, data)
	}
}

// RejectNullField sees every member, so neither a repeat nor malformed input can hide a null budget.
func TestRejectNullField(t *testing.T) {
	bad := []string{
		`{"Budget":null}`, `{"budget":null}`, `{"Budget":{"tokens":1},"Budget":null}`,
		`{"Budget":{"tokens":1},"budget":{"tokens":1}}`, `{"Other":1,"Other":2,"Budget":null}`,
		`not json`, `[]`, `{"Budget":`, `{"Budget":{"tokens":1}} {}`,
		`{"\u0042udget":null}`, `{"Budget":{"tokens":1},"\u0042udget":null}`, `{"\u0042udget":{"tokens":1},"Budget":{"tokens":1}}`,
	}
	for _, in := range bad {
		if err := RejectNullField([]byte(in), "Budget"); err == nil {
			t.Errorf("%s accepted", in)
		}
	}
	for _, ok := range []string{`null`, `{}`, `{"Budget":{"tokens":1}}`, `{"\u0042udget":{"tokens":1}}`, `{"Other":null}`, `{"Other":1,"Other":2}`} {
		if err := RejectNullField([]byte(ok), "Budget"); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
}

// The budget DTOs take only their exact keys: encoding/json would fold another spelling onto a field
// whose presence and value were already checked.
func TestBudgetDTOsRequireExactKeys(t *testing.T) {
	bad := []struct {
		body   string
		target any
	}{
		{`{"Tokens":1}`, &Spec{}},
		{`{"tokens":1,"Tokens":2}`, &Spec{}},
		{`{"tokens":1,"tokens":2}`, &Spec{}},
		{`{"tokens":1,"Stop_Policy":"info"}`, &Spec{}},
		{`{"kind":"limit","spec":{"tokens":1},"Kind":"none"}`, &Override{}},
		{`{"Kind":"none"}`, &Override{}},
		{`{"kind":"none","Spec":null}`, &Override{}},
		{`{"kind":null}`, &Override{}},
		{`{"kind":"limit","spec":{"tokens":1},"Spec":{"tokens":2}}`, &Override{}},
		{`{"kind":"limit","spec":{"tokens":1,"tokens":2}}`, &Override{}},
		{`{"scope":"run","expected_revision":4,"Expected_Revision":0,"override":{"kind":"none"}}`, &UpdateRequest{}},
		{`{"scope":"run","expected_revision":4,"expected_revision":0,"override":{"kind":"none"}}`, &UpdateRequest{}},
		{`{"scope":"stage:s","ſcope":"run","expected_revision":4,"override":{"kind":"none"}}`, &UpdateRequest{}},
		{`{"scope":"run","expected_revision":4,"override":{"kind":"limit","spec":{"tokens":1}},"Override":{"kind":"none"}}`, &UpdateRequest{}},
		{`{"scope":"run","expected_revision":4,"override":{"kind":"none"},"resume":null}`, &UpdateRequest{}},
		{`{"scope":"run","expected_revision":4,"override":{"kind":"none"},"save_to_yaml":null}`, &UpdateRequest{}},
		{`{"scope":"run","expected_revision":4,"override":{"kind":"none"},"Resume":true}`, &UpdateRequest{}},
		{`{"scope":"run","expected_revision":4,"Expected_Revision":0}`, &ResumeRequest{}},
		{`{"scope":"stage:s","ſcope":"run","expected_revision":4}`, &ResumeRequest{}},
		{`{"scope":"stage:s","Scope":"run","expected_revision":4}`, &ResumeRequest{}},
	}
	for _, tc := range bad {
		if err := json.Unmarshal([]byte(tc.body), tc.target); err == nil {
			t.Errorf("%s accepted: %+v", tc.body, tc.target)
		}
	}
}

func TestBudgetDTOsAcceptExplicitValues(t *testing.T) {
	updates := map[string]Override{
		`{"scope":"run","expected_revision":0,"override":{"kind":"none"}}`:                      {Kind: OverrideNone},
		`{"scope":"run","expected_revision":4,"override":{"kind":"unlimited"},"resume":false}`:  {Kind: OverrideUnlimited},
		`{"scope":"run","expected_revision":4,"override":{"kind":"limit","spec":{"tokens":1}}}`: {Kind: OverrideLimit, Spec: Spec{Tokens: 1, StopPolicy: PolicyHard}},
	}
	for in, want := range updates {
		var r UpdateRequest
		if err := json.Unmarshal([]byte(in), &r); err != nil || r.Override != want || r.Scope != RunScope() {
			t.Errorf("%s = %+v, %v; want override %+v", in, r, err, want)
		}
	}
	var r ResumeRequest
	if err := json.Unmarshal([]byte(`{"scope":"run","expected_revision":0}`), &r); err != nil || r.Scope != RunScope() {
		t.Errorf("resume with revision 0 = %+v, %v", r, err)
	}
}

// A JSON escape spells the same key as its decoded name: it is accepted alone and repeats the plain spelling.
func TestBudgetDTOsDecodeEscapedKeys(t *testing.T) {
	bad := []struct {
		body   string
		target any
	}{
		{`{"tokens":1,"\u0074okens":2}`, &Spec{}},
		{`{"\u0054okens":1}`, &Spec{}},
		{`{"scope":"run","expected_revision":4,"\u0065xpected_revision":0,"override":{"kind":"none"}}`, &UpdateRequest{}},
		{`{"scope":"run","\u0045xpected_revision":4,"override":{"kind":"none"}}`, &UpdateRequest{}},
		{`{"scope":"run","expected_revision":4,"\u0065xpected_revision":0}`, &ResumeRequest{}},
	}
	for _, tc := range bad {
		if err := json.Unmarshal([]byte(tc.body), tc.target); err == nil {
			t.Errorf("%s accepted: %+v", tc.body, tc.target)
		}
	}

	var s Spec
	if err := json.Unmarshal([]byte(`{"\u0074okens":1}`), &s); err != nil || s.Tokens != 1 {
		t.Errorf("escaped tokens key = %+v, %v; want 1 token", s, err)
	}
	var r UpdateRequest
	body := `{"scope":"run","\u0065xpected_revision":4,"override":{"kind":"none"}}`
	if err := json.Unmarshal([]byte(body), &r); err != nil || r.ExpectedRevision != 4 {
		t.Errorf("escaped expected_revision key = %+v, %v; want revision 4", r, err)
	}
}
