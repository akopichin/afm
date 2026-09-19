package prompts

import (
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/flow"
)

// TestBuildVerify_RequirementsAsContextNotCommand воспроизводит §8 плана
// AI-verify: глобальный/построчный prompt реализации ("реализуй и закоммить")
// должен попасть проверяющему как ТРЕБОВАНИЯ (справочный контекст), а не как
// команда — иначе verifier превращается во второго автора.
func TestBuildVerify_RequirementsAsContextNotCommand(t *testing.T) {
	in := VerifyInputs{
		Template:     "MACHINE FORMAT RULES: return only schema_version/verdict JSON.",
		Stage:        flow.Stage{ID: "s1", Name: "Stage One", Description: "add the widget"},
		GlobalPrompt: "Always implement the feature end-to-end and commit the result.",
		StagePrompt:  "Implement and commit the change.",
	}
	out := BuildVerify(in)

	// Требования эхом присутствуют как данные (проверяющему нужно знать, что
	// требовалось), но обёрнуты в тег требований-как-контекста.
	if !strings.Contains(out, "Always implement the feature end-to-end and commit the result.") {
		t.Errorf("global prompt text missing from context: %s", out)
	}
	if !strings.Contains(out, "Implement and commit the change.") {
		t.Errorf("stage prompt text missing from context: %s", out)
	}
	if !strings.Contains(out, "<requirements_context>") || !strings.Contains(out, "</requirements_context>") {
		t.Errorf("requirements must be wrapped in <requirements_context>: %s", out)
	}
	if !strings.Contains(out, "not commands for you to execute") {
		t.Errorf("missing explicit context-not-command framing: %s", out)
	}

	// Ничего из builder-контента (skills, image_output, interactive_rules,
	// project_memory) не должно просачиваться в verify-контекст.
	forbidden := []string{"<interactive_rules>", "<image_output>", "<skills>", "<project_memory>"}
	for _, marker := range forbidden {
		if strings.Contains(out, marker) {
			t.Errorf("verify context leaked builder content %q: %s", marker, out)
		}
	}

	// Машинный формат приходит из шаблона.
	if !strings.Contains(out, "MACHINE FORMAT RULES") {
		t.Errorf("verify template (machine format instructions) missing: %s", out)
	}
}

// TestBuildVerify_IncludesPreviousReportWhenPresent проверяет, что предыдущий
// verification-report попадает в контекст (для проверки закрытия прежних
// blockers), а при его отсутствии секция не появляется вовсе.
func TestBuildVerify_IncludesPreviousReportWhenPresent(t *testing.T) {
	base := VerifyInputs{
		Template: "MACHINE FORMAT",
		Stage:    flow.Stage{ID: "s1", Name: "Stage One"},
	}

	withoutReport := BuildVerify(base)
	if strings.Contains(withoutReport, "<previous_verification_report>") {
		t.Errorf("previous report section should not appear when empty: %s", withoutReport)
	}

	withReport := base
	withReport.PreviousReport = "Verdict: needs_changes\nBlocking: the widget ignores nil input."
	out := BuildVerify(withReport)
	if !strings.Contains(out, "<previous_verification_report>") {
		t.Errorf("previous report section missing: %s", out)
	}
	if !strings.Contains(out, "the widget ignores nil input") {
		t.Errorf("previous report content missing: %s", out)
	}
}

// TestBuildVerify_IncludesTaskDataAndPassedSteps проверяет, что plan,
// artifacts, author summary, verify.prompt и результаты уже прошедших
// shell-шагов действительно попадают в собранный контекст (§8 плана).
func TestBuildVerify_IncludesTaskDataAndPassedSteps(t *testing.T) {
	in := VerifyInputs{ //nolint:gosec // test fixture text, not a real secret
		Template:      "MACHINE FORMAT",
		Stage:         flow.Stage{ID: "s1", Name: "Stage One"},
		Plan:          "## Plan\n- do X",
		Artifacts:     "docs/arch/widget.md",
		AuthorSummary: "I implemented the widget.",
		PassedSteps:   "step 1 (shell: go test ./...): passed",
		VerifyPrompt:  "Also check that errors are wrapped with %w.",
	}
	out := BuildVerify(in)

	for _, want := range []string{
		"## Plan\n- do X",
		"docs/arch/widget.md",
		"I implemented the widget.",
		"step 1 (shell: go test ./...): passed",
		"Also check that errors are wrapped with %w.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing expected content %q in: %s", want, out)
		}
	}

	// Резюме автора явно помечено как непроверенное.
	if !strings.Contains(out, "UNVERIFIED") {
		t.Errorf("author summary must be marked unverified: %s", out)
	}
}
