package memory

import "testing"

func TestEvict_UnderMaxUnchanged(t *testing.T) {
	s := Store{Findings: []Finding{{ID: "a", ConfirmCount: 1}}}
	if len(Evict(s, 5).Findings) != 1 {
		t.Error("under max must be unchanged")
	}
}

func TestEvict_DropsLowestConfirmCount(t *testing.T) {
	s := Store{Findings: []Finding{
		{ID: "keep-hi", ConfirmCount: 5, LastSeen: "r1"},
		{ID: "drop-lo", ConfirmCount: 1, LastSeen: "r1"},
		{ID: "keep-mid", ConfirmCount: 3, LastSeen: "r1"},
	}}
	out := Evict(s, 2)
	if len(out.Findings) != 2 {
		t.Fatalf("want 2, got %d", len(out.Findings))
	}
	for _, f := range out.Findings {
		if f.ID == "drop-lo" {
			t.Error("lowest confirm_count should have been evicted")
		}
	}
}

func TestEvict_TieBrokenByRecency(t *testing.T) {
	s := Store{Findings: []Finding{
		{ID: "old", ConfirmCount: 1, LastSeen: "flow-20260101-000000-aaaa"},
		{ID: "new", ConfirmCount: 1, LastSeen: "flow-20260828-000000-bbbb"},
	}}
	out := Evict(s, 1)
	if len(out.Findings) != 1 || out.Findings[0].ID != "new" {
		t.Errorf("recency tie-break failed: %+v", out.Findings)
	}
}
