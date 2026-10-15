package assistants_test

import (
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
)

// alwaysAvailable is a stand-in AvailabilityCheck for tests that don't care about the host-capability gate
// itself (TestHelperCapability_* in pkg/config covers that gate directly).
func alwaysAvailable(string) (bool, string, assistants.ReadonlyEnforcement) {
	return true, "", assistants.ReadonlyEnforcementOS
}

func TestRegistry_LayerOverridesWholeProfile(t *testing.T) {
	global := []assistants.Profile{{ID: "x", Title: "G", Instructions: "ginstr", Command: "claude"}}
	project := []assistants.Profile{{ID: "x", Title: "P", Command: "claude"}} // no Instructions: must NOT inherit from global
	reg, err := assistants.BuildRegistry(nil, global, project, "claude", alwaysAvailable)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := reg.Get("x")
	if !ok {
		t.Fatal("expected profile x")
	}
	if p.Title != "P" {
		t.Errorf("Title = %q, want %q (project layer must fully replace global's definition)", p.Title, "P")
	}
	if p.Instructions != "" {
		t.Errorf("Instructions = %q, want empty — a later layer replaces the WHOLE profile, it does not merge fields from the earlier one", p.Instructions)
	}
}

func TestRegistry_DuplicateIdsWithinLayerRejected(t *testing.T) {
	global := []assistants.Profile{{ID: "x", Command: "claude"}, {ID: "x", Command: "claude"}}
	if _, err := assistants.BuildRegistry(nil, global, nil, "claude", alwaysAvailable); err == nil {
		t.Fatal("expected an error for a duplicate profile id within one layer")
	}
}

func TestRegistry_BuiltinInheritsClientCommand(t *testing.T) {
	builtin := []assistants.Profile{{ID: "reviewer", Title: "Reviewer"}} // no Command set
	reg, err := assistants.BuildRegistry(builtin, nil, nil, "claude", alwaysAvailable)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := reg.Get("reviewer")
	if !ok {
		t.Fatal("expected profile reviewer")
	}
	if p.Command != "claude" {
		t.Errorf("Command = %q, want inherited default command %q", p.Command, "claude")
	}
}

func TestRegistry_BuiltinKeepsExplicitCommand(t *testing.T) {
	builtin := []assistants.Profile{{ID: "reviewer", Command: "codex-as-claude"}}
	reg, err := assistants.BuildRegistry(builtin, nil, nil, "claude", alwaysAvailable)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := reg.Get("reviewer")
	if p.Command != "codex-as-claude" {
		t.Errorf("Command = %q, want the explicitly configured %q (default must not override it)", p.Command, "codex-as-claude")
	}
}

// TestRegistry_GlobalProjectDoNotInheritDefaultCommand — only BUILTIN profiles inherit the default command;
// global/project profiles are user-authored and must name their own command.
func TestRegistry_GlobalProjectDoNotInheritDefaultCommand(t *testing.T) {
	global := []assistants.Profile{{ID: "x"}} // no Command
	reg, err := assistants.BuildRegistry(nil, global, nil, "claude", alwaysAvailable)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := reg.Get("x")
	if p.Command != "" {
		t.Errorf("Command = %q, want empty (global layer must not inherit the default command)", p.Command)
	}
}

func TestRegistry_UnavailableCommandIsReported(t *testing.T) {
	unavailable := func(cmd string) (bool, string, assistants.ReadonlyEnforcement) {
		return false, "no provider configured for " + cmd, assistants.ReadonlyEnforcementNone
	}
	global := []assistants.Profile{{ID: "x", Command: "mystery-agent"}}
	reg, err := assistants.BuildRegistry(nil, global, nil, "claude", unavailable)
	if err != nil {
		t.Fatalf("BuildRegistry must not fail config load for an unavailable (but configured) provider: %v", err)
	}
	views := reg.List()
	if len(views) != 1 {
		t.Fatalf("List() = %d entries, want 1", len(views))
	}
	if views[0].Available {
		t.Error("Available = true, want false")
	}
	if views[0].UnavailableReason == "" {
		t.Error("UnavailableReason is empty, want a reason")
	}
}

func TestRegistry_SnapshotIsImmutable(t *testing.T) {
	global := []assistants.Profile{{ID: "x", Title: "Original", Command: "claude"}}
	reg, err := assistants.BuildRegistry(nil, global, nil, "claude", alwaysAvailable)
	if err != nil {
		t.Fatal(err)
	}
	global[0].Title = "Mutated"
	p, _ := reg.Get("x")
	if p.Title != "Original" {
		t.Errorf("Get() returned %q after mutating the source slice, want %q (BuildRegistry must copy, not alias)", p.Title, "Original")
	}
}

func TestRegistry_RejectsIdUnsafeForPath(t *testing.T) {
	for _, id := range []string{"a/b", "..", ".", "", "a\x00b", "../escape"} {
		global := []assistants.Profile{{ID: assistants.ProfileID(id), Command: "claude"}}
		if _, err := assistants.BuildRegistry(nil, global, nil, "claude", alwaysAvailable); err == nil {
			t.Errorf("id %q: expected rejection (unsafe for use as a path component)", id)
		}
	}
}

func TestRegistry_ListIsSortedAndStable(t *testing.T) {
	global := []assistants.Profile{{ID: "zebra", Command: "claude"}, {ID: "alpha", Command: "claude"}}
	reg, err := assistants.BuildRegistry(nil, global, nil, "claude", alwaysAvailable)
	if err != nil {
		t.Fatal(err)
	}
	views := reg.List()
	if len(views) != 2 || views[0].ID != "alpha" || views[1].ID != "zebra" {
		t.Errorf("List() = %+v, want sorted [alpha, zebra]", views)
	}
}

func TestRegistry_GetUnknownIDReturnsFalse(t *testing.T) {
	reg, err := assistants.BuildRegistry(nil, nil, nil, "claude", alwaysAvailable)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Get("nope"); ok {
		t.Error("Get(unknown) ok = true, want false")
	}
}
