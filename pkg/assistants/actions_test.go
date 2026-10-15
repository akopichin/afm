package assistants_test

import (
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
)

func TestKnownActionType(t *testing.T) {
	if !assistants.KnownActionType(string(assistants.ActionAttachAssistant)) {
		t.Fatal("attach_assistant must be a known action type")
	}
	if assistants.KnownActionType("delete_everything") {
		t.Fatal("an unrecognized action type must not be reported as known")
	}
	if assistants.KnownActionType("") {
		t.Fatal("empty type must not be reported as known")
	}
}
