package main

import (
	"strings"
	"testing"
)

func TestNewRunID_UniqueWithinSameSecond(t *testing.T) {
	a := newRunID("flow")
	b := newRunID("flow")
	if a == b {
		t.Fatalf("run ids collided: %q == %q", a, b)
	}
	if !strings.HasPrefix(a, "flow-") {
		t.Fatalf("run id must start with flow-: %q", a)
	}
}
