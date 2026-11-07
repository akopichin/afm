package memory

import (
	"strings"
	"testing"
)

func TestSelectHigh(t *testing.T) {
	in := "## High\n1. A — desc a\n2. B — desc b\n\n## Medium\n1. C — desc c\n\n## Low\n1. D — desc d\n"
	got := SelectHigh(in)
	if !strings.Contains(got, "A — desc a") || !strings.Contains(got, "B — desc b") {
		t.Errorf("High items missing: %q", got)
	}
	if strings.Contains(got, "desc c") || strings.Contains(got, "desc d") {
		t.Errorf("non-High leaked: %q", got)
	}
}

func TestSelectHigh_NoHigh(t *testing.T) {
	if SelectHigh("## Medium\n1. x\n") != "" {
		t.Error("no High → empty")
	}
}
