package assistants_test

import (
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
)

func TestRegistry_LayerOverridesWholeProfile(t *testing.T) {
	global := []assistants.Profile{{ID: "x", Title: "G", Instructions: "ginstr", Command: "claude"}}
	project := []assistants.Profile{{ID: "x", Title: "P", Command: "claude"}} // no Instructions: must NOT inherit from global
	reg, err := assistants.BuildRegistry(nil, global, project)
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
	if _, err := assistants.BuildRegistry(nil, global, nil); err == nil {
		t.Fatal("expected an error for a duplicate profile id within one layer")
	}
}

// TestRegistry_BuiltinDefaultIsARoleWithoutCommand — the builtin "default" profile is always there, carries
// general instructions and names no command: the executor comes from where the task runs (ResolveExecution).
func TestRegistry_BuiltinDefaultIsARoleWithoutCommand(t *testing.T) {
	reg, err := assistants.BuildRegistry(assistants.BuiltinProfiles(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := reg.Get(assistants.DefaultProfileID)
	if !ok {
		t.Fatal("builtin default profile missing")
	}
	if p.Command != "" || p.Capabilities.Delegate || p.Instructions == "" || p.Title == "" {
		t.Fatalf("default = %+v, want a titled role with instructions, no command and no delegation", p)
	}
}

// A configured profile keeps exactly what it names: no layer fills in a command it left out.
func TestRegistry_ProfilesKeepTheirOwnCommand(t *testing.T) {
	global := []assistants.Profile{{ID: "x"}, {ID: "y", Command: "codex-as-claude"}}
	reg, err := assistants.BuildRegistry(assistants.BuiltinProfiles(), global, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := reg.Get("x"); p.Command != "" {
		t.Errorf("x.Command = %q, want empty (inherited at task creation, not in the registry)", p.Command)
	}
	if p, _ := reg.Get("y"); p.Command != "codex-as-claude" {
		t.Errorf("y.Command = %q, want codex-as-claude", p.Command)
	}
}

// A global or project "default" replaces the builtin one as a whole, like any other profile id.
func TestRegistry_ConfiguredDefaultReplacesBuiltin(t *testing.T) {
	project := []assistants.Profile{{ID: assistants.DefaultProfileID, Title: "Mine", Command: "codex-as-claude"}}
	reg, err := assistants.BuildRegistry(assistants.BuiltinProfiles(), nil, project)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := reg.Get(assistants.DefaultProfileID)
	if p.Title != "Mine" || p.Command != "codex-as-claude" || p.Instructions != "" {
		t.Fatalf("default = %+v, want the project's definition in full", p)
	}
}

// A configured profile whose id differs from the builtin default's only in case replaces it, as before the builtin
// existed: the user never wrote the builtin, so it must not make their config ambiguous. Two configured ids that
// collide that way still do.
func TestRegistry_ConfiguredIDInAnotherCaseReplacesBuiltin(t *testing.T) {
	for _, layers := range [][2][]assistants.Profile{
		{{{ID: "Default", Command: "my-cli"}}, nil},
		{nil, {{ID: "DEFAULT", Command: "my-cli"}}},
	} {
		reg, err := assistants.BuildRegistry(assistants.BuiltinProfiles(), layers[0], layers[1])
		if err != nil {
			t.Fatalf("BuildRegistry: %v", err)
		}
		if _, ok := reg.Get(assistants.DefaultProfileID); ok {
			t.Error("the builtin default is still registered next to the configured one")
		}
		if id, ok := reg.Resolve("default"); !ok || id == assistants.DefaultProfileID {
			t.Errorf("@default resolves to %q, %v; want the configured profile", id, ok)
		}
	}
	for name, layers := range map[string][2][]assistants.Profile{
		"Default and DEFAULT across layers":   {{{ID: "Default"}}, {{ID: "DEFAULT"}}},
		"default and Default in one layer":    {nil, {{ID: "default"}, {ID: "Default"}}},
		"global default, project Default":     {{{ID: "default"}}, {{ID: "Default"}}},
		"global Default, project default":     {{{ID: "Default"}}, {{ID: "default"}}},
		"global default+Default, project nil": {{{ID: "Default"}, {ID: "default"}}, nil},
	} {
		if _, err := assistants.BuildRegistry(assistants.BuiltinProfiles(), layers[0], layers[1]); err == nil {
			t.Errorf("%s: two configured ids differing only in case must still be rejected", name)
		}
	}
}

func TestRegistry_SnapshotIsImmutable(t *testing.T) {
	global := []assistants.Profile{{ID: "x", Title: "Original", Command: "claude"}}
	reg, err := assistants.BuildRegistry(nil, global, nil)
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
		if _, err := assistants.BuildRegistry(nil, global, nil); err == nil {
			t.Errorf("id %q: expected rejection (unsafe for use as a path component)", id)
		}
	}
}

func TestRegistry_ListIsSortedAndStable(t *testing.T) {
	global := []assistants.Profile{{ID: "zebra", Command: "claude"}, {ID: "alpha", Command: "claude"}}
	reg, err := assistants.BuildRegistry(nil, global, nil)
	if err != nil {
		t.Fatal(err)
	}
	profiles := reg.List()
	if len(profiles) != 2 || profiles[0].ID != "alpha" || profiles[1].ID != "zebra" {
		t.Errorf("List() = %+v, want sorted [alpha, zebra]", profiles)
	}
}

func TestRegistry_GetUnknownIDReturnsFalse(t *testing.T) {
	reg, err := assistants.BuildRegistry(nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Get("nope"); ok {
		t.Error("Get(unknown) ok = true, want false")
	}
}

// TestMentionNormalize_CaseInsensitiveUniqueIds — T4.1: a mention addresses a ROLE by id, and the match must
// be case-insensitive (`@QAEngineer` and `@qaengineer` both reach the one profile "QAEngineer"). Two
// DIFFERENT ids that collide once lowercased is a registry build error, not a silent pick of one of them —
// such a config is ambiguous for every future `@mention` and must fail loudly at startup, not at mention time.
func TestMentionNormalize_CaseInsensitiveUniqueIds(t *testing.T) {
	global := []assistants.Profile{{ID: "QAEngineer", Command: "claude"}}
	reg, err := assistants.BuildRegistry(nil, global, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, mention := range []string{"QAEngineer", "qaengineer", "QAENGINEER", "qaEngineer"} {
		id, ok := reg.Resolve(mention)
		if !ok || id != "QAEngineer" {
			t.Errorf("Resolve(%q) = (%q, %v), want (QAEngineer, true)", mention, id, ok)
		}
	}

	collidingGlobal := []assistants.Profile{{ID: "QAEngineer", Command: "claude"}, {ID: "qaengineer", Command: "claude"}}
	if _, err := assistants.BuildRegistry(nil, collidingGlobal, nil); err == nil {
		t.Fatal("expected an error for two ids colliding once lowercased within one layer")
	}

	// Collision can also span configured layers: a later layer does not exactly match the earlier id (different
	// case), so it does NOT replace it — it silently introduces an ambiguous second entry unless rejected. Only a
	// builtin id is replaced that way (TestRegistry_ConfiguredIDInAnotherCaseReplacesBuiltin).
	global = []assistants.Profile{{ID: "QAEngineer", Command: "claude"}}
	project := []assistants.Profile{{ID: "qaengineer", Command: "claude"}}
	if _, err := assistants.BuildRegistry(nil, global, project); err == nil {
		t.Fatal("expected an error for ids colliding once lowercased across layers")
	}
}

// TestRegistry_ResolveNilSafe — a nil *Registry (collaboration off) must not panic: Resolve always reports
// "not found" instead, matching the codebase's "nil-receiver → capability off" convention.
func TestRegistry_ResolveNilSafe(t *testing.T) {
	var reg *assistants.Registry
	if id, ok := reg.Resolve("anything"); ok {
		t.Errorf("nil registry Resolve() = (%q, true), want ok=false", id)
	}
}

// TestMentionNormalize_UnknownMentionIsNotAnError — an unrecognized `@word` is not an error anywhere in the
// mention path: it stays plain text and never triggers anything (dev-plan.md §4.1 acceptance).
func TestMentionNormalize_UnknownMentionIsNotAnError(t *testing.T) {
	reg, err := assistants.BuildRegistry(nil, []assistants.Profile{{ID: "frontendDeveloper", Command: "claude"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	id, ok := reg.Resolve("totallyUnknownRole")
	if ok {
		t.Errorf("Resolve(unknown) = (%q, true), want ok=false", id)
	}
}
