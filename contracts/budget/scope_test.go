package budget

import "testing"

func TestScopeID(t *testing.T) {
	cases := []struct {
		id   ScopeID
		kind ScopeKind
		obj  string
	}{
		{RunScope(), ScopeRun, ""},
		{StageScope("implement"), ScopeStage, "implement"},
		{TaskScope("t-1"), ScopeTask, "t-1"},
		{StageScope("a:b"), ScopeStage, "a:b"},
	}
	for _, c := range cases {
		parsed, err := ParseScopeID(string(c.id))
		if err != nil || parsed != c.id || c.id.Kind() != c.kind || c.id.ObjectID() != c.obj {
			t.Errorf("%q: parsed %q, %v, kind %q, object %q", c.id, parsed, err, c.id.Kind(), c.id.ObjectID())
		}
	}
	for _, bad := range []string{"", "stage", "stage:", "task:", "helper:x", "run:x", "Run", ":x"} {
		if _, err := ParseScopeID(bad); err == nil {
			t.Errorf("ParseScopeID(%q) must fail", bad)
		}
		if ScopeID(bad).Kind() != "" || ScopeID(bad).ObjectID() != "" {
			t.Errorf("malformed %q must have empty kind and object", bad)
		}
	}
}
