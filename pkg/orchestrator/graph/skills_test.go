package graph_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator/graph"
	"github.com/akopichin/afm/pkg/skills"
)

func TestRestoreOrPersist_SavedSkillsWinOverTheNewFlow(t *testing.T) {
	dir := t.TempDir()
	first := []flow.Stage{{ID: "a", Skills: skills.Names{"tdd"}, SkillsDir: "/lib/one"}}
	if _, err := graph.RestoreOrPersist(dir, first); err != nil {
		t.Fatal(err)
	}

	edited := []flow.Stage{{ID: "a", Skills: skills.Names{"docs"}, SkillsDir: "/lib/two"}}
	got, err := graph.RestoreOrPersist(dir, edited)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got[0].Skills, []string{"tdd"}) || got[0].SkillsDir != "/lib/one" {
		t.Fatalf("restored stage skills %v dir %q, want the saved ones", got[0].Skills, got[0].SkillsDir)
	}
}

// A registry written before skills_dir existed has no such key; its skills list is still a plain array.
func TestLoadRegistry_LegacySkillsList(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "graph_registry.json"), []byte(`[{"ID":"a","Skills":["tdd","docs"]},{"ID":"b"}]`), 0o644); err != nil {
		t.Fatal(err)
	}

	got, found, err := graph.LoadRegistry(dir)
	if err != nil || !found {
		t.Fatalf("LoadRegistry = %v, %v", found, err)
	}
	if !slices.Equal(got[0].Skills, []string{"tdd", "docs"}) || got[0].SkillsDir != "" || len(got[1].Skills) != 0 {
		t.Fatalf("legacy registry decoded as %+v", got)
	}
}

func TestRegistry_DoesNotAliasSkills(t *testing.T) {
	in := []flow.Stage{{ID: "a", Skills: skills.Names{"tdd", "docs"}}}
	reg := graph.NewRegistry(in)

	in[0].Skills[0] = "changed-by-caller"
	reg.Stages()[0].Skills[1] = "changed-through-stages"
	reg.Stage("a").Skills[1] = "changed-through-stage"
	stage, _ := reg.StageByID("a")
	stage.Skills[1] = "changed-through-stage-by-id"

	if got := reg.Stages()[0].Skills; !slices.Equal(got, []string{"tdd", "docs"}) {
		t.Fatalf("registry skills = %v, want them untouched by callers", got)
	}
}
