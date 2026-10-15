package jsonstrict_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/jsonstrict"
)

func TestRejectFence(t *testing.T) {
	if err := jsonstrict.RejectFence([]byte("```json\n{}\n```")); !errors.Is(err, jsonstrict.ErrMarkdownFence) {
		t.Fatalf("RejectFence(fenced) = %v, want ErrMarkdownFence", err)
	}
	if err := jsonstrict.RejectFence([]byte(`{"a":1}`)); err != nil {
		t.Fatalf("RejectFence(bare) = %v, want nil", err)
	}
}

func TestEnforceMaxBytes(t *testing.T) {
	if err := jsonstrict.EnforceMaxBytes([]byte(strings.Repeat("a", 10)), 5); !errors.Is(err, jsonstrict.ErrTooLarge) {
		t.Fatalf("EnforceMaxBytes(over) = %v, want ErrTooLarge", err)
	}
	if err := jsonstrict.EnforceMaxBytes([]byte("abc"), 5); err != nil {
		t.Fatalf("EnforceMaxBytes(under) = %v, want nil", err)
	}
}

type decodeTarget struct {
	A int `json:"a"`
}

func TestDecodeDisallowUnknown_RejectsUnknownField(t *testing.T) {
	var v decodeTarget
	err := jsonstrict.DecodeDisallowUnknown([]byte(`{"a":1,"b":2}`), &v)
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
}

func TestDecodeDisallowUnknown_RejectsTrailingContent(t *testing.T) {
	var v decodeTarget
	err := jsonstrict.DecodeDisallowUnknown([]byte(`{"a":1}{"a":2}`), &v)
	if !errors.Is(err, jsonstrict.ErrTrailingContent) {
		t.Fatalf("err = %v, want ErrTrailingContent", err)
	}
}

func TestDecodeDisallowUnknown_RejectsTrailingJunk(t *testing.T) {
	var v decodeTarget
	err := jsonstrict.DecodeDisallowUnknown([]byte(`{"a":1} trailing-junk`), &v)
	if !errors.Is(err, jsonstrict.ErrTrailingContent) {
		t.Fatalf("err = %v, want ErrTrailingContent", err)
	}
}

func TestDecodeDisallowUnknown_Accepts(t *testing.T) {
	var v decodeTarget
	if err := jsonstrict.DecodeDisallowUnknown([]byte(`{"a":1}`), &v); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.A != 1 {
		t.Fatalf("A = %d, want 1", v.A)
	}
}

func TestDecodeNoDuplicates_TopLevel(t *testing.T) {
	err := jsonstrict.DecodeNoDuplicates([]byte(`{"a":1,"a":2}`))
	if !errors.Is(err, jsonstrict.ErrDuplicateKey) {
		t.Fatalf("err = %v, want ErrDuplicateKey", err)
	}
}

func TestDecodeNoDuplicates_NestedSecondLevel(t *testing.T) {
	err := jsonstrict.DecodeNoDuplicates([]byte(`{"a":1,"findings":[{"id":"f1","id":"f2"}]}`))
	if !errors.Is(err, jsonstrict.ErrDuplicateKey) {
		t.Fatalf("err = %v, want ErrDuplicateKey for a 2nd-level dup", err)
	}
}

func TestDecodeNoDuplicates_NoFalsePositive(t *testing.T) {
	if err := jsonstrict.DecodeNoDuplicates([]byte(`{"a":1,"b":{"a":1}}`)); err != nil {
		t.Fatalf("same key name in a DIFFERENT object must not be flagged: %v", err)
	}
}

func TestRepairRawNewlines_OnlyInsideStrings(t *testing.T) {
	pretty := "{\n  \"a\": 1\n}"
	if _, changed := jsonstrict.RepairRawNewlines([]byte(pretty)); changed {
		t.Fatal("structural whitespace outside strings must not be escaped")
	}
	inStr := "{\"s\":\"a\nb\"}"
	got, changed := jsonstrict.RepairRawNewlines([]byte(inStr))
	if !changed || string(got) != `{"s":"a\nb"}` {
		t.Fatalf("raw newline inside a string must be escaped, got %q changed=%v", got, changed)
	}
	already := `{"s":"a\nb"}`
	if _, changed := jsonstrict.RepairRawNewlines([]byte(already)); changed {
		t.Fatal("already-escaped \\n must not be touched")
	}
}
