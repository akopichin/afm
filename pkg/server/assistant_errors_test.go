package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/state"
)

// A typed rejection reaches the client as a 4xx with its reason, so an agent (or the dashboard) can tell
// what to fix instead of seeing a bare "500 internal".
func TestWriteAssistantError_RejectionCarriesStatusAndReason(t *testing.T) {
	cases := []struct {
		code assistants.ErrorCode
		want int
	}{
		{assistants.CodeNotFound, http.StatusNotFound},
		{assistants.CodeInvalidRequest, http.StatusBadRequest},
		{agentcomm.CodeWaitCycle, http.StatusConflict},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		writeAssistantError(w, &state.OperationRejectedError{Code: tc.code, Reason: "unknown thread mt-x"})
		if w.Code != tc.want {
			t.Fatalf("%s: HTTP %d, want %d", tc.code, w.Code, tc.want)
		}
		var body map[string]string
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if body["error"] != string(tc.code) || body["message"] != "unknown thread mt-x" {
			t.Fatalf("%s: body = %v, want error code + message", tc.code, body)
		}
	}
}

// A rejection wrapped by the storage layer ("reject control record: %w") is still recognized.
func TestWriteAssistantError_WrappedRejection(t *testing.T) {
	w := httptest.NewRecorder()
	writeAssistantError(w, errors.Join(errors.New("reject control record"), &state.OperationRejectedError{Code: assistants.CodeNotFound, Reason: "x"}))
	if w.Code != http.StatusNotFound {
		t.Fatalf("HTTP %d, want 404", w.Code)
	}
}

// Anything unexpected is still a 500, but with a message pointing at the log (where the error is written).
func TestWriteAssistantError_UnexpectedIsInternalWithMessage(t *testing.T) {
	w := httptest.NewRecorder()
	writeAssistantError(w, errors.New("disk on fire"))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("HTTP %d, want 500", w.Code)
	}
	var body map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["error"] != "internal" || body["message"] == "" {
		t.Fatalf("body = %v, want internal + a message", body)
	}
}
