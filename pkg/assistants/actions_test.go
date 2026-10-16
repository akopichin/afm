package assistants_test

import (
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
)

func TestKnownActionType(t *testing.T) {
	if !assistants.KnownActionType(string(assistants.ActionAttachAssistant)) {
		t.Fatal("attach_assistant must be a known action type")
	}
	// Этап 10 (T10.1): add_stage joins attach_assistant in the common action-draft protocol.
	if !assistants.KnownActionType(string(assistants.ActionAddStage)) {
		t.Fatal("add_stage must be a known action type")
	}
	if assistants.KnownActionType("delete_everything") {
		t.Fatal("an unrecognized action type must not be reported as known")
	}
	if assistants.KnownActionType("") {
		t.Fatal("empty type must not be reported as known")
	}
}
