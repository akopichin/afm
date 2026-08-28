package memory

import "testing"

func TestReconcile_NewGetsMetadataAndSlug(t *testing.T) {
	merged := MergedStore{Findings: []MergedFinding{
		{Finding: Finding{Scope: ScopeProject, Kind: KindFact, Statement: "Server uses sqlite", Evidence: "config.json:1"}, Status: StatusNew},
	}}
	out := Reconcile(Store{}, merged, "r5")
	if len(out.Findings) != 1 {
		t.Fatalf("want 1, got %d", len(out.Findings))
	}
	f := out.Findings[0]
	if f.ID == "" || f.FirstSeen != "r5" || f.LastSeen != "r5" || f.ConfirmCount != 1 {
		t.Errorf("new finding metadata wrong: %+v", f)
	}
}

func TestReconcile_ReinforcedBumpsCountKeepsFirstSeen(t *testing.T) {
	prev := Store{Findings: []Finding{{ID: "sqlite-db", Scope: ScopeProject, Kind: KindFact, Statement: "uses sqlite", Evidence: "config.json:1", FirstSeen: "r1", LastSeen: "r1", ConfirmCount: 2}}}
	merged := MergedStore{Findings: []MergedFinding{
		{Finding: Finding{ID: "sqlite-db", Scope: ScopeProject, Kind: KindFact, Statement: "uses sqlite (db layer)", Evidence: "config.json:1"}, Status: StatusReinforced},
	}}
	out := Reconcile(prev, merged, "r5")
	f := out.Findings[0]
	if f.FirstSeen != "r1" || f.LastSeen != "r5" || f.ConfirmCount != 3 {
		t.Errorf("reinforced metadata wrong: %+v", f)
	}
}

func TestReconcile_DropsInvalidAndDedupesSlug(t *testing.T) {
	merged := MergedStore{Findings: []MergedFinding{
		{Finding: Finding{Scope: ScopeProject, Kind: KindFact, Statement: "Same topic", Evidence: "a:1"}, Status: StatusNew},
		{Finding: Finding{Scope: ScopeProject, Kind: KindFact, Statement: "Same topic", Evidence: "b:2"}, Status: StatusNew},
		{Finding: Finding{Scope: ScopeProject, Kind: KindFact, Statement: "no evidence"}, Status: StatusNew}, // invalid → dropped
	}}
	out := Reconcile(Store{}, merged, "r1")
	if len(out.Findings) != 2 {
		t.Fatalf("want 2 valid, got %d", len(out.Findings))
	}
	if out.Findings[0].ID == out.Findings[1].ID {
		t.Error("colliding slugs must be deduped")
	}
}

func TestReconcile_CollisionSuffixesAreCorrect(t *testing.T) {
	// Test that colliding slugs get -2, -3, -4 suffixes (not -0, -1, -2)
	merged := MergedStore{Findings: []MergedFinding{
		{Finding: Finding{Scope: ScopeProject, Kind: KindFact, Statement: "Same topic", Evidence: "a:1"}, Status: StatusNew},
		{Finding: Finding{Scope: ScopeProject, Kind: KindFact, Statement: "Same topic", Evidence: "b:2"}, Status: StatusNew},
		{Finding: Finding{Scope: ScopeProject, Kind: KindFact, Statement: "Same topic", Evidence: "c:3"}, Status: StatusNew},
	}}
	out := Reconcile(Store{}, merged, "r1")
	if len(out.Findings) != 3 {
		t.Fatalf("want 3 findings, got %d", len(out.Findings))
	}
	ids := map[string]bool{}
	for _, f := range out.Findings {
		ids[f.ID] = true
	}
	// First one gets the base slug, next two get -2 and -3
	if !ids["same-topic"] {
		t.Error("first collision must keep base slug")
	}
	if !ids["same-topic-2"] {
		t.Error("second collision must get -2 suffix")
	}
	if !ids["same-topic-3"] {
		t.Error("third collision must get -3 suffix")
	}
}
