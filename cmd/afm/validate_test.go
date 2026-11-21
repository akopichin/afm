package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm-engine/engine"
	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/contracts/flow"
)

func writeTestFlow(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "flow.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write flow.yaml: %v", err)
	}
	return path
}

func TestValidateFlowFile_ValidFlowReturnsOK(t *testing.T) {
	path := writeTestFlow(t, `name: test-flow
description: "a test flow"
stages:
  - id: implementation
    name: "Implementation"
    description: "do the thing"
    agents: [planning, implementation, review]
`)
	msg, err := validateFlowFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(msg, "test-flow") || !strings.Contains(msg, "1 stage") {
		t.Errorf("unexpected message: %q", msg)
	}
}

func TestValidateFlowFile_CycleReturnsError(t *testing.T) {
	path := writeTestFlow(t, `name: cyclic-flow
stages:
  - id: a
    description: "a"
    agents: [planning, implementation]
    depends_on: [b]
  - id: b
    description: "b"
    agents: [planning, implementation]
    depends_on: [a]
`)
	_, err := validateFlowFile(path)
	if err == nil {
		t.Fatal("expected error for cyclic depends_on")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("expected cycle error, got: %v", err)
	}
}

func TestValidateFlowFile_UnknownDependsOnReturnsError(t *testing.T) {
	path := writeTestFlow(t, `name: bad-flow
stages:
  - id: a
    description: "a"
    agents: [planning, implementation]
    depends_on: [nonexistent]
`)
	_, err := validateFlowFile(path)
	if err == nil {
		t.Fatal("expected error for unknown depends_on")
	}
	if !strings.Contains(err.Error(), "unknown stage") {
		t.Errorf("expected unknown-stage error, got: %v", err)
	}
}

// writeValidateConfig points afm validate at an empty HOME and a project .afm/config.yaml with body.
func writeValidateConfig(t *testing.T, body string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AFM_COLLABORATION", "")
	root := t.TempDir()
	prev := rootDir
	rootDir = root
	t.Cleanup(func() { rootDir = prev })
	if err := os.MkdirAll(filepath.Join(root, ".afm"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".afm", "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const validateTeamFlow = `name: team-flow
team:
  - id: arch
    profile: rev-arch
    title: Architecture
    instruction: Review when asked.
stages:
  - id: s
    agents: [auto]
`

func TestValidateFlowFile_TeamWithKnownProfile(t *testing.T) {
	writeValidateConfig(t, "collaboration:\n  profiles:\n    rev-arch:\n      command: codex-as-claude\n")
	if _, err := validateFlowFile(writeTestFlow(t, validateTeamFlow)); err != nil {
		t.Fatalf("validateFlowFile: %v", err)
	}
}

func TestValidateFlowFile_TeamUnknownProfile(t *testing.T) {
	writeValidateConfig(t, "collaboration:\n  profiles:\n    qa:\n      command: codex-as-claude\n")
	_, err := validateFlowFile(writeTestFlow(t, validateTeamFlow))
	if err == nil || !strings.Contains(err.Error(), `team member "arch"`) || !strings.Contains(err.Error(), "rev-arch") {
		t.Fatalf("validateFlowFile = %v, want an unknown-profile error for arch", err)
	}
}

func TestValidateFlowFile_TeamDuplicateID(t *testing.T) {
	writeValidateConfig(t, "")
	path := writeTestFlow(t, `name: team-flow
team:
  - id: qa
    profile: qa
    title: QA
  - id: qa
    profile: qa
    title: QA two
stages:
  - id: s
    agents: [auto]
`)
	_, err := validateFlowFile(path)
	if err == nil || !strings.Contains(err.Error(), `duplicate team member id "qa"`) {
		t.Fatalf("validateFlowFile = %v, want a duplicate id error", err)
	}
}

func TestValidateFlowFile_TeamWithDisabledCollaborationWarns(t *testing.T) {
	writeValidateConfig(t, "collaboration:\n  enabled: false\n")
	var err error
	stderr := captureStderr(t, func() { _, err = validateFlowFile(writeTestFlow(t, validateTeamFlow)) })
	if err != nil {
		t.Fatalf("validateFlowFile: %v, want OK with a warning", err)
	}
	if !strings.Contains(stderr, "collaboration is disabled") {
		t.Fatalf("stderr = %q, want the disabled-collaboration warning", stderr)
	}
}

// validate builds the same profile registry as afm run: a malformed profile that would turn collaboration
// off at run time fails validation even when the team's own profile is fine.
func TestValidateFlowFile_TeamWithMalformedProfileRegistry(t *testing.T) {
	writeValidateConfig(t, "collaboration:\n  profiles:\n    rev-arch:\n      command: codex-as-claude\n    \"bad/id\":\n      command: codex-as-claude\n")
	_, err := validateFlowFile(writeTestFlow(t, validateTeamFlow))
	if err == nil || !strings.Contains(err.Error(), "bad/id") {
		t.Fatalf("validateFlowFile = %v, want the registry error for bad/id", err)
	}
}

// The builtin default needs no config, like in afm run: a team member and a stage assistant may name it.
func TestValidateFlowFile_BuiltinDefaultProfile(t *testing.T) {
	t.Setenv("AFM_COLLABORATION", "1") // with collaboration off validate only warns and would prove nothing
	writeValidateConfig(t, "")
	path := writeTestFlow(t, `name: default-flow
team:
  - id: helper
    profile: default
    title: Helper
stages:
  - id: s
    agents: [auto]
    assistants: default
`)
	if _, err := validateFlowFile(path); err != nil {
		t.Fatalf("validateFlowFile: %v", err)
	}
}

const validateStageAssistantFlow = `name: stage-flow
stages:
  - id: s
    agents: [auto]
    assistants: ghost
`

// A stage's assistants are checked as afm run's preflight checks them.
func TestValidateFlowFile_StageAssistantUnknownProfile(t *testing.T) {
	writeValidateConfig(t, "")
	_, err := validateFlowFile(writeTestFlow(t, validateStageAssistantFlow))
	if err == nil || !strings.Contains(err.Error(), `stage "s"`) || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("validateFlowFile = %v, want an unknown-profile error for stage s", err)
	}
}

func TestValidateFlowFile_StageAssistantWithDisabledCollaborationWarns(t *testing.T) {
	writeValidateConfig(t, "collaboration:\n  enabled: false\n")
	var err error
	stderr := captureStderr(t, func() { _, err = validateFlowFile(writeTestFlow(t, validateStageAssistantFlow)) })
	if err != nil {
		t.Fatalf("validateFlowFile: %v, want OK with a warning", err)
	}
	if !strings.Contains(stderr, `stage "s"`) || !strings.Contains(stderr, "collaboration is disabled") {
		t.Fatalf("stderr = %q, want the disabled-collaboration warning for stage s", stderr)
	}
}

const validateLoopMarker = "PROMPT-MARKER-7f3a"

// loopFlow declares one helper loop on stage build; trigger is the helper's trigger line, empty for none.
func loopFlow(trigger string) string {
	return `name: loop-flow
stages:
  - id: build
    agents: [auto]
    assistants:
      - profile: watcher
` + trigger + `        loop:
          every: 1s
          prompt: ` + validateLoopMarker + `
`
}

func TestValidateFlowFile_StageAssistantLoopReturnsOK(t *testing.T) {
	writeValidateConfig(t, "collaboration:\n  profiles:\n    watcher:\n      command: codex-as-claude\n")
	if _, err := validateFlowFile(writeTestFlow(t, loopFlow("        trigger: on_message\n"))); err != nil {
		t.Fatalf("validateFlowFile: %v", err)
	}
}

// The message names the stage, the profile and the missing trigger, never the prompt.
func TestValidateFlowFile_StageAssistantLoopWithoutTriggerNamesTheFix(t *testing.T) {
	writeValidateConfig(t, "collaboration:\n  profiles:\n    watcher:\n      command: codex-as-claude\n")
	_, err := validateFlowFile(writeTestFlow(t, loopFlow("")))
	if err == nil {
		t.Fatal("validateFlowFile accepted a loop without a trigger")
	}
	for _, want := range []string{`stage "build"`, "watcher", "on_message"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
	if strings.Contains(err.Error(), validateLoopMarker) {
		t.Errorf("error %q leaks the prompt", err)
	}
}

// budgetParityRecipes declares the agent command "bcmd" (model budget-m) and "vcmd" (model verify-m) so that
// the rate check has a model to look at.
const budgetParityRecipes = `docker:
  agents:
    bcmd:
      type: claude
      model: budget-m
      auth: {from: "env:AFM_TEST_TOKEN", to: "env:CLAUDE_CODE_OAUTH_TOKEN"}
    vcmd:
      type: claude
      model: verify-m
      auth: {from: "env:AFM_TEST_TOKEN", to: "env:CLAUDE_CODE_OAUTH_TOKEN"}
`

const budgetParityRates = `pricing:
  models:
    budget-m: {input: 1, output: 2, cache_read: 0.1, cache_write: 1.5}
    verify-m: {input: 1, output: 2, cache_read: 0.1, cache_write: 1.5}
`

// openBudgetRun opens (without starting) the run the way afm run does and returns Open's error.
func openBudgetRun(t *testing.T, flowPath string) error {
	t.Helper()
	f, err := flow.ParseFile(flowPath)
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	cfg, err := loadProductConfig(filepath.Join(home, config.AfmDir), fmDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := engine.Open(context.Background(), engine.Spec{Flow: f, Config: cfg.Execution()}, engine.Options{
		Paths:  enginePaths(""),
		Stdout: io.Discard, Stderr: io.Discard,
	})
	if err == nil {
		_ = run.Close()
	}
	return err
}

// The validate preflight and engine Open must refuse exactly the same flows: a stricter validate would reject
// flows that run, a looser one would pass flows that fail at start.
func TestValidateFlowFile_MoneyRatesMatchEngineOpen(t *testing.T) {
	const money = "  money: 5\n  tokens: 100k\n"
	cases := []struct {
		name    string
		config  string
		flow    string
		refused bool
	}{
		{"no budget at all", budgetParityRecipes, "name: f\nstages:\n  - id: a\n    agents: [auto]\n    command: bcmd\n", false},
		{"tokens only, no rates", budgetParityRecipes, "name: f\nbudget:\n  tokens: 100k\nstages:\n  - id: a\n    agents: [auto]\n    command: bcmd\n", false},
		{"run money, no rates", budgetParityRecipes, "name: f\nbudget:\n" + money + "stages:\n  - id: a\n    agents: [auto]\n    command: bcmd\n", true},
		{"run money, explicit rates", budgetParityRecipes + budgetParityRates, "name: f\nbudget:\n" + money + "stages:\n  - id: a\n    agents: [auto]\n    command: bcmd\n", false},
		{"run money, a rate missing", budgetParityRecipes + "pricing:\n  models:\n    budget-m: {input: 1, output: 2, cache_read: 0.1}\n", "name: f\nbudget:\n" + money + "stages:\n  - id: a\n    agents: [auto]\n    command: bcmd\n", true},
		{"stage money, no rates", budgetParityRecipes, "name: f\nstages:\n  - id: a\n    agents: [auto]\n    command: bcmd\n    budget:\n" + strings.ReplaceAll(money, "  ", "      ") + "", true},
		{"money only on an uncovered script stage", budgetParityRecipes, "name: f\nstages:\n  - id: a\n    script: echo ok\n    budget:\n" + strings.ReplaceAll(money, "  ", "      ") + "  - id: b\n    agents: [auto]\n    command: bcmd\n", false},
		{"script stage under a run money budget", budgetParityRecipes, "name: f\nbudget:\n" + money + "stages:\n  - id: a\n    script: echo ok\n", false},
		{"command without a recipe", "", "name: f\nbudget:\n" + money + "stages:\n  - id: a\n    agents: [auto]\n    command: unknown-cmd\n", false},
		{"default client command", "", "name: f\nbudget:\n" + money + "stages:\n  - id: a\n    agents: [auto]\n", false},
		{"configured client command, no rates", budgetParityRecipes + "client:\n  command: bcmd\n", "name: f\nbudget:\n" + money + "stages:\n  - id: a\n    agents: [auto]\n", true},
		{"configured client command, explicit rates", budgetParityRecipes + budgetParityRates + "client:\n  command: bcmd\n", "name: f\nbudget:\n" + money + "stages:\n  - id: a\n    agents: [auto]\n", false},
		{"verify agent step, no rates", budgetParityRecipes + "pricing:\n  models:\n    budget-m: {input: 1, output: 2, cache_read: 0.1, cache_write: 1.5}\n",
			"name: f\nbudget:\n" + money + "stages:\n  - id: a\n    agents: [auto]\n    command: bcmd\n    verify:\n      command: vcmd\n", true},
		{"verify agent step, explicit rates", budgetParityRecipes + budgetParityRates,
			"name: f\nbudget:\n" + money + "stages:\n  - id: a\n    agents: [auto]\n    command: bcmd\n    verify:\n      command: vcmd\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeValidateConfig(t, tc.config)
			t.Setenv("AFM_SIDEAGENT", "0")
			t.Setenv("AFM_TEST_TOKEN", "x")
			path := writeTestFlow(t, tc.flow)

			_, validateErr := validateFlowFile(path)
			openErr := openBudgetRun(t, path)

			var engineErr *engine.Error
			openRefused := errors.As(openErr, &engineErr) && engineErr.Code == engine.CodeBudgetRatesMissing
			if openErr != nil && !openRefused {
				t.Fatalf("engine.Open failed for another reason: %v", openErr)
			}
			if openRefused != tc.refused {
				t.Errorf("engine.Open refused = %t, want %t (err %v)", openRefused, tc.refused, openErr)
			}
			if validateRefused := validateErr != nil; validateRefused != openRefused {
				t.Errorf("validate refused = %t but engine.Open refused = %t\nvalidate: %v\nopen: %v", validateRefused, openRefused, validateErr, openErr)
			}
			if tc.refused && validateErr != nil && !strings.Contains(validateErr.Error(), "needs explicit rates") {
				t.Errorf("validate error = %v, want the missing-rates explanation", validateErr)
			}
		})
	}
}

func TestValidateFlowFile_MissingRatesNameScopeCommandModelAndCategories(t *testing.T) {
	writeValidateConfig(t, budgetParityRecipes+"pricing:\n  models:\n    budget-m: {input: 1, output: 2}\n")
	path := writeTestFlow(t, "name: f\nstages:\n  - id: a\n    agents: [auto]\n    command: bcmd\n    budget:\n      money: 3\n")
	_, err := validateFlowFile(path)
	if err == nil {
		t.Fatal("validateFlowFile passed a money budget without explicit rates")
	}
	for _, want := range []string{"stage:a", `"bcmd"`, `"budget-m"`, "cache_read", "cache_write"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

// The trigger is named for every assistants: form contracts accept; any other error stays exactly the contracts one.
func TestValidateFlowFile_PreCompletionHelperOnPlanningOnlyStageNamesTrigger(t *testing.T) {
	planOnly := func(assistants string) string {
		return "  - id: plan-only\n    agents: [planning]\n    assistants: " + assistants + "\n"
	}
	build := func(assistants string) string {
		return "  - id: build\n    agents: [auto]\n    assistants: " + assistants + "\n"
	}
	const helper = "{profile: qa, trigger: pre_completion}"
	cases := []struct {
		name         string
		stages       string
		namesTrigger bool
	}{
		{"list of objects", planOnly("[{profile: qa, trigger: pre_completion, mode: required}]"), true},
		{"single object", planOnly(helper), true},
		{"list mixing a profile name and an object", planOnly("[other, " + helper + "]"), true},
		{"profile name on a stage before", build("other") + planOnly("["+helper+"]"), true},
		{"profile name on a stage after", planOnly("["+helper+"]") + build("other"), true},
		{"aliased object", build("&helper "+helper) + planOnly("*helper"), true},
		{"aliased list", build("&helpers ["+helper+"]") + planOnly("*helpers"), true},
		{"aliased object in a mixed list", build("&helper "+helper) + planOnly("[other, *helper]"), true},
		{"aliased profile name in a mixed list", build("&name other") + planOnly("[*name, "+helper+"]"), true},
		{"aliased trigger", planOnly("[{profile: qa, title: &trigger pre_completion, trigger: *trigger}]"), true},
		// A bare name is a profile, not a trigger, even a profile called pre_completion.
		{"profile name", planOnly("qa"), false},
		{"profile named pre_completion", planOnly("pre_completion"), false},
		{"list of profile names", planOnly("[qa, pre_completion]"), false},
		// Only the rejected stage counts: a pre_completion helper on a valid stage is not the cause.
		{"one_shot trigger", planOnly("[{profile: qa, trigger: one_shot}]") + build("["+helper+"]"), false},
		{"on_message trigger", planOnly("[{profile: qa, trigger: on_message}]") + build("["+helper+"]"), false},
		{"on_completion trigger", planOnly("[{profile: qa, trigger: on_completion}]") + build("["+helper+"]"), false},
		{"checkpoints trigger", planOnly("[{profile: qa, trigger: checkpoints}]") + build("["+helper+"]"), false},
		{"earlier stage fails first", planOnly("other") + "  - id: second\n    agents: [planning]\n    assistants: [" + helper + "]\n", false},
		{"script stage", "  - id: plan-only\n    script: echo ok\n    assistants: [" + helper + "]\n", false},
		{"stage pre_completion block", planOnly("["+helper+"]") + "    pre_completion: {max_failures: 1}\n", false},
		{"unknown trigger", planOnly("[{profile: qa, trigger: other}]"), false},
		{"malformed YAML", "  - id: plan-only\n    agents: [planning\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTestFlow(t, "name: test-flow\nstages:\n"+tc.stages)
			_, original := flow.ParseFile(path)
			if original == nil {
				t.Fatal("flow.ParseFile accepted the flow")
			}
			named := nameMisplacedPreCompletion(path, original)
			if tc.namesTrigger {
				if named == original || !errors.Is(named, original) {
					t.Errorf("error %q must wrap the original %q", named, original)
				}
				for _, want := range []string{`stage "plan-only"`, "pre_completion"} {
					if !strings.Contains(named.Error(), want) {
						t.Errorf("error %q must mention %s", named, want)
					}
				}
			} else if named != original {
				t.Errorf("error %q, want the original %q", named, original)
			}
			if _, err := validateFlowFile(path); err == nil || err.Error() != "invalid flow.yaml: "+named.Error() {
				t.Errorf("validateFlowFile = %v, want invalid flow.yaml: %v", err, named)
			}
		})
	}
}

// When the file is gone, no longer YAML or has no rejected stage, the contracts error is the best there is.
func TestValidateFlowFile_PreCompletionNamingKeepsErrorItCannotExplain(t *testing.T) {
	original := errors.New(`stage "plan-only": ` + planningOnlyAssistantsError)
	paths := map[string]string{
		"unreadable file":       filepath.Join(t.TempDir(), "absent.yaml"),
		"malformed YAML":        writeTestFlow(t, "stages: [\n"),
		"stage not in the file": writeTestFlow(t, "name: test-flow\nstages:\n  - id: another\n    agents: [planning]\n    assistants: [{profile: qa, trigger: pre_completion}]\n"),
	}
	for name, path := range paths {
		t.Run(name, func(t *testing.T) {
			if got := nameMisplacedPreCompletion(path, original); got != original {
				t.Errorf("error %q, want the original %q", got, original)
			}
		})
	}
}

func TestValidateFlowFile_PreCompletionHelperOnExecutionStageReturnsOK(t *testing.T) {
	writeValidateConfig(t, "")
	path := writeTestFlow(t, "name: test-flow\nstages:\n  - id: build\n    agents: [auto]\n    assistants: {profile: default, trigger: pre_completion}\n")
	if _, err := validateFlowFile(path); err != nil {
		t.Fatalf("validateFlowFile: %v", err)
	}
}

const stageContextConfig = `collaboration:
  profiles:
    picky:
      command: codex-as-claude
      stage_context: {inputs: [prep.rules]}
    bare:
      command: codex-as-claude
      stage_context: {inputs: []}
    bad:
      command: codex-as-claude
      stage_context: {inputs: [prep.missing]}
`

// stageContextFlow runs prep → work; work declares helperProfile and has a team member backed by memberProfile.
func stageContextFlow(helperProfile, memberProfile string) string {
	return `name: ctx-flow
team:
  - id: member
    profile: ` + memberProfile + `
    title: Member
stages:
  - id: prep
    agents: [auto]
    artifacts:
      - name: rules
        path: rules.md
      - name: big
        path: big.md
  - id: work
    agents: [auto]
    depends_on: [prep]
    inputs: [prep.rules, prep.big]
    assistants: ` + helperProfile + `
`
}

func TestValidateFlowFile_StageContextValidBlocksReturnOK(t *testing.T) {
	writeValidateConfig(t, stageContextConfig)
	if _, err := validateFlowFile(writeTestFlow(t, stageContextFlow("picky", "bare"))); err != nil {
		t.Fatalf("validateFlowFile: %v", err)
	}
}

func TestValidateFlowFile_StageContextUnknownReferenceOfDeclaredHelper(t *testing.T) {
	writeValidateConfig(t, stageContextConfig)
	_, err := validateFlowFile(writeTestFlow(t, stageContextFlow("bad", "bare")))
	for _, want := range []string{`stage "work"`, "bad", "prep.missing"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("validateFlowFile = %v, want an error containing %q", err, want)
		}
	}
}

func TestValidateFlowFile_StageContextTeamMemberSelectsNothing(t *testing.T) {
	writeValidateConfig(t, stageContextConfig)
	_, err := validateFlowFile(writeTestFlow(t, stageContextFlow("picky", "picky")))
	for _, want := range []string{"team member", "picky", "prep.rules"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("validateFlowFile = %v, want an error containing %q", err, want)
		}
	}
}

func TestValidateFlowFile_StageContextMalformedBlockStopsConfigLoading(t *testing.T) {
	cases := map[string]string{
		"null":          "stage_context: null",
		"no dot in ref": "stage_context: {inputs: [rules]}",
	}
	for name, block := range cases {
		t.Run(name, func(t *testing.T) {
			writeValidateConfig(t, "collaboration:\n  profiles:\n    p:\n      command: codex-as-claude\n      "+block+"\n")
			_, err := validateFlowFile(writeTestFlow(t, stageContextFlow("p", "p")))
			if err == nil || !strings.Contains(err.Error(), "stage_context") {
				t.Fatalf("validateFlowFile = %v, want a config error naming stage_context", err)
			}
		})
	}
}
