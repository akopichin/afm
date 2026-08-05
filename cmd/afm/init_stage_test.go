package main

import (
	"bufio"
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
)

func TestAskStageDetails_StandardModePlanningAgentDefaults(t *testing.T) {
	lines := []string{
		"",                // id -> default "implementation"
		"",                // name -> default "Implementation"
		"",                // mode -> default standard
		"build the thing", // description
		"",                // plan mode -> default: plan with an agent
		"",                // phases -> default: implementation only
		"n",               // advanced? -> no
	}
	scanner := bufio.NewScanner(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	var out bytes.Buffer
	d := stageDefaults{SuggestedID: "implementation", SuggestedName: "Implementation"}
	stage := askStageDetails(scanner, &out, d, nil)

	if stage.ID != "implementation" || stage.Name != "Implementation" {
		t.Fatalf("got ID=%q Name=%q", stage.ID, stage.Name)
	}
	if stage.Description != "build the thing" {
		t.Errorf("Description = %q", stage.Description)
	}
	want := []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation}
	if !reflect.DeepEqual(stage.Agents, want) {
		t.Errorf("Agents = %v, want %v", stage.Agents, want)
	}
	if stage.Verify != "" || stage.Interactive || len(stage.Artifacts) != 0 || stage.Plan != "" {
		t.Errorf("unexpected fields set: %+v", stage)
	}
}

func TestAskStageDetails_PlanFileWithReviewOnlyPhase(t *testing.T) {
	lines := []string{
		"",           // id -> default "check"
		"",           // name -> default "Check"
		"",           // mode -> default standard
		"check X",    // description
		"2",          // plan mode -> "I already have a plan file"
		"plans/x.md", // plan path
		"2",          // phases -> review only
		"",           // advanced? -> default no
	}
	scanner := bufio.NewScanner(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	var out bytes.Buffer
	d := stageDefaults{SuggestedID: "check", SuggestedName: "Check"}
	stage := askStageDetails(scanner, &out, d, nil)

	if stage.Plan != "plans/x.md" {
		t.Errorf("Plan = %q", stage.Plan)
	}
	want := []flow.AgentType{flow.AgentReview}
	if !reflect.DeepEqual(stage.Agents, want) {
		t.Errorf("Agents = %v, want %v (no planning agent — plan file was supplied)", stage.Agents, want)
	}
}

func TestAskStageDetails_AutoMode(t *testing.T) {
	lines := []string{
		"",  // id -> default
		"",  // name -> default
		"2", // mode -> auto (2nd menu option)
		"n", // advanced? -> no
	}
	scanner := bufio.NewScanner(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	var out bytes.Buffer
	d := stageDefaults{SuggestedID: "watch", SuggestedName: "Watch"}
	stage := askStageDetails(scanner, &out, d, nil)

	want := []flow.AgentType{flow.AgentAuto}
	if !reflect.DeepEqual(stage.Agents, want) {
		t.Errorf("Agents = %v, want %v", stage.Agents, want)
	}
	if stage.Description != "" || stage.Plan != "" {
		t.Errorf("auto mode should skip description/plan questions: %+v", stage)
	}
}

func TestAskStageDetails_ScriptMode(t *testing.T) {
	lines := []string{
		"",           // id -> default
		"",           // name -> default
		"3",          // mode -> script (3rd menu option)
		"echo hello", // shell command
		"n",          // advanced? -> no
	}
	scanner := bufio.NewScanner(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	var out bytes.Buffer
	d := stageDefaults{SuggestedID: "check", SuggestedName: "Check"}
	stage := askStageDetails(scanner, &out, d, nil)

	if stage.Script != "echo hello" {
		t.Errorf("Script = %q", stage.Script)
	}
	if len(stage.Agents) != 0 {
		t.Errorf("Agents = %v, want empty for a script stage", stage.Agents)
	}
}

func TestAskStageDetails_ForceVerifyAsksRegardlessOfAdvanced(t *testing.T) {
	lines := []string{
		"",              // id -> default
		"",              // name -> default
		"",              // mode -> default standard
		"go test suite", // description
		"",              // plan mode -> default agent
		"",              // phases -> default implementation
		"go test ./...", // forced verify command
		"n",             // advanced? -> no
	}
	scanner := bufio.NewScanner(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	var out bytes.Buffer
	d := stageDefaults{SuggestedID: "build", SuggestedName: "Build", ForceVerify: true}
	stage := askStageDetails(scanner, &out, d, nil)

	if stage.Verify != "go test ./..." {
		t.Errorf("Verify = %q", stage.Verify)
	}
}

func TestAskStageDetails_AskDepsForCustomArchetype(t *testing.T) {
	priorStages := []flow.Stage{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	lines := []string{
		"",     // name -> default "My Stage" (FixedID set, id not asked)
		"",     // mode -> default standard
		"do x", // description
		"",     // plan mode -> default agent
		"",     // phases -> default implementation
		"1,2",  // depends_on -> select "a" and "b" from the checklist, not "c"
		"n",    // advanced? -> no
	}
	scanner := bufio.NewScanner(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	var out bytes.Buffer
	d := stageDefaults{FixedID: "mystage", SuggestedName: "My Stage", AskDeps: true}
	stage := askStageDetails(scanner, &out, d, priorStages)

	if stage.ID != "mystage" {
		t.Errorf("ID = %q, want mystage (FixedID must be used verbatim)", stage.ID)
	}
	if !reflect.DeepEqual(stage.DependsOn, []string{"a", "b"}) {
		t.Errorf("DependsOn = %v", stage.DependsOn)
	}
}

func TestAskStageDetails_AskDepsSkipsQuestionWhenNoPriorStages(t *testing.T) {
	lines := []string{
		"",     // name -> default "My Stage" (FixedID set, id not asked)
		"",     // mode -> default standard
		"do x", // description
		"",     // plan mode -> default agent
		"",     // phases -> default implementation
		// no depends_on line: with zero prior stages there's nothing to
		// select, so askDependsOn must not consume a line here
		"n", // advanced? -> no
	}
	scanner := bufio.NewScanner(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	var out bytes.Buffer
	d := stageDefaults{FixedID: "first", SuggestedName: "First", AskDeps: true}
	stage := askStageDetails(scanner, &out, d, nil)

	if stage.DependsOn != nil {
		t.Errorf("DependsOn = %v, want nil (no prior stages to depend on)", stage.DependsOn)
	}
}

func TestAskStageDetails_AdvancedBlockArtifactsInputsVerifyInteractiveCommand(t *testing.T) {
	priorStages := []flow.Stage{
		{ID: "build", Artifacts: []flow.Artifact{{Name: "binary", Path: "bin/app"}}},
	}
	lines := []string{
		"",             // id -> default "check"
		"",             // name -> default "Check"
		"",             // mode -> default standard
		"verify build", // description
		"",             // plan mode -> default agent
		"",             // phases -> default implementation
		"y",            // advanced? -> yes
		"",             // artifact name -> empty, stop (no artifacts of its own)
		"build.binary", // inputs -> consume build's artifact
		"some check",   // verify command
		"y",            // interactive -> yes
		"",             // custom command -> empty (skip)
	}
	scanner := bufio.NewScanner(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	var out bytes.Buffer
	d := stageDefaults{SuggestedID: "check", SuggestedName: "Check", DependsOn: []string{"build"}}
	stage := askStageDetails(scanner, &out, d, priorStages)

	if !reflect.DeepEqual(stage.Inputs, []flow.Input{{Ref: "build.binary"}}) {
		t.Errorf("Inputs = %+v", stage.Inputs)
	}
	if stage.Verify != "some check" {
		t.Errorf("Verify = %q", stage.Verify)
	}
	if !stage.Interactive {
		t.Error("Interactive = false, want true")
	}
	if stage.Command != "" {
		t.Errorf("Command = %q, want empty", stage.Command)
	}
	if len(stage.Artifacts) != 0 {
		t.Errorf("Artifacts = %+v, want none", stage.Artifacts)
	}
}
