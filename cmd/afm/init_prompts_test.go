package main

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

func TestPromptLine_ReturnsTrimmedInput(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader("  hello world  \n"))
	var out bytes.Buffer
	got := promptLine(scanner, &out, "Name: ")
	if got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
	if !strings.Contains(out.String(), "Name: ") {
		t.Errorf("label not printed: %q", out.String())
	}
}

func TestPromptChoice_DefaultOnEmptyInput(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader("\n"))
	var out bytes.Buffer
	got := promptChoice(scanner, &out, "Pick one:", []string{"A", "B", "C"}, 1)
	if got != 1 {
		t.Errorf("got %d, want 1", got)
	}
}

func TestPromptChoice_ValidSelection(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader("3\n"))
	var out bytes.Buffer
	got := promptChoice(scanner, &out, "Pick one:", []string{"A", "B", "C"}, 0)
	if got != 2 {
		t.Errorf("got %d, want 2", got)
	}
}

func TestPromptChoice_RepromptsOnInvalidInput(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader("bogus\n5\n2\n"))
	var out bytes.Buffer
	got := promptChoice(scanner, &out, "Pick one:", []string{"A", "B", "C"}, 0)
	if got != 1 {
		t.Errorf("got %d, want 1", got)
	}
}

func TestPromptYesNo_Default(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader("\n"))
	var out bytes.Buffer
	if got := promptYesNo(scanner, &out, "OK? ", true); !got {
		t.Error("expected default true")
	}
}

func TestPromptYesNo_ExplicitNo(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader("n\n"))
	var out bytes.Buffer
	if got := promptYesNo(scanner, &out, "OK? ", true); got {
		t.Error("expected false")
	}
}

func TestPromptInt_Default(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader("\n"))
	var out bytes.Buffer
	if got := promptInt(scanner, &out, "N? ", 2); got != 2 {
		t.Errorf("got %d, want 2", got)
	}
}

func TestPromptInt_Explicit(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader("5\n"))
	var out bytes.Buffer
	if got := promptInt(scanner, &out, "N? ", 2); got != 5 {
		t.Errorf("got %d, want 5", got)
	}
}

func TestParsePhaseSelection_EmptyReturnsDefaults(t *testing.T) {
	got := parsePhaseSelection("", 2, []int{0})
	if len(got) != 1 || got[0] != 0 {
		t.Errorf("got %v, want [0]", got)
	}
}

func TestParsePhaseSelection_ParsesValidIndices(t *testing.T) {
	got := parsePhaseSelection("1,2", 2, []int{0})
	if len(got) != 2 || got[0] != 0 || got[1] != 1 {
		t.Errorf("got %v, want [0 1]", got)
	}
}

func TestParsePhaseSelection_IgnoresInvalidIndices(t *testing.T) {
	got := parsePhaseSelection("1,9,bogus", 2, []int{0})
	if len(got) != 1 || got[0] != 0 {
		t.Errorf("got %v, want [0]", got)
	}
}
