package budgetparity

import (
	"testing"

	"github.com/akopichin/afm/contracts/budget"
)

// JSONCase is one JSON body of a container with a *budget.Spec field named Budget, and the budget the
// container must decode to; Err means decoding must fail.
type JSONCase struct {
	Name string
	Body string
	Want *budget.Spec
	Err  bool
}

var (
	oneToken = &budget.Spec{Tokens: 1, StopPolicy: budget.PolicyHard}
	twoUSD   = &budget.Spec{MoneyUSD: 2, StopPolicy: budget.PolicyHard}
)

// JSONContainerCases is the matrix every JSON container must pass. Only the Budget member is strict:
// unrelated members, repeats included, keep the lenient encoding/json behavior they had. A key written
// with JSON escapes is the same member as its plain spelling, as encoding/json decodes it.
var JSONContainerCases = []JSONCase{
	{Name: "absent", Body: `{}`},
	{Name: "null container", Body: `null`},
	{Name: "unrelated repeat", Body: `{"Other":1,"Other":2}`},
	{Name: "canonical", Body: `{"Budget":{"tokens":1}}`, Want: oneToken},
	{Name: "lower case", Body: `{"budget":{"money_usd":2}}`, Want: twoUSD},
	{Name: "unrelated repeat before a valid budget", Body: `{"Other":1,"Other":2,"Budget":{"tokens":1}}`, Want: oneToken},
	{Name: "escaped key", Body: `{"\u0042udget":{"tokens":1}}`, Want: oneToken},

	{Name: "null", Body: `{"Budget":null}`, Err: true},
	{Name: "lower-case null", Body: `{"budget":null}`, Err: true},
	{Name: "upper-case null", Body: `{"BUDGET":null}`, Err: true},
	{Name: "valid then null", Body: `{"Budget":{"tokens":1},"Budget":null}`, Err: true},
	{Name: "null then valid", Body: `{"Budget":null,"Budget":{"tokens":1}}`, Err: true},
	{Name: "null twice", Body: `{"Budget":null,"Budget":null}`, Err: true},
	{Name: "two different budgets", Body: `{"Budget":{"tokens":1},"Budget":{"tokens":2}}`, Err: true},
	{Name: "canonical and case-folded", Body: `{"Budget":{"tokens":1},"budget":{"tokens":2}}`, Err: true},
	{Name: "canonical and escaped key", Body: `{"Budget":{"tokens":1},"\u0042udget":{"tokens":1}}`, Err: true},
	{Name: "escaped null", Body: `{"\u0042udget":null}`, Err: true},
	{Name: "escaped lower-case null", Body: `{"\u0062udget":null}`, Err: true},
	{Name: "valid then escaped null", Body: `{"Budget":{"tokens":1},"\u0042udget":null}`, Err: true},
	{Name: "escaped null then valid", Body: `{"\u0042udget":null,"Budget":{"tokens":1}}`, Err: true},
	{Name: "unrelated repeat before null", Body: `{"Other":1,"Other":2,"Budget":null}`, Err: true},
	{Name: "empty budget", Body: `{"Budget":{}}`, Err: true},
	{Name: "repeated budget key", Body: `{"Budget":{"tokens":1,"tokens":2}}`, Err: true},
	{Name: "case-folded budget key", Body: `{"Budget":{"Tokens":1}}`, Err: true},
}

// CheckJSONContainer runs JSONContainerCases through decode, which unmarshals a body into one container
// (possibly nested in another) and returns that container's Budget.
func CheckJSONContainer(t *testing.T, decode func(body string) (*budget.Spec, error)) {
	t.Helper()
	for _, tc := range JSONContainerCases {
		t.Run(tc.Name, func(t *testing.T) {
			got, err := decode(tc.Body)
			if tc.Err {
				if err == nil {
					t.Errorf("%s accepted, budget %+v", tc.Body, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("%s: %v", tc.Body, err)
			}
			if (got == nil) != (tc.Want == nil) || (got != nil && *got != *tc.Want) {
				t.Errorf("%s: budget = %+v, want %+v", tc.Body, got, tc.Want)
			}
		})
	}
}
