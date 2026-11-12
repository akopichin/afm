package agentapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/akopichin/afm/contracts/assistants"
	"github.com/akopichin/afm/contracts/state"
)

func TestDefaultHostPreservesAdmissionStatus(t *testing.T) {
	for code, status := range map[assistants.ErrorCode]int{
		assistants.CodeCapabilityUnavailable: http.StatusUnprocessableEntity,
		assistants.CodeFlowPaused:            http.StatusConflict,
		assistants.CodeActorNotAllowed:       http.StatusForbidden,
		assistants.CodeNotFound:              http.StatusNotFound,
	} {
		response := httptest.NewRecorder()
		New(nil, nil).writeAssistantError(response, &state.OperationRejectedError{Code: code})
		if response.Code != status {
			t.Errorf("%s: got %d, want %d", code, response.Code, status)
		}
	}
}

func TestOperationErrorStatusSpendCodes(t *testing.T) {
	for code, status := range map[assistants.ErrorCode]int{
		assistants.CodeSpendLimitReached: http.StatusConflict,
		assistants.CodeRatesMissing:      http.StatusUnprocessableEntity,
	} {
		if got := OperationErrorStatus(code); got != status {
			t.Errorf("%s: got %d, want %d", code, got, status)
		}
	}
}
