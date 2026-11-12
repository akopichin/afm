package graph

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/akopichin/afm/contracts/budget"
	"github.com/akopichin/afm/contracts/internal/budgetparity"
)

func TestStageDraftBudget(t *testing.T) {
	data, err := json.Marshal(StageDraft{ID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "Budget") {
		t.Errorf("draft without a budget must not mention it: %s", data)
	}
	want := StageDraft{ID: "s1", Budget: &budget.Spec{MoneyUSD: 2, StopPolicy: budget.PolicyInfo}}
	data, err = json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got StageDraft
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
	if got.Budget == nil || *got.Budget != *want.Budget {
		t.Fatalf("budget = %+v, want %+v", got.Budget, want.Budget)
	}
}

func TestStageDraftRejectsNullBudget(t *testing.T) {
	var d StageDraft
	if err := json.Unmarshal([]byte(`{"Name":"x","Budget":null}`), &d); err == nil {
		t.Fatalf("explicit null budget accepted: %+v", d)
	}
	if err := json.Unmarshal([]byte(`{"Name":"x"}`), &d); err != nil || d.Budget != nil {
		t.Fatalf("omitted budget = %+v, %v; want nil", d.Budget, err)
	}
}

func TestStageDraftBudgetJSONBoundary(t *testing.T) {
	budgetparity.CheckJSONContainer(t, func(body string) (*budget.Spec, error) {
		var d StageDraft
		err := json.Unmarshal([]byte(body), &d)
		return d.Budget, err
	})
}
