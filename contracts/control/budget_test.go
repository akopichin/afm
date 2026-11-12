package control

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/akopichin/afm/contracts/budget"
	"github.com/akopichin/afm/contracts/internal/budgetparity"
)

func TestCreationRequestsCarryBudget(t *testing.T) {
	spec := &budget.Spec{Tokens: 1_000_000, MoneyUSD: 5, StopPolicy: budget.PolicyMedium}
	requests := map[string]any{
		"attach":          AttachRequest{Budget: spec},
		"attach_and_send": AttachAndSendRequest{Budget: spec},
		"add_run_agent":   AddRunAgentRequest{Budget: spec},
	}
	for name, req := range requests {
		data, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(string(data), `"Budget":{"tokens":1000000,"money_usd":5,"stop_policy":"medium"}`) {
			t.Errorf("%s: budget missing in %s", name, data)
		}
	}
}

func TestCreationRequestsOmitUnsetBudget(t *testing.T) {
	for name, req := range map[string]any{"attach": AttachRequest{}, "attach_and_send": AttachAndSendRequest{}, "add_run_agent": AddRunAgentRequest{}} {
		data, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.Contains(string(data), "Budget") {
			t.Errorf("%s: unset budget must be omitted: %s", name, data)
		}
	}
}

func TestCreationRequestsRejectNullBudget(t *testing.T) {
	for name, target := range map[string]any{
		"AttachRequest":        &AttachRequest{},
		"AttachAndSendRequest": &AttachAndSendRequest{},
		"AddRunAgentRequest":   &AddRunAgentRequest{},
	} {
		if err := json.Unmarshal([]byte(`{"Budget":null}`), target); err == nil {
			t.Errorf("%s accepted an explicit null budget", name)
		}
		if err := json.Unmarshal([]byte(`{}`), target); err != nil {
			t.Errorf("%s rejected an omitted budget: %v", name, err)
		}
	}
}

func TestCreationRequestsBudgetJSONBoundary(t *testing.T) {
	requests := map[string]func(body string) (*budget.Spec, error){
		"AttachRequest": func(body string) (*budget.Spec, error) {
			var r AttachRequest
			err := json.Unmarshal([]byte(body), &r)
			return r.Budget, err
		},
		"AttachAndSendRequest": func(body string) (*budget.Spec, error) {
			var r AttachAndSendRequest
			err := json.Unmarshal([]byte(body), &r)
			return r.Budget, err
		},
		"AddRunAgentRequest": func(body string) (*budget.Spec, error) {
			var r AddRunAgentRequest
			err := json.Unmarshal([]byte(body), &r)
			return r.Budget, err
		},
	}
	for name, decode := range requests {
		t.Run(name, func(t *testing.T) { budgetparity.CheckJSONContainer(t, decode) })
	}
}
