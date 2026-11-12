package agentapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/akopichin/afm/contracts/agentcomm"
	"github.com/akopichin/afm/contracts/assistants"
	"github.com/akopichin/afm/contracts/graph"
	"github.com/akopichin/afm/contracts/state"
)

func OperationErrorStatus(code assistants.ErrorCode) int {
	switch code {
	case assistants.CodeStaleRevision, assistants.CodeInvalidStatus, assistants.CodeFlowPaused, assistants.CodeBudgetExceeded, assistants.CodeSpendLimitReached, agentcomm.CodeWaitCycle:
		return http.StatusConflict
	case assistants.CodeFeatureDisabled, assistants.CodeNotFound:
		return http.StatusNotFound
	case assistants.CodeRunFinalizing:
		return http.StatusServiceUnavailable
	case assistants.CodeCapabilityUnavailable, assistants.CodeDelegationDepthExceeded, assistants.CodeInvalidParent, assistants.CodeAncestorInactive, assistants.CodeExecutorNotPermitted, assistants.CodeRatesMissing:
		return http.StatusUnprocessableEntity
	case assistants.CodeActorNotAllowed:
		return http.StatusForbidden
	case assistants.CodeInvalidRequest:
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func GraphErrorStatus(code string) int {
	switch code {
	case string(assistants.CodeStaleRevision), string(assistants.CodeInvalidStatus):
		return http.StatusConflict
	case string(assistants.CodeRunFinalizing):
		return http.StatusServiceUnavailable
	default:
		return http.StatusBadRequest
	}
}

// WriteKnownError keeps admission rejections consistent across embedding products.
func WriteKnownError(w http.ResponseWriter, err error) bool {
	var rejected *state.OperationRejectedError
	if errors.As(err, &rejected) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(OperationErrorStatus(rejected.Code))
		body := map[string]string{"error": string(rejected.Code)}
		if rejected.Reason != "" {
			body["message"] = rejected.Reason
		}
		_ = json.NewEncoder(w).Encode(body)
		return true
	}
	var invalid *graph.ValidationError
	if errors.As(err, &invalid) {
		WriteErrorCode(w, GraphErrorStatus(invalid.Code), invalid.Code)
		return true
	}
	return false
}
