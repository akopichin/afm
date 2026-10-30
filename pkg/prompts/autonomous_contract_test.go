package prompts_test

import (
	"strings"
	"testing"

	"github.com/akopichin/afm/assets"
)

// Контракт summary — непустой текст: prompt не должен обещать retry из-за отсутствия разделов.
func TestAutonomousPrompt_SummaryContractIsNonEmptyText(t *testing.T) {
	text, _, err := assets.ReadPrompt("autonomous.md", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "Missing any section causes a retry") {
		t.Error("autonomous prompt still promises a retry for missing sections")
	}
	if !strings.Contains(text, "MUST be a non-empty text file") {
		t.Error("autonomous prompt must state the real contract: a non-empty execution_summary.md")
	}
	if strings.Contains(text, "MUST contain these top-level sections") {
		t.Error("section names are recommended, not enforced")
	}
}
