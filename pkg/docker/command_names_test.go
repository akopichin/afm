package docker_test

import (
	"slices"
	"testing"

	"github.com/akopichin/afm/pkg/docker"
	"github.com/akopichin/afm/pkg/flow"
)

// CommandNames — единый перечень команд для монтирования и сравнения Docker-проекции: отсортирован, без
// повторов, без пустых имён и встроенного claude.
func TestCommandNames(t *testing.T) {
	f := &flow.Flow{Stages: []flow.Stage{
		{ID: "a", Command: "zeta"},
		{ID: "b", Command: "claude"},
		{ID: "c", Command: "alpha", Verify: flow.VerifySpec{Steps: []flow.VerifyStep{
			{Kind: flow.VerifyAgent, Command: "verifier"},
			{Kind: flow.VerifyShell, Run: "true"},
		}}},
		{ID: "d"},
	}}

	got := docker.CommandNames(f, "global", []string{"helper", "alpha", ""})

	want := []string{"alpha", "global", "helper", "verifier", "zeta"}
	if !slices.Equal(got, want) {
		t.Fatalf("CommandNames = %v, want %v", got, want)
	}
}

func TestCommandNames_NilFlow(t *testing.T) {
	if got := docker.CommandNames(nil, "claude", nil); len(got) != 0 {
		t.Fatalf("CommandNames(nil, claude) = %v, want none", got)
	}
}
