package state

import (
	"reflect"
	"testing"
)

func TestDiscussionQuestions_SaveIsIdempotentAndTakeConsumes(t *testing.T) {
	dir := t.TempDir() + "/stage"

	for _, q := range []DiscussionQuestion{
		{MessageID: "dm-1", Body: "how is it going?"},
		{MessageID: "dm-1", Body: "how is it going?"},
		{MessageID: "dm-2", Body: "and the plan?"},
	} {
		if err := SaveDiscussionQuestion(dir, q); err != nil {
			t.Fatalf("SaveDiscussionQuestion: %v", err)
		}
	}

	got, err := TakeDiscussionQuestions(dir)
	if err != nil {
		t.Fatalf("TakeDiscussionQuestions: %v", err)
	}
	want := []DiscussionQuestion{{MessageID: "dm-1", Body: "how is it going?"}, {MessageID: "dm-2", Body: "and the plan?"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("questions = %+v, want %+v (a re-saved id is not asked twice)", got, want)
	}

	again, err := TakeDiscussionQuestions(dir)
	if err != nil || len(again) != 0 {
		t.Fatalf("second take = %+v, %v; want none: a question goes into exactly one restarted prompt", again, err)
	}
}
