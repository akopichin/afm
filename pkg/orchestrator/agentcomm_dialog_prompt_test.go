package orchestrator

import (
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/prompts"
)

func TestAgentCommPrompt_HumanDialogRouting(t *testing.T) {
	const routing = "When your task requires a question, clarification, choice, or approval from the human"
	for _, tc := range []struct {
		name        string
		mode        assistants.CollaborationMode
		interactive bool
		mesh        bool
		wantRouting bool
	}{
		{"assistants interactive", assistants.CollaborationModeAssistants, true, true, true},
		{"assistants non-interactive", assistants.CollaborationModeAssistants, false, true, false},
		{"team interactive", assistants.CollaborationModeTeam, true, true, false},
		{"team non-interactive", assistants.CollaborationModeTeam, false, true, false},
		{"default interactive", "", true, true, false},
		{"mesh unavailable", assistants.CollaborationModeAssistants, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stage := flow.Stage{ID: "dialog", Interactive: tc.interactive}
			o, _ := newAssistantsTestOrchestrator(t, true, stage)
			o.opts.CollaborationMode = tc.mode
			if tc.mesh {
				o.opts.DashboardURL = "http://127.0.0.1:1"
			}
			in := o.baseStageInputs(stage, t.TempDir(), tc.interactive)
			in.PhaseAgent = prompts.AgentImplementation
			prompt := prompts.Build(in)
			if got := strings.Contains(prompt, routing); got != tc.wantRouting {
				t.Fatalf("human dialog routing = %v, want %v:\n%s", got, tc.wantRouting, prompt)
			}
			if strings.Contains(o.agentCommBlockForTask("missing-task"), routing) {
				t.Fatal("helper must not receive the stage author's dialog routing")
			}
			if !tc.wantRouting {
				return
			}
			for _, want := range []string{
				"<interactive_rules>",
				"use `afm stage ask`, then `afm stage wait-answer`",
				"Do not substitute a mesh message or `afm agent wait` for this dialog",
				"do not ask the user to @mention you to continue",
				"This rule takes precedence over the mesh wait instructions below",
				"Answer every message from the human",
				"afm agent reply --in-reply-to",
			} {
				if !strings.Contains(prompt, want) {
					t.Errorf("prompt missing %q", want)
				}
			}
			if strings.Index(prompt, routing) > strings.Index(prompt, "Available operations,") {
				t.Error("human dialog routing must precede the mesh operations")
			}
			if strings.Contains(prompt, "uses to talk to each other and to the human") {
				t.Error("the mesh introduction must distinguish chat replies from stage questions")
			}
		})
	}
}
