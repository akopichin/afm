package budget

import (
	"encoding/json"
	"testing"
)

func TestResolveBase(t *testing.T) {
	declared := &Spec{Tokens: 10}
	profile := &Spec{MoneyUSD: 2, StopPolicy: PolicyInfo}
	cases := []struct {
		name              string
		explicit, profile *Spec
		want              *Spec
		source            Source
	}{
		{"declaration replaces profile whole", declared, profile, &Spec{Tokens: 10, StopPolicy: PolicyHard}, SourceDeclaration},
		{"profile default", nil, profile, profile, SourceProfile},
		{"nothing", nil, nil, nil, SourceNone},
	}
	for _, c := range cases {
		got, source := ResolveBase(c.explicit, c.profile)
		if source != c.source || (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
			t.Errorf("%s: got %+v %q", c.name, got, source)
		}
	}
	got, _ := ResolveBase(declared, nil)
	got.Tokens = 99
	if declared.Tokens != 10 {
		t.Error("ResolveBase must return a copy")
	}
}

func TestEffective(t *testing.T) {
	base := &Spec{Tokens: 10, StopPolicy: PolicyHard}
	limit := Override{Kind: OverrideLimit, Spec: Spec{MoneyUSD: 3}}
	if got := Effective(base, Override{Kind: OverrideNone}); got == nil || *got != *base {
		t.Errorf("none keeps the base, got %+v", got)
	}
	if got := Effective(base, Override{}); got == nil || *got != *base {
		t.Errorf("zero override keeps the base, got %+v", got)
	}
	if got := Effective(base, limit); got == nil || *got != (Spec{MoneyUSD: 3, StopPolicy: PolicyHard}) {
		t.Errorf("limit replaces the base whole, got %+v", got)
	}
	if got := Effective(base, Override{Kind: OverrideUnlimited}); got != nil {
		t.Errorf("unlimited removes the limit, got %+v", got)
	}
	if got := Effective(nil, Override{Kind: OverrideNone}); got != nil {
		t.Errorf("no base and no override is unlimited, got %+v", got)
	}
	if got := Effective(nil, limit); got == nil {
		t.Error("an override can add a limit where there is no base")
	}
}

func TestOverrideJSON(t *testing.T) {
	cases := map[string]Override{
		`{"kind":"none"}`:      {Kind: OverrideNone},
		`{"kind":"unlimited"}`: {Kind: OverrideUnlimited},
		`{"kind":"limit","spec":{"money_usd":3,"stop_policy":"info"}}`: {Kind: OverrideLimit, Spec: Spec{MoneyUSD: 3, StopPolicy: PolicyInfo}},
	}
	for want, o := range cases {
		data, err := json.Marshal(o)
		if err != nil || string(data) != want {
			t.Errorf("marshal %+v = %s, %v; want %s", o, data, err, want)
		}
		var back Override
		if err := json.Unmarshal([]byte(want), &back); err != nil || back != o {
			t.Errorf("unmarshal %s = %+v, %v", want, back, err)
		}
	}
	bad := []string{
		`{"kind":"limit"}`, `{"kind":"none","spec":{"tokens":1}}`, `{"kind":"unlimited","spec":{"tokens":1}}`,
		`{"kind":"other"}`, `{"kind":"limit","spec":{"tokens":0}}`, `{"kind":"none","extra":1}`, `[]`,
	}
	for _, in := range bad {
		var o Override
		if err := json.Unmarshal([]byte(in), &o); err == nil {
			t.Errorf("override %s = %+v, want error", in, o)
		}
	}
}
