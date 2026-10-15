package assistants

import "testing"

func ptrInt(v int) *int { return &v }

func TestResolveDepth_DefaultIsThree(t *testing.T) {
	p, err := ResolveDepth(nil, nil, nil)
	if err != nil {
		t.Fatalf("ResolveDepth(nil,nil,nil) err = %v", err)
	}
	if p.MaxDepth != 3 || p.Source != DepthSourceDefault {
		t.Fatalf("ResolveDepth(nil,nil,nil) = %+v, want {3, default}", p)
	}
}

func TestResolveDepth_ProjectOverridesGlobal(t *testing.T) {
	p, err := ResolveDepth(ptrInt(2), ptrInt(5), nil)
	if err != nil {
		t.Fatalf("ResolveDepth err = %v", err)
	}
	if p.MaxDepth != 5 || p.Source != DepthSourceProject {
		t.Fatalf("ResolveDepth(global=2,project=5,flow=nil) = %+v, want {5, project}", p)
	}
}

func TestResolveDepth_FlowOverridesBoth(t *testing.T) {
	p, err := ResolveDepth(ptrInt(2), ptrInt(5), ptrInt(1))
	if err != nil {
		t.Fatalf("ResolveDepth err = %v", err)
	}
	if p.MaxDepth != 1 || p.Source != DepthSourceFlow {
		t.Fatalf("ResolveDepth(global=2,project=5,flow=1) = %+v, want {1, flow}", p)
	}
}

func TestResolveDepth_FlowCanRaiseAboveGlobal(t *testing.T) {
	// Это НЕ min(): global — дефолт, не административный потолок.
	p, err := ResolveDepth(ptrInt(2), nil, ptrInt(4))
	if err != nil {
		t.Fatalf("ResolveDepth err = %v", err)
	}
	if p.MaxDepth != 4 || p.Source != DepthSourceFlow {
		t.Fatalf("ResolveDepth(global=2,flow=4) = %+v, want {4, flow}", p)
	}
}

func TestResolveDepth_ExplicitZeroIsPreserved(t *testing.T) {
	p, err := ResolveDepth(ptrInt(3), nil, ptrInt(0))
	if err != nil {
		t.Fatalf("ResolveDepth err = %v", err)
	}
	if p.MaxDepth != 0 || p.Source != DepthSourceFlow {
		t.Fatalf("ResolveDepth(global=3,flow=0) = %+v, want {0, flow} (explicit zero, not inheritance)", p)
	}
}

func TestResolveDepth_RejectsNegative(t *testing.T) {
	if _, err := ResolveDepth(nil, nil, ptrInt(-1)); err == nil {
		t.Fatal("ResolveDepth(flow=-1) err = nil, want error")
	}
	if _, err := ResolveDepth(ptrInt(-2), nil, nil); err == nil {
		t.Fatal("ResolveDepth(global=-2) err = nil, want error")
	}
	if _, err := ResolveDepth(nil, ptrInt(-3), nil); err == nil {
		t.Fatal("ResolveDepth(project=-3) err = nil, want error")
	}
}

func TestResolveDepth_RevisionStableForSameInputs(t *testing.T) {
	p1, err := ResolveDepth(ptrInt(2), ptrInt(5), ptrInt(1))
	if err != nil {
		t.Fatalf("ResolveDepth err = %v", err)
	}
	p2, err := ResolveDepth(ptrInt(2), ptrInt(5), ptrInt(1))
	if err != nil {
		t.Fatalf("ResolveDepth err = %v", err)
	}
	if p1.Revision == "" {
		t.Fatal("Revision is empty, want a stable non-empty hash")
	}
	if p1.Revision != p2.Revision {
		t.Fatalf("Revision changed for identical inputs: %q != %q", p1.Revision, p2.Revision)
	}
}

func TestResolveDepth_RevisionChangesOnInputChange(t *testing.T) {
	p1, err := ResolveDepth(ptrInt(2), ptrInt(5), ptrInt(1))
	if err != nil {
		t.Fatalf("ResolveDepth err = %v", err)
	}
	p2, err := ResolveDepth(ptrInt(2), ptrInt(5), ptrInt(2))
	if err != nil {
		t.Fatalf("ResolveDepth err = %v", err)
	}
	if p1.Revision == p2.Revision {
		t.Fatalf("Revision unchanged after input change: both %q", p1.Revision)
	}
}
