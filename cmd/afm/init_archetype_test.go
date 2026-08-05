package main

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
	"gopkg.in/yaml.v3"
)

// parseGeneratedFlow marshals f, writes it to a temp file, and parses it
// back through flow.ParseFile — failing the test if the generated
// flow.yaml doesn't pass real schema validation. Shared by later tasks'
// tests in this package.
func parseGeneratedFlow(t *testing.T, f *flow.Flow) *flow.Flow {
	t.Helper()
	data, err := yaml.Marshal(f)
	if err != nil {
		t.Fatalf("marshal flow: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "flow.yaml")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write flow: %v", err)
	}
	parsed, err := flow.ParseFile(path)
	if err != nil {
		t.Fatalf("generated flow.yaml is invalid: %v\n---\n%s", err, data)
	}
	return parsed
}

func TestBuildSingleChangeStages_ProducesValidFlow(t *testing.T) {
	lines := []string{
		"", "", "", "ship the feature", "", "", "n",
	}
	scanner := bufio.NewScanner(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	var out bytes.Buffer
	stages := buildSingleChangeStages(scanner, &out)
	if len(stages) != 1 || stages[0].ID != "implementation" {
		t.Fatalf("got %+v", stages)
	}
	f := &flow.Flow{Name: "test", Description: "d", Stages: stages}
	parseGeneratedFlow(t, f)
}

func TestBuildVerifyLoopStages_CheckDefaultsToScriptAndIsValid(t *testing.T) {
	// build: id/name defaults, mode default (standard), description,
	// plan mode default (agent), phases default (implementation), forced
	// verify command, advanced -> no.
	buildLines := []string{"", "", "", "implement the feature", "", "", "go test ./...", "n"}
	// check: id/name defaults, mode default (script, per DefaultMode) —
	// note the mode prompt itself still reads one line even though the
	// answer is empty (accepting the default), script command, advanced -> no.
	checkLines := []string{"", "", "", "go vet ./...", "n"}
	full := strings.Join(buildLines, "\n") + "\n" + strings.Join(checkLines, "\n") + "\n"
	scanner := bufio.NewScanner(strings.NewReader(full))
	var out bytes.Buffer
	stages := buildVerifyLoopStages(scanner, &out)
	if len(stages) != 2 {
		t.Fatalf("got %d stages, want 2", len(stages))
	}
	build, check := stages[0], stages[1]
	if build.Verify != "go test ./..." {
		t.Errorf("build.Verify = %q", build.Verify)
	}
	if check.Script != "go vet ./..." {
		t.Errorf("check.Script = %q, want the default script mode to be used", check.Script)
	}
	if len(check.Agents) != 0 {
		t.Errorf("check.Agents = %v, want empty (script stage)", check.Agents)
	}
	if len(check.DependsOn) != 1 || check.DependsOn[0] != build.ID {
		t.Errorf("check.DependsOn = %v", check.DependsOn)
	}
	f := &flow.Flow{Name: "test", Description: "d", Stages: stages}
	parseGeneratedFlow(t, f) // fails the test if the generated flow.yaml doesn't validate
}

func TestBuildParallelTracksStages_DefaultTwoTracksPlusIntegration(t *testing.T) {
	trackAnswers := []string{"", "", "", "track work", "", "", "n"}
	full := "\n" + // "How many parallel tracks?" -> default 2
		strings.Join(trackAnswers, "\n") + "\n" + // track-1
		strings.Join(trackAnswers, "\n") + "\n" + // track-2
		strings.Join(trackAnswers, "\n") + "\n" // integration
	scanner := bufio.NewScanner(strings.NewReader(full))
	var out bytes.Buffer
	stages := buildParallelTracksStages(scanner, &out)
	if len(stages) != 3 {
		t.Fatalf("got %d stages, want 3 (2 tracks + integration)", len(stages))
	}
	integration := stages[2]
	if len(integration.DependsOn) != 2 {
		t.Errorf("integration.DependsOn = %v, want 2 entries", integration.DependsOn)
	}
	f := &flow.Flow{Name: "test", Description: "d", Stages: stages}
	parseGeneratedFlow(t, f)
}

func TestBuildParallelTracksStages_NegativeCountClampsToOne(t *testing.T) {
	trackAnswers := []string{"", "", "", "track work", "", "", "n"}
	full := "-1\n" + // "How many parallel tracks?" -> negative, must clamp to 1
		strings.Join(trackAnswers, "\n") + "\n" + // track-1
		strings.Join(trackAnswers, "\n") + "\n" // integration
	scanner := bufio.NewScanner(strings.NewReader(full))
	var out bytes.Buffer
	stages := buildParallelTracksStages(scanner, &out)
	if len(stages) != 2 {
		t.Fatalf("got %d stages, want 2 (1 track + integration)", len(stages))
	}
	integration := stages[1]
	if len(integration.DependsOn) != 1 {
		t.Errorf("integration.DependsOn = %v, want 1 entry", integration.DependsOn)
	}
	f := &flow.Flow{Name: "test", Description: "d", Stages: stages}
	parseGeneratedFlow(t, f)
}

func TestBuildArchetypeStages_DispatchesToEachBuilder(t *testing.T) {
	singleChangeLines := []string{"", "", "", "ship the feature", "", "", "n"}
	trackAnswers := []string{"", "", "", "track work", "", "", "n"}
	parallelTracksLines := "\n" + strings.Join(trackAnswers, "\n") + "\n" + strings.Join(trackAnswers, "\n") + "\n" + strings.Join(trackAnswers, "\n") + "\n"
	customLines := []string{"alpha", "", "", "do alpha", "", "", "n", ""}
	verifyLoopLines := strings.Join([]string{"", "", "", "implement the feature", "", "", "go test ./...", "n"}, "\n") + "\n" +
		strings.Join([]string{"", "", "", "go vet ./...", "n"}, "\n") + "\n"

	cases := []struct {
		name      string
		archetype int
		input     string
		wantIDs   []string
	}{
		{"single change", archetypeSingleChange, strings.Join(singleChangeLines, "\n") + "\n", []string{"implementation"}},
		{"verify loop", archetypeVerifyLoop, verifyLoopLines, []string{"build", "check"}},
		{"parallel tracks", archetypeParallelTracks, parallelTracksLines, []string{"track-1", "track-2", "integration"}},
		{"custom", archetypeCustom, strings.Join(customLines, "\n") + "\n", []string{"alpha"}},
	}

	if len(archetypeOptions) != 4 {
		t.Fatalf("archetypeOptions has %d entries, want 4 (one per archetype)", len(archetypeOptions))
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scanner := bufio.NewScanner(strings.NewReader(tc.input))
			var out bytes.Buffer
			stages := buildArchetypeStages(tc.archetype, scanner, &out)
			if len(stages) != len(tc.wantIDs) {
				t.Fatalf("got %d stages, want %d (%+v)", len(stages), len(tc.wantIDs), stages)
			}
			for i, id := range tc.wantIDs {
				if stages[i].ID != id {
					t.Errorf("stages[%d].ID = %q, want %q", i, stages[i].ID, id)
				}
			}
			f := &flow.Flow{Name: "test", Description: "d", Stages: stages}
			parseGeneratedFlow(t, f)
		})
	}
}

func TestBuildCustomStages_StopsOnEmptyID(t *testing.T) {
	stageLines := []string{
		"alpha",    // outer loop: stage ID
		"",         // name -> default "alpha"
		"",         // mode -> default standard
		"do alpha", // description
		"",         // plan mode -> default agent
		"",         // phases -> default implementation
		// no depends_on line: "alpha" is the first stage, no prior
		// stages exist yet, so the checklist question is skipped
		"n", // advanced? -> no
		"",  // outer loop: empty ID -> stop
	}
	scanner := bufio.NewScanner(strings.NewReader(strings.Join(stageLines, "\n") + "\n"))
	var out bytes.Buffer
	stages := buildCustomStages(scanner, &out)
	if len(stages) != 1 || stages[0].ID != "alpha" {
		t.Fatalf("got %+v", stages)
	}
	f := &flow.Flow{Name: "test", Description: "d", Stages: stages}
	parseGeneratedFlow(t, f)
}

func TestBuildCustomStages_DependsOnOffersOnlyEarlierStagesAsChecklist(t *testing.T) {
	stageLines := []string{
		// stage 1: "alpha" — no prior stages, no depends_on question
		"alpha", "", "", "do alpha", "", "", "n",
		// stage 2: "beta" — checklist now offers only "alpha"; select it by index
		"beta", "", "", "do beta", "", "", "1", "n",
		// stop
		"",
	}
	scanner := bufio.NewScanner(strings.NewReader(strings.Join(stageLines, "\n") + "\n"))
	var out bytes.Buffer
	stages := buildCustomStages(scanner, &out)
	if len(stages) != 2 {
		t.Fatalf("got %d stages, want 2: %+v", len(stages), stages)
	}
	beta := stages[1]
	if !reflect.DeepEqual(beta.DependsOn, []string{"alpha"}) {
		t.Errorf("beta.DependsOn = %v, want [alpha] (selected by index from the checklist, not typed)", beta.DependsOn)
	}
	if !strings.Contains(out.String(), "1. alpha") {
		t.Errorf("expected the checklist to list \"alpha\" as option 1, got:\n%s", out.String())
	}
	f := &flow.Flow{Name: "test", Description: "d", Stages: stages}
	parseGeneratedFlow(t, f)
}
