package assistants

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestProfileStageContextSnapshotRoundTrip(t *testing.T) {
	for _, ctx := range []*StageContext{nil, {Inputs: []string{}}, {Inputs: []string{"prep.rules"}}} {
		want := Task{ID: "task", ProfileSnapshot: Profile{ID: "writer", StageContext: ctx}}
		data, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		var got Task
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("round trip %s: got %+v", data, got)
		}
		if ctx == nil && strings.Contains(string(data), "StageContext") {
			t.Fatalf("absent field emitted: %s", data)
		}
	}
	var old Task
	if err := json.Unmarshal([]byte(`{"ID":"old","ProfileSnapshot":{"ID":"writer","Instructions":"test"}}`), &old); err != nil || old.ProfileSnapshot.StageContext != nil {
		t.Fatalf("old snapshot: %+v, %v", old, err)
	}
}

func TestProfileStageContextJSONRejected(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"inputs":null}`, `{"inputs":[] ,"unknown":true}`, `{"Inputs":[]}`, `{"inputs":[null]}`, `{"inputs":["broken"]}`, `{"inputs":[],"inputs":[]}`} {
		for _, key := range []string{"StageContext", "stagecontext"} {
			var task Task
			if err := json.Unmarshal([]byte(`{"ProfileSnapshot":{"`+key+`":`+raw+`}}`), &task); err == nil {
				t.Errorf("accepted %s: %s", key, raw)
			}
		}
	}
}

func TestStageContextYAMLRoundTrip(t *testing.T) {
	want := Profile{ID: "writer", StageContext: &StageContext{Inputs: []string{}}}
	data, err := yaml.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got Profile
	if err := yaml.Unmarshal(data, &got); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip %s: %+v, %v", data, got, err)
	}
}

func TestProfileStageContextDuplicateJSONField(t *testing.T) {
	for _, raw := range []string{
		`{"StageContext":null,"StageContext":{"inputs":[]}}`,
		`{"StageContext":{"inputs":[]},"stagecontext":{"inputs":[]}}`,
	} {
		var p Profile
		if err := json.Unmarshal([]byte(raw), &p); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}
