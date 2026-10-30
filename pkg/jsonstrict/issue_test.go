package jsonstrict_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/jsonstrict"
)

func TestIssueClassification(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		kind jsonstrict.IssueKind
	}{
		{`{"field":`, jsonstrict.SyntaxIssue},
		{`{"unknown":1}`, jsonstrict.StructureIssue},
		{`{"field":42}`, jsonstrict.StructureIssue},
		{`{"field":"x"}{}`, jsonstrict.RefusalIssue},
	} {
		var v struct {
			Field string `json:"field"`
		}
		err := jsonstrict.DecodeDisallowUnknown([]byte(tc.raw), &v)
		if got := jsonstrict.Classify(err); got != tc.kind {
			t.Errorf("%s: %s want %s (%v)", tc.raw, got, tc.kind, err)
		}
	}
	cause := errors.New("business invariant")
	err := fmt.Errorf("wrapped: %w", jsonstrict.NewIssue(jsonstrict.DomainIssue, cause))
	var issue *jsonstrict.Issue
	if !errors.Is(err, cause) || !errors.As(err, &issue) || jsonstrict.Classify(err) != jsonstrict.DomainIssue {
		t.Fatal("typed error lost its cause")
	}
}

func TestSingleObjectRefusesAmbiguousOrIncompleteInput(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{`{"text":"brace } and escaped \" quote"}`, true},
		{"```json\n{\"field\":1}\n```", true},
		{`prefix "quoted text {"field":1} suffix`, true},
		{`[{"field":1}]`, true},
		{`{"field":1}{"field":2}`, false},
		{`{"field":1}{"field":`, false},
		{`{"field":"unfinished`, false},
		{`no object`, false},
	} {
		candidate, ok := jsonstrict.SingleObject([]byte(tc.raw))
		if ok != tc.want {
			t.Errorf("%q: complete=%v want %v", tc.raw, ok, tc.want)
		}
		if ok && (!strings.HasPrefix(string(candidate), "{") || !strings.HasSuffix(string(candidate), "}")) {
			t.Fatal("not an object span")
		}
	}
}
