package flow_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/akopichin/afm/contracts/assistants"
	"github.com/akopichin/afm/contracts/flow"
)

func TestStageAssistants_Scalar(t *testing.T) {
	yamlDoc := `
name: t
stages:
  - id: s1
    agents: [auto]
    assistants: reviewer
`
	f, err := flow.ParseFile(writeTemp(t, yamlDoc))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	want := flow.StageAssistants{{Profile: "reviewer"}}
	if got := f.Stages[0].Assistants; len(got) != 1 || !reflect.DeepEqual(got[0], want[0]) {
		t.Fatalf("Assistants = %+v, want %+v", got, want)
	}
}

func TestStageAssistants_SingleElementList(t *testing.T) {
	yamlDoc := `
name: t
stages:
  - id: s1
    agents: [auto]
    assistants: [reviewer]
`
	f, err := flow.ParseFile(writeTemp(t, yamlDoc))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	want := flow.StageAssistants{{Profile: "reviewer"}}
	if got := f.Stages[0].Assistants; len(got) != 1 || !reflect.DeepEqual(got[0], want[0]) {
		t.Fatalf("Assistants = %+v, want %+v", got, want)
	}
}

func TestStageAssistants_MultiElementListPreservesOrder(t *testing.T) {
	yamlDoc := `
name: t
stages:
  - id: s1
    agents: [auto]
    assistants: [reviewer, qa]
`
	f, err := flow.ParseFile(writeTemp(t, yamlDoc))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	got := f.Stages[0].Assistants
	if len(got) != 2 || got[0].Profile != "reviewer" || got[1].Profile != "qa" {
		t.Fatalf("Assistants = %+v, want [reviewer, qa] in order", got)
	}
}

func TestStageAssistants_ObjectAllFields(t *testing.T) {
	yamlDoc := `
name: t
stages:
  - id: s1
    agents: [auto]
    assistants:
      - profile: qa
        title: "QA gate"
        instruction: "Check test coverage"
        mode: required
        trigger: on_completion
`
	f, err := flow.ParseFile(writeTemp(t, yamlDoc))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	got := f.Stages[0].Assistants
	want := flow.StageAssistant{
		Profile: "qa", Title: "QA gate", Instruction: "Check test coverage",
		Mode: assistants.ModeRequired, Trigger: assistants.TriggerOnCompletion,
	}
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("Assistants = %+v, want [%+v]", got, want)
	}
}

func TestStageAssistants_MixedListOfNameAndObject(t *testing.T) {
	yamlDoc := `
name: t
stages:
  - id: s1
    agents: [auto]
    assistants:
      - reviewer
      - profile: qa
        mode: required
`
	f, err := flow.ParseFile(writeTemp(t, yamlDoc))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	got := f.Stages[0].Assistants
	if len(got) != 2 {
		t.Fatalf("Assistants = %+v, want 2 elements", got)
	}
	if !reflect.DeepEqual(got[0], flow.StageAssistant{Profile: "reviewer"}) {
		t.Fatalf("Assistants[0] = %+v, want {Profile: reviewer}", got[0])
	}
	if !reflect.DeepEqual(got[1], flow.StageAssistant{Profile: "qa", Mode: assistants.ModeRequired}) {
		t.Fatalf("Assistants[1] = %+v, want {Profile: qa, Mode: required}", got[1])
	}
}

func TestStageAssistants_SingleObjectWithoutList(t *testing.T) {
	yamlDoc := `
name: t
stages:
  - id: s1
    agents: [auto]
    assistants:
      profile: qa
`
	f, err := flow.ParseFile(writeTemp(t, yamlDoc))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	want := flow.StageAssistants{{Profile: "qa"}}
	if got := f.Stages[0].Assistants; len(got) != 1 || !reflect.DeepEqual(got[0], want[0]) {
		t.Fatalf("Assistants = %+v, want %+v", got, want)
	}
}

func TestStageAssistants_ParseErrors(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"number", "assistants: 42"},
		{"bool", "assistants: true"},
		{"nested list", "assistants: [[reviewer]]"},
		{"unknown key", "assistants: [{triger: on_completion, profile: qa}]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			yamlDoc := `
name: t
stages:
  - id: s1
    agents: [auto]
    ` + tc.body + `
`
			if _, err := flow.ParseFile(writeTemp(t, yamlDoc)); err == nil {
				t.Fatalf("ParseFile: want error for %s, got nil", tc.name)
			}
		})
	}
}

// ---- T2: Flow.validate() rules ----

func TestStageAssistants_ScriptStageRejected(t *testing.T) {
	yamlDoc := `
name: t
stages:
  - id: s1
    script: "echo hi"
    assistants: reviewer
`
	_, err := flow.ParseFile(writeTemp(t, yamlDoc))
	if err == nil {
		t.Fatal("ParseFile: want error, got nil")
	}
	want := `stage "s1": "assistants" cannot be combined with script`
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

func TestStageAssistants_PlanningOnlyStageRejected(t *testing.T) {
	yamlDoc := `
name: t
stages:
  - id: s1
    agents: [planning]
    assistants: reviewer
`
	_, err := flow.ParseFile(writeTemp(t, yamlDoc))
	if err == nil {
		t.Fatal("ParseFile: want error, got nil")
	}
	want := `stage "s1": assistants requires an execution stage (planning-only stage would never attach them)`
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

func TestStageAssistants_EmptyProfileNameFromScalar(t *testing.T) {
	yamlDoc := `
name: t
stages:
  - id: s1
    agents: [auto]
    assistants: [""]
`
	_, err := flow.ParseFile(writeTemp(t, yamlDoc))
	if err == nil {
		t.Fatal("ParseFile: want error, got nil")
	}
	want := `stage "s1": assistants[0]: profile is required`
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

func TestStageAssistants_EmptyProfileNameFromObject(t *testing.T) {
	yamlDoc := `
name: t
stages:
  - id: s1
    agents: [auto]
    assistants:
      - profile: ""
`
	_, err := flow.ParseFile(writeTemp(t, yamlDoc))
	if err == nil {
		t.Fatal("ParseFile: want error, got nil")
	}
	want := `stage "s1": assistants[0]: profile is required`
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

func TestStageAssistants_DuplicateProfileWithinStage(t *testing.T) {
	yamlDoc := `
name: t
stages:
  - id: s1
    agents: [auto]
    assistants: [reviewer, reviewer]
`
	_, err := flow.ParseFile(writeTemp(t, yamlDoc))
	if err == nil {
		t.Fatal("ParseFile: want error, got nil")
	}
	want := `stage "s1": duplicate assistant profile "reviewer"`
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

func TestStageAssistants_InvalidMode(t *testing.T) {
	yamlDoc := `
name: t
stages:
  - id: s1
    agents: [auto]
    assistants:
      - profile: reviewer
        mode: advisry
`
	_, err := flow.ParseFile(writeTemp(t, yamlDoc))
	if err == nil {
		t.Fatal("ParseFile: want error, got nil")
	}
	if !strings.Contains(err.Error(), "mode") {
		t.Fatalf("error = %q, want it to mention %q", err.Error(), "mode")
	}
}

func TestStageAssistants_InvalidTrigger(t *testing.T) {
	yamlDoc := `
name: t
stages:
  - id: s1
    agents: [auto]
    assistants:
      - profile: reviewer
        trigger: on_complete
`
	_, err := flow.ParseFile(writeTemp(t, yamlDoc))
	if err == nil {
		t.Fatal("ParseFile: want error, got nil")
	}
	if want := `trigger must be "one_shot", "checkpoints", "on_completion", or "on_message", got "on_complete"`; !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want it to contain %q", err.Error(), want)
	}
}

func TestStageAssistants_OnMessageTrigger(t *testing.T) {
	yamlDoc := `
name: t
stages:
  - id: s1
    agents: [auto]
    assistants:
      - profile: qa
        trigger: on_message
`
	f, err := flow.ParseFile(writeTemp(t, yamlDoc))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if got := f.Stages[0].Assistants[0].Trigger; got != assistants.TriggerOnMessage {
		t.Fatalf("Trigger = %q, want %q", got, assistants.TriggerOnMessage)
	}
}

func TestStageAssistants_InvalidTriggerErrorListsEveryValue(t *testing.T) {
	yamlDoc := `
name: t
stages:
  - id: s1
    agents: [auto]
    assistants:
      - profile: reviewer
        trigger: on_complete
`
	_, err := flow.ParseFile(writeTemp(t, yamlDoc))
	if err == nil {
		t.Fatal("ParseFile: want error, got nil")
	}
	for _, trigger := range []assistants.Trigger{assistants.TriggerOneShot, assistants.TriggerCheckpoints, assistants.TriggerOnCompletion, assistants.TriggerOnMessage} {
		if !strings.Contains(err.Error(), string(trigger)) {
			t.Fatalf("error = %q, want it to list %q", err.Error(), trigger)
		}
	}
}

func TestStageAssistants_SameProfileAcrossDifferentStagesIsValid(t *testing.T) {
	yamlDoc := `
name: t
stages:
  - id: s1
    agents: [auto]
    assistants: reviewer
  - id: s2
    agents: [auto]
    assistants: reviewer
`
	if _, err := flow.ParseFile(writeTemp(t, yamlDoc)); err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
}

func TestStageAssistants_UnknownKeySuggestsProfile(t *testing.T) {
	yamlDoc := `
name: t
stages:
  - id: s1
    agents: [auto]
    assistants: [{role: qa}]
`
	_, err := flow.ParseFile(writeTemp(t, yamlDoc))
	if err == nil {
		t.Fatal("ParseFile: want error, got nil")
	}
	if !strings.Contains(err.Error(), "profile") {
		t.Fatalf("error = %q, want it to mention %q", err.Error(), "profile")
	}
}

// ---- loop ----

func loopFlowYAML(assistant string) string {
	return "name: t\nstages:\n  - id: s1\n    agents: [auto]\n    assistants:\n" + assistant
}

func loopAssistantYAML(trigger, every, prompt string) string {
	out := "      - profile: qa\n"
	if trigger != "" {
		out += "        trigger: " + trigger + "\n"
	}
	out += "        loop:\n"
	if every != "" {
		out += "          every: " + every + "\n"
	}
	if prompt != "" {
		out += "          prompt: " + prompt + "\n"
	}
	return out
}

func TestStageAssistants_LoopParsed(t *testing.T) {
	object := "    assistants:\n      profile: qa\n      trigger: on_message\n      loop:\n        every: 5m\n        prompt: check the queue\n"
	list := loopFlowYAML(loopAssistantYAML("on_message", "5m", "check the queue") + "      - reviewer\n")
	for name, doc := range map[string]string{
		"object": "name: t\nstages:\n  - id: s1\n    agents: [auto]\n" + object,
		"list":   list,
	} {
		t.Run(name, func(t *testing.T) {
			f, err := flow.ParseFile(writeTemp(t, doc))
			if err != nil {
				t.Fatalf("ParseFile: %v", err)
			}
			got := f.Stages[0].Assistants[0].Loop
			want := &flow.StageAssistantLoop{Every: "5m", Prompt: "check the queue"}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("Loop = %+v, want %+v", got, want)
			}
			if every, err := got.Interval(); err != nil || every != 5*time.Minute {
				t.Fatalf("Interval() = %v, %v; want 5m", every, err)
			}
		})
	}
}

func TestStageAssistants_WithoutLoopHasNilLoop(t *testing.T) {
	doc := "name: t\nstages:\n  - id: s1\n    agents: [auto]\n    assistants:\n      - reviewer\n      - profile: qa\n        trigger: on_message\n"
	f, err := flow.ParseFile(writeTemp(t, doc))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	for i, a := range f.Stages[0].Assistants {
		if a.Loop != nil {
			t.Errorf("assistants[%d].Loop = %+v, want nil", i, a.Loop)
		}
	}
}

func TestStageAssistants_LoopAcceptsFullDurationSyntax(t *testing.T) {
	for _, every := range []string{`"+1m"`, `".5m"`, `"1.s"`, "\"1μs1s\"", "\"1µs1s\"", "1h30m"} {
		t.Run(every, func(t *testing.T) {
			doc := loopFlowYAML(loopAssistantYAML("on_message", every, "ping"))
			if _, err := flow.ParseFile(writeTemp(t, doc)); err != nil {
				t.Fatalf("ParseFile: %v", err)
			}
		})
	}
}

func TestStageAssistants_LoopRejected(t *testing.T) {
	bigPrompt := strings.Repeat("я", 7936) // 15872 bytes
	cases := []struct {
		name    string
		doc     string
		wantErr string
	}{
		{"null", loopFlowYAML("      - profile: qa\n        trigger: on_message\n        loop: null\n"), "loop must be an object"},
		{"scalar", loopFlowYAML("      - profile: qa\n        trigger: on_message\n        loop: 5m\n"), "loop must be an object"},
		{"list", loopFlowYAML("      - profile: qa\n        trigger: on_message\n        loop: [a]\n"), "loop must be an object"},
		{"duplicate field", loopFlowYAML("      - profile: qa\n        trigger: on_message\n        loop:\n          every: 5m\n          every: 6m\n          prompt: p\n"), "duplicate field"},
		{"unknown field", loopFlowYAML("      - profile: qa\n        trigger: on_message\n        loop:\n          evry: 5m\n"), "unknown field"},
		{"numeric every", loopFlowYAML(loopAssistantYAML("on_message", "5", "p")), "every must be a string"},
		{"numeric prompt", loopFlowYAML(loopAssistantYAML("on_message", "5m", "7")), "prompt must be a string"},
		{"empty object", loopFlowYAML("      - profile: qa\n        trigger: on_message\n        loop: {}\n"), "loop.every is required"},
		{"missing every", loopFlowYAML(loopAssistantYAML("on_message", "", "p")), "loop.every is required"},
		{"missing prompt", loopFlowYAML(loopAssistantYAML("on_message", "5m", "")), "loop.prompt is required"},
		{"whitespace prompt", loopFlowYAML(loopAssistantYAML("on_message", "5m", `"   "`)), "loop.prompt is required"},
		{"zero", loopFlowYAML(loopAssistantYAML("on_message", "0s", "p")), "at least"},
		{"negative", loopFlowYAML(loopAssistantYAML("on_message", "-5m", "p")), "at least"},
		{"below minimum", loopFlowYAML(loopAssistantYAML("on_message", "500ms", "p")), "at least"},
		{"days", loopFlowYAML(loopAssistantYAML("on_message", "1d", "p")), "not a valid duration"},
		{"overflow", loopFlowYAML(loopAssistantYAML("on_message", "9999999999h", "p")), "not a valid duration"},
		{"no trigger", loopFlowYAML(loopAssistantYAML("", "5m", "p")), "requires trigger"},
		{"empty trigger", loopFlowYAML(loopAssistantYAML(`""`, "5m", "p")), "requires trigger"},
		{"one_shot", loopFlowYAML(loopAssistantYAML("one_shot", "5m", "p")), "requires trigger"},
		{"checkpoints", loopFlowYAML(loopAssistantYAML("checkpoints", "5m", "p")), "requires trigger"},
		{"on_completion", loopFlowYAML(loopAssistantYAML("on_completion", "5m", "p")), "requires trigger"},
		{"prompt over limit", loopFlowYAML(loopAssistantYAML("on_message", "5m", bigPrompt+"x")), "limit is"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := flow.ParseFile(writeTemp(t, tc.doc))
			if err == nil {
				t.Fatal("ParseFile: want error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %q, want it to contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestStageAssistants_LoopPromptLimitIsBytesAfterTrim(t *testing.T) {
	prompt := "  " + strings.Repeat("я", 7936) + "\n" // 15872 bytes after trim
	doc := loopFlowYAML(loopAssistantYAML("on_message", "5m", "|-\n            "+prompt))
	if _, err := flow.ParseFile(writeTemp(t, doc)); err != nil {
		t.Fatalf("ParseFile at the limit: %v", err)
	}
}

// Every configuration error of a helper with a loop, found by decoding or by validation, names the stage
// and profile and never echoes the prompt — with the profile written before or after the bad field.
func TestStageAssistants_LoopHelperErrorsNameStageAndProfile(t *testing.T) {
	const marker = "SECRET-PROMPT-MARKER"
	loop := "loop: {every: 5m, prompt: " + marker + "}"
	onMessage := "trigger: on_message"
	needsOnMessage := `loop requires trigger "on_message"`
	cases := []struct {
		name    string
		fields  []string
		wantErr string
	}{
		{"loop null", []string{onMessage, "loop: null"}, "loop must be an object"},
		{"loop scalar", []string{onMessage, "loop: " + marker}, "loop must be an object"},
		{"loop list", []string{onMessage, "loop: [" + marker + "]"}, "loop must be an object"},
		{"loop empty object", []string{onMessage, "loop: {}"}, "loop.every is required"},
		{"loop unknown key", []string{onMessage, "loop: {evry: 5m, prompt: " + marker + "}"}, `loop: unknown field "evry"`},
		{"loop duplicate key", []string{onMessage, "loop: {every: 5m, every: 6m, prompt: " + marker + "}"}, `loop: duplicate field "every"`},
		{"loop numeric every", []string{onMessage, "loop: {every: 5, prompt: " + marker + "}"}, "loop.every must be a string"},
		{"loop numeric prompt", []string{onMessage, "loop: {every: 5m, prompt: 7}"}, "loop.prompt must be a string"},
		{"loop map prompt", []string{onMessage, "loop: {every: 5m, prompt: {text: " + marker + "}}"}, "loop.prompt must be a string"},
		{"loop invalid every", []string{onMessage, "loop: {every: 1d, prompt: " + marker + "}"}, "not a valid duration"},
		{"loop every below minimum", []string{onMessage, "loop: {every: 500ms, prompt: " + marker + "}"}, "at least"},
		{"loop missing prompt", []string{onMessage, "loop: {every: 5m}"}, "loop.prompt is required"},
		{"loop prompt over limit", []string{onMessage, "loop: {every: 5m, prompt: " + marker + strings.Repeat("я", 7936) + "}"}, "the limit is"},
		{"duplicate loop key", []string{onMessage, loop, loop}, `duplicate field "loop"`},
		{"trigger invalid", []string{"trigger: on_complete", loop}, "trigger must be"},
		{"trigger numeric", []string{"trigger: 5", loop}, "trigger must be"},
		{"trigger bool", []string{"trigger: true", loop}, "trigger must be"},
		{"trigger alias of prompt", []string{"loop: {every: 5m, prompt: &p " + marker + "}", "trigger: *p"}, "trigger must be"},
		{"mode alias of prompt", []string{onMessage, "loop: {every: 5m, prompt: &p " + marker + "}", "mode: *p"}, "mode must be"},
		{"every alias of prompt", []string{onMessage, "loop: {prompt: &p " + marker + ", every: *p}"}, "not a valid duration"},
		{"trigger sequence", []string{"trigger: [on_message]", loop}, "trigger: "},
		{"trigger map", []string{"trigger: {on: message}", loop}, "trigger: "},
		{"trigger empty", []string{`trigger: ""`, loop}, needsOnMessage},
		{"trigger missing", []string{loop}, needsOnMessage},
		{"trigger one_shot", []string{"trigger: one_shot", loop}, needsOnMessage},
		{"trigger checkpoints", []string{"trigger: checkpoints", loop}, needsOnMessage},
		{"trigger on_completion", []string{"trigger: on_completion", loop}, needsOnMessage},
		{"mode invalid", []string{onMessage, "mode: advisry", loop}, "mode must be"},
		{"mode sequence", []string{onMessage, "mode: [advisory]", loop}, "mode: "},
		{"title map", []string{onMessage, "title: {text: t}", loop}, "title: "},
		{"unknown helper key", []string{"triger: on_message", loop}, `unknown field "triger"`},
		{"duplicate helper key", []string{onMessage, onMessage, loop}, `duplicate field "trigger"`},
		{"skills map", []string{onMessage, "skills: {name: s}", loop}, "skills must be"},
		{"skills_dir empty", []string{onMessage, `skills_dir: ""`, loop}, "skills_dir must not be empty"},
	}
	forms := []struct {
		name, assistants, first, rest, index string
	}{
		{"object", "    assistants:\n", "      ", "      ", "assistants[0]"},
		{"list", "    assistants:\n      - reviewer\n", "      - ", "        ", "assistants[1]"},
	}
	for _, tc := range cases {
		for _, form := range forms {
			for _, profileFirst := range []bool{true, false} {
				fields := append([]string{}, tc.fields...)
				if profileFirst {
					fields = append([]string{"profile: qa-loop"}, fields...)
				} else {
					fields = append(fields, "profile: qa-loop")
				}
				doc := "name: t\nstages:\n  - id: build\n    agents: [auto]\n" + form.assistants
				for i, field := range fields {
					indent := form.rest
					if i == 0 {
						indent = form.first
					}
					doc += indent + field + "\n"
				}
				name := fmt.Sprintf("%s/%s/profile_first=%t", tc.name, form.name, profileFirst)
				t.Run(name, func(t *testing.T) {
					_, err := flow.ParseFile(writeTemp(t, doc))
					if err == nil {
						t.Fatalf("ParseFile: want error, got nil\n%s", doc)
					}
					for _, want := range []string{`stage "build"`, form.index + ` (profile "qa-loop")`, tc.wantErr} {
						if !strings.Contains(err.Error(), want) {
							t.Errorf("error = %q, want it to contain %s", err.Error(), want)
						}
					}
					if strings.Contains(err.Error(), marker) {
						t.Errorf("error %q leaks the prompt", err.Error())
					}
				})
			}
		}
	}
}

func TestStageAssistants_AliasedHelperValuesAccepted(t *testing.T) {
	stages := func(firstAssistants, secondHelper string) string {
		return "name: t\nstages:\n  - id: first\n    agents: [auto]\n    assistants: " + firstAssistants +
			"\n  - id: second\n    agents: [auto]\n    assistants: [reviewer, " + secondHelper + "]\n"
	}
	cases := []struct{ name, doc string }{
		{"helper object as list item", stages("&h {profile: qa, trigger: on_message, loop: {every: 5m, prompt: ping}}", "*h")},
		{"loop object", stages("{profile: qa, trigger: on_message, loop: &lp {every: 5m, prompt: ping}}", "{profile: qa, trigger: on_message, loop: *lp}")},
		{"loop scalar values", stages("{profile: qa, title: &e 5m, instruction: &p ping, trigger: on_message, loop: {every: 5m, prompt: ping}}", "{profile: qa, trigger: on_message, loop: {every: *e, prompt: *p}}")},
	}
	want := flow.StageAssistantLoop{Every: "5m", Prompt: "ping"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := flow.ParseFile(writeTemp(t, tc.doc))
			if err != nil {
				t.Fatalf("ParseFile: %v\n%s", err, tc.doc)
			}
			data, err := yaml.Marshal(f)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			roundTrip, err := flow.ParseFile(writeTemp(t, string(data)))
			if err != nil {
				t.Fatalf("ParseFile after marshal: %v\n%s", err, data)
			}
			for _, parsed := range []*flow.Flow{f, roundTrip} {
				got := parsed.Stages[1].Assistants
				if len(got) != 2 || got[1].Profile != "qa" || got[1].Loop == nil || *got[1].Loop != want {
					t.Fatalf("second stage assistants = %+v, want reviewer and qa with loop %+v", got, want)
				}
			}
		})
	}
}

func TestStageAssistants_AliasedLoopErrorsNameStageAndProfile(t *testing.T) {
	const marker = "SECRET-PROMPT-MARKER"
	first := "name: t\nstages:\n  - id: first\n    agents: [auto]\n    assistants: {profile: qa, trigger: on_message, loop: &lp {every: 5m, prompt: &p " + marker + "}}\n"
	cases := []struct{ name, secondHelper, wantErr string }{
		{"aliased loop object", "{profile: watcher, trigger: one_shot, loop: *lp}", `loop requires trigger "on_message"`},
		{"trigger aliased to another helper's prompt", "{profile: watcher, trigger: *p, loop: {every: 5m, prompt: ping}}", "trigger must be"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := first + "  - id: second\n    agents: [auto]\n    assistants: [reviewer, " + tc.secondHelper + "]\n"
			_, err := flow.ParseFile(writeTemp(t, doc))
			if err == nil {
				t.Fatalf("ParseFile: want error, got nil\n%s", doc)
			}
			want := `stage "second": assistants[1] (profile "watcher"): ` + tc.wantErr
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), want)
			}
			if strings.Contains(err.Error(), marker) {
				t.Errorf("error %q leaks the prompt", err.Error())
			}
		})
	}
}

func TestStageAssistants_SeventeenStagesWithSameLoopProfile(t *testing.T) {
	doc := "name: t\nstages:\n"
	for i := range 17 {
		doc += fmt.Sprintf("  - id: s%d\n    agents: [auto]\n    assistants:\n", i) + loopAssistantYAML("on_message", "5m", "p")
	}
	if _, err := flow.ParseFile(writeTemp(t, doc)); err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
}
