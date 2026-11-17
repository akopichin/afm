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
	for _, in := range []string{"all", "reply,all", "write", "reply;stages"} {
		if _, err := ParseGroups(in); err == nil {
			t.Errorf("ParseGroups(%q) accepted", in)
		}
	}
}

func TestGroupsReadKeywordIsReadOnly(t *testing.T) {
	g, err := ParseGroups(" read ")
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Names()) != 0 {
		t.Fatalf("read enabled %v, want no write groups", g.Names())
	}
}

func TestGroupsReadKeywordCannotBeMixed(t *testing.T) {
	for _, in := range []string{"read,reply", "reply,read", "read,read"} {
		if _, err := ParseGroups(in); err == nil {
			t.Errorf("ParseGroups(%q) accepted", in)
		}
	}
}

func TestAllGroups(t *testing.T) {
	names := AllGroups().Names()
	want := []Group{GroupReply, GroupStages, GroupApprove, GroupBudget}
	if len(names) != len(want) {
		t.Fatalf("AllGroups = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("AllGroups = %v, want %v", names, want)
		}
	}
}
