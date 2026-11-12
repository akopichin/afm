package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm-engine/engine"
	"github.com/akopichin/afm/contracts/flow"
)

func loopHelper(profile, every, prompt string) flow.StageAssistant {
	return flow.StageAssistant{Profile: profile, Loop: &flow.StageAssistantLoop{Every: every, Prompt: prompt}}
}

func TestDeclaredAssistantLoops_NoLoopGivesNothing(t *testing.T) {
	stages := []flow.Stage{
		{ID: "a"},
		{ID: "b", Assistants: flow.StageAssistants{{Profile: "watcher"}}},
	}
	got, err := declaredAssistantLoops(stages)
	if err != nil || len(got) != 0 {
		t.Fatalf("declaredAssistantLoops = %+v, %v, want nothing", got, err)
	}
}

// The same profile on two stages is two loops: the stage is part of the loop's identity.
func TestDeclaredAssistantLoops_SameProfileOnTwoStages(t *testing.T) {
	stages := []flow.Stage{
		{ID: "build", Assistants: flow.StageAssistants{loopHelper("watcher", "1m", "check build")}},
		{ID: "docs", Assistants: flow.StageAssistants{loopHelper("watcher", "2m", "check docs")}},
	}
	got, err := declaredAssistantLoops(stages)
	if err != nil {
		t.Fatal(err)
	}
	want := []engine.StageAssistantLoop{
		{StageID: "build", ProfileID: "watcher", Every: time.Minute, Prompt: "check build"},
		{StageID: "docs", ProfileID: "watcher", Every: 2 * time.Minute, Prompt: "check docs"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("declaredAssistantLoops = %+v, want %+v", got, want)
	}
}

// Helpers without a loop are skipped; the rest keep the order they were declared in.
func TestDeclaredAssistantLoops_MixedHelpersKeepDeclarationOrder(t *testing.T) {
	stages := []flow.Stage{
		{ID: "first", Assistants: flow.StageAssistants{
			loopHelper("b", "1s", "one"),
			{Profile: "plain"},
			loopHelper("a", "90s", "two"),
		}},
		{ID: "second"},
		{ID: "third", Assistants: flow.StageAssistants{loopHelper("c", "1h", "three")}},
	}
	got, err := declaredAssistantLoops(stages)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, l := range got {
		order = append(order, l.StageID+"/"+l.ProfileID)
	}
	if want := []string{"first/b", "first/a", "third/c"}; !slices.Equal(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	if got[1].Every != 90*time.Second {
		t.Fatalf("every = %v, want 1m30s", got[1].Every)
	}
}

func TestDeclaredAssistantLoops_PromptPassesThroughUnchanged(t *testing.T) {
	const prompt = "  Check  the\nconditions.\n"
	got, err := declaredAssistantLoops([]flow.Stage{{ID: "s", Assistants: flow.StageAssistants{loopHelper("w", "1s", prompt)}}})
	if err != nil || len(got) != 1 || got[0].Prompt != prompt {
		t.Fatalf("declaredAssistantLoops = %+v, %v, want the prompt untouched", got, err)
	}
}

// A flow built in code skips validation; the converter still refuses it and keeps the prompt out of the error.
func TestDeclaredAssistantLoops_InvalidEveryNamesStageAndProfileNotPrompt(t *testing.T) {
	const marker = "PROMPT-MARKER-91c2"
	stages := []flow.Stage{{ID: "build", Assistants: flow.StageAssistants{loopHelper("watcher", "soon", marker)}}}
	got, err := declaredAssistantLoops(stages)
	if err == nil {
		t.Fatalf("declaredAssistantLoops = %+v, want an error", got)
	}
	for _, want := range []string{`stage "build"`, `assistant "watcher"`, "loop.every"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
	if strings.Contains(err.Error(), marker) {
		t.Errorf("error %q leaks the prompt", err)
	}
}
