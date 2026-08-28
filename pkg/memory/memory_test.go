package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func validFinding() Finding {
	return Finding{ID: "sqlite-db", Scope: ScopeProject, Kind: KindFact, Topic: []string{"db"},
		Statement: "uses sqlite", Evidence: "config.json:1", FirstSeen: "r1", LastSeen: "r1", ConfirmCount: 1, SourceStage: "step1"}
}

func TestValid_RejectsMissingEvidenceAndBadFields(t *testing.T) {
	if !validFinding().Valid() {
		t.Fatal("baseline should be valid")
	}
	noEv := validFinding()
	noEv.Evidence = ""
	if noEv.Valid() {
		t.Error("empty evidence must be invalid")
	}
	badScope := validFinding()
	badScope.Scope = "global"
	if badScope.Valid() {
		t.Error("unknown scope must be invalid")
	}
	badKind := validFinding()
	badKind.Kind = "rule"
	if badKind.Valid() {
		t.Error("unknown kind must be invalid")
	}
	badID := validFinding()
	badID.ID = "Bad ID!"
	if badID.Valid() {
		t.Error("bad id charset must be invalid")
	}
}

func TestSanitize_DropsInvalid(t *testing.T) {
	bad := validFinding()
	bad.Evidence = ""
	s := Store{Findings: []Finding{validFinding(), bad}}.Sanitize()
	if len(s.Findings) != 1 {
		t.Fatalf("want 1 valid finding, got %d", len(s.Findings))
	}
}

func TestLoadMissing_IsEmpty(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil || len(s.Findings) != 0 {
		t.Fatalf("missing → empty,nil; got %v err=%v", s, err)
	}
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "m.yaml")
	in := Store{Findings: []Finding{validFinding()}}
	if err := Save(p, in); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal("file not written")
	}
	out, err := Load(p)
	if err != nil || len(out.Findings) != 1 || out.Findings[0].ID != "sqlite-db" {
		t.Fatalf("round-trip mismatch: %v err=%v", out, err)
	}
}
