package memory

import (
	"strings"
	"testing"
)

func mk(id string, scope string, cc int, topic, stmt string) Finding {
	return Finding{ID: id, Scope: scope, Kind: KindFact, Topic: []string{topic}, Statement: stmt, Evidence: "e:1", ConfirmCount: cc, LastSeen: "r1"}
}

func TestSelect_SmallStoreInjectsAll(t *testing.T) {
	proj := Store{Findings: []Finding{mk("a", ScopeProject, 1, "db", "uses sqlite")}}
	_, all := Select(proj, Store{}, []string{"anything"}, RetrievalConfig{Threshold: 25, CoreConfirmCount: 3})
	if !all {
		t.Error("small store must signal injectAll")
	}
}

func TestSelect_LargeStorePicksCoreAndRelevant(t *testing.T) {
	proj := Store{}
	for i := 0; i < 30; i++ {
		proj.Findings = append(proj.Findings, mk("f"+string(rune('a'+i)), ScopeProject, 1, "misc", "irrelevant thing"))
	}
	proj.Findings = append(proj.Findings,
		mk("core1", ScopeProject, 5, "misc", "core high-confidence rule"),                  // core by confirm_count
		mk("rel1", ScopeProject, 1, "database", "relevant to build stage database schema"), // relevant by token
	)
	sess := Store{Findings: []Finding{mk("s1", ScopeSession, 1, "x", "session ctx")}}
	got, all := Select(proj, sess, Tokenize("build database migration"), RetrievalConfig{Threshold: 25, CoreConfirmCount: 3})
	if all {
		t.Fatal("large store must not injectAll")
	}
	ids := map[string]bool{}
	for _, f := range got {
		ids[f.ID] = true
	}
	if !ids["core1"] || !ids["rel1"] || !ids["s1"] {
		t.Errorf("must include core, relevant, and all session; got %v", ids)
	}
	if ids["fa"] {
		t.Error("irrelevant low-confidence finding must be excluded")
	}
}

func TestRender_CompactAndDeterministic(t *testing.T) {
	out := Render([]Finding{mk("a", ScopeProject, 1, "db", "uses sqlite")})
	if !strings.Contains(out, "uses sqlite") || !strings.Contains(out, "e:1") {
		t.Errorf("render missing content: %q", out)
	}
	if Render(nil) != "" {
		t.Error("empty render must be empty string")
	}
}
