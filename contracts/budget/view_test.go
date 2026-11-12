package budget

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestViewJSONRoundTrip(t *testing.T) {
	spec := Spec{Tokens: 100, MoneyUSD: 2, StopPolicy: PolicyMedium}
	in := View{
		Scope: StageScope("implement"), Kind: ViewStage, Revision: 3,
		Base: &spec, BaseSource: SourceProfile,
		Declaration: &DeclarationRef{Kind: DeclStageAssistant, StageID: "implement", Profile: "reviewer"},
		Override:    Override{Kind: OverrideLimit, Spec: Spec{Tokens: 50, StopPolicy: PolicyHard}},
		Effective:   &Spec{Tokens: 50, StopPolicy: PolicyHard},
		Spent:       Spend{Tokens: 40, MoneyNanoUSD: 5, Records: 2, Unmetered: 1},
		Evaluation:  Evaluation{Tokens: LevelWarning},
		ParentLimits: []ParentLimit{{
			Scope: RunScope(), Limit: spec, Spent: Spend{Tokens: 90}, Evaluation: Evaluation{Tokens: LevelWarning, Money: LevelOK},
		}},
		Hold:    &Hold{Reason: HoldParentLimitReached, Scope: RunScope(), Metric: MetricMoney},
		Actions: []Action{ActionUpdate, ActionResume, ActionSaveToYAML},
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out View
	if err := json.Unmarshal(data, &out); err != nil || !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip: %v\n in: %+v\nout: %+v", err, in, out)
	}
}

func TestZeroViewMarshals(t *testing.T) {
	if _, err := json.Marshal(View{Scope: RunScope(), Kind: ViewRun}); err != nil {
		t.Fatalf("a view without limits must marshal: %v", err)
	}
}

func TestUpdateResultAndNoticeRoundTrip(t *testing.T) {
	res := UpdateResult{
		View:   View{Scope: RunScope(), Kind: ViewRun, Revision: 1},
		Export: &ExportResult{Status: ExportFailed, Path: "flow.yaml", Error: "read-only"},
	}
	notice := Notice{
		Kind: NoticeLimitReached, Scope: TaskScope("t1"), Metric: MetricTokens,
		Limit: Spec{Tokens: 10, StopPolicy: PolicyHard}, Spent: Spend{Tokens: 11}, Policy: PolicyHard, Revision: 2,
	}
	for _, v := range []any{res, notice} {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		back := reflect.New(reflect.TypeOf(v))
		if err := json.Unmarshal(data, back.Interface()); err != nil || !reflect.DeepEqual(v, back.Elem().Interface()) {
			t.Errorf("%T round trip: %v\n%s", v, err, data)
		}
	}
}

func TestUpdateRequestStrict(t *testing.T) {
	var ok UpdateRequest
	good := `{"scope":"stage:a","expected_revision":4,"override":{"kind":"limit","spec":{"tokens":9}},"save_to_yaml":true,"resume":true}`
	if err := json.Unmarshal([]byte(good), &ok); err != nil || ok.Scope != StageScope("a") || !ok.Resume || ok.Override.Spec.StopPolicy != PolicyHard {
		t.Fatalf("good request: %+v, %v", ok, err)
	}
	bad := []string{
		`{"scope":"bad","override":{"kind":"none"}}`,
		`{"scope":"run","override":{"kind":"limit"}}`,
		`{"scope":"run","expected_revision":-1,"override":{"kind":"none"}}`,
		`{"scope":"run","override":{"kind":"none"},"extra":1}`,
	}
	for _, in := range bad {
		var r UpdateRequest
		if err := json.Unmarshal([]byte(in), &r); err == nil {
			t.Errorf("update request %s = %+v, want error", in, r)
		}
	}
}

func TestResumeRequestStrict(t *testing.T) {
	var r ResumeRequest
	if err := json.Unmarshal([]byte(`{"scope":"task:x","expected_revision":2}`), &r); err != nil || r.ExpectedRevision != 2 {
		t.Fatalf("good: %+v, %v", r, err)
	}
	for _, in := range []string{`{"scope":"nope"}`, `{"scope":"run","expected_revision":-2}`, `{"scope":"run","x":1}`} {
		var r ResumeRequest
		if err := json.Unmarshal([]byte(in), &r); err == nil {
			t.Errorf("resume request %s must fail", in)
		}
	}
}
