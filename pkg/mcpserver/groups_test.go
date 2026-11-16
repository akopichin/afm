package mcpserver

import "testing"

func TestGroupsDefaultIsReadOnly(t *testing.T) {
	g, err := ParseGroups("")
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Names()) != 0 {
		t.Fatalf("empty list enabled %v", g.Names())
	}
	var zero Groups
	if zero.Has(GroupApprove) {
		t.Fatal("zero value allows approve")
	}
}

func TestGroupsParseExplicitList(t *testing.T) {
	g, err := ParseGroups(" stages , reply,stages")
	if err != nil {
		t.Fatal(err)
	}
	if !g.Has(GroupReply) || !g.Has(GroupStages) || g.Has(GroupApprove) || g.Has(GroupBudget) {
		t.Fatalf("enabled = %v", g.Names())
	}
	if names := g.Names(); len(names) != 2 || names[0] != GroupReply || names[1] != GroupStages {
		t.Fatalf("Names = %v, want stable [reply stages]", names)
	}
}

func TestGroupsRejectUnknownAndAll(t *testing.T) {
	for _, in := range []string{"all", "reply,all", "write", "read", "reply;stages"} {
		if _, err := ParseGroups(in); err == nil {
			t.Errorf("ParseGroups(%q) accepted", in)
		}
	}
}
