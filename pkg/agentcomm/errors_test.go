package agentcomm

import (
	"testing"

	"github.com/akopichin/afm/pkg/assistants"
)

// acceptsAssistantsErrorCode only compiles if agentcomm.ErrorCode and assistants.ErrorCode are
// the same type — proving the alias in errors.go is real, not a lookalike with a matching method
// set.
func acceptsAssistantsErrorCode(c assistants.ErrorCode) assistants.ErrorCode { return c }

// TestErrorCode_IsAssistantsErrorCode proves agentcomm.ErrorCode is the SAME type as
// assistants.ErrorCode (a type alias, not a lookalike), so a single HTTP-status switch can accept
// values from either package without a conversion.
func TestErrorCode_IsAssistantsErrorCode(t *testing.T) {
	got := acceptsAssistantsErrorCode(CodeThreadClosed)
	if string(got) != "thread_closed" {
		t.Fatalf("CodeThreadClosed = %q, want %q", got, "thread_closed")
	}
}

func TestErrorCode_NewCodesAreDistinctAndStable(t *testing.T) {
	codes := map[ErrorCode]string{
		CodeRecipientUnreachable: "recipient_unreachable",
		CodeThreadClosed:         "thread_closed",
		CodeWaitCycle:            "wait_cycle",
		CodeMessagingUnsupported: "messaging_unsupported",
	}
	seen := make(map[string]bool, len(codes))
	for code, want := range codes {
		if string(code) != want {
			t.Errorf("code %v = %q, want %q", code, string(code), want)
		}
		if seen[string(code)] {
			t.Errorf("duplicate error code string %q", code)
		}
		seen[string(code)] = true
	}
}
