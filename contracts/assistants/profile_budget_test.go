package assistants

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/akopichin/afm/contracts/budget"
	"github.com/akopichin/afm/contracts/internal/budgetparity"
)

// Журнал и state.json хранят Profile и Task дословно: без бюджета JSON не должен меняться.
func TestProfileBudget_OmittedWhenUnset(t *testing.T) {
	task := Task{ID: "t1", ProfileSnapshot: Profile{ID: "qa", Title: "QA"}}
	for name, v := range map[string]any{"profile": Profile{ID: "qa"}, "task": task} {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "Budget") {
			t.Errorf("%s JSON mentions Budget: %s", name, data)
		}
	}
}

func TestProfileBudget_RoundTrips(t *testing.T) {
	want := Profile{ID: "qa", Budget: &budget.Spec{Tokens: 500_000, StopPolicy: budget.PolicyHard}}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got Profile
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
	if got.Budget == nil || *got.Budget != *want.Budget {
		t.Fatalf("budget = %+v, want %+v (json %s)", got.Budget, want.Budget, data)
	}
}

// Профиль и задача без бюджета сериализуются байт-в-байт как до появления поля (снято с базовой ревизии).
func TestProfileAndTaskJSONAreByteIdentical(t *testing.T) {
	profile := `{"ID":"p","Title":"T","Command":"claude","Instructions":"","Capabilities":{"Delegate":false,"Browser":false},"ReadonlyEnforcement":""}`
	task := `{"ID":"t1","RunID":"","Scope":"","RootStageID":"","ParentTaskID":null,"Depth":0,"PolicyRev":"","MaxDepthAtCreate":0,"ProfileID":"","ProfileSnapshot":{"ID":"p","Title":"","Command":"","Instructions":"","Capabilities":{"Delegate":false,"Browser":false},"ReadonlyEnforcement":""},"Execution":null,"Title":"","Instruction":"","Mode":"","Trigger":"","Skills":null,"SkillsDir":"","Status":"","StatusReason":"","RootGeneration":0,"ParentGeneration":0,"TaskGeneration":0,"CreatedBy":"","CreatedAt":"0001-01-01T00:00:00Z","StartedAt":"0001-01-01T00:00:00Z","FinishedAt":"0001-01-01T00:00:00Z"}`
	for name, tc := range map[string]struct {
		v    any
		want string
	}{
		"profile": {Profile{ID: "p", Title: "T", Command: "claude"}, profile},
		"task":    {Task{ID: "t1", ProfileSnapshot: Profile{ID: "p"}}, task},
	} {
		got, err := json.Marshal(tc.v)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != tc.want {
			t.Errorf("%s JSON changed:\n got %s\nwant %s", name, got, tc.want)
		}
	}
}

func TestProfileBudget_JSONNullIsRejected(t *testing.T) {
	var p Profile
	if err := json.Unmarshal([]byte(`{"ID":"p","Budget":null}`), &p); err == nil {
		t.Fatalf("explicit null budget accepted: %+v", p)
	}
	if err := json.Unmarshal([]byte(`{"ID":"p"}`), &p); err != nil || p.Budget != nil {
		t.Fatalf("omitted budget = %+v, %v; want nil", p.Budget, err)
	}
}

func TestProfileBudget_JSONBoundary(t *testing.T) {
	t.Run("profile", func(t *testing.T) {
		budgetparity.CheckJSONContainer(t, func(body string) (*budget.Spec, error) {
			var p Profile
			err := json.Unmarshal([]byte(body), &p)
			return p.Budget, err
		})
	})
	t.Run("task snapshot", func(t *testing.T) {
		budgetparity.CheckJSONContainer(t, func(body string) (*budget.Spec, error) {
			var task Task
			err := json.Unmarshal([]byte(`{"ID":"t1","ProfileSnapshot":`+body+`}`), &task)
			return task.ProfileSnapshot.Budget, err
		})
	})
}
