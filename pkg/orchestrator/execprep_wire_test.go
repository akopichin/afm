package orchestrator

// Этап 9, T9.1/T8.5 — execprep_wire.go (buildPreparedCommandSet/IsCommandPrepared) shipped with zero direct
// tests (only exercised indirectly through graph_mutate_test.go's commit/preview tests). These cover the
// function in isolation: dedup across stages, the default/client command is always included, and a command
// never configured anywhere is NOT prepared.

import (
	"testing"

	"github.com/akopichin/afm/pkg/flow"
)

func TestBuildPreparedCommandSet_IncludesDefaultClientCommand(t *testing.T) {
	set := buildPreparedCommandSet(nil, "claude")
	if !set.IsPrepared("claude") {
		t.Fatal("default client command must always be prepared, even with zero stages")
	}
}

func TestBuildPreparedCommandSet_IncludesEveryDistinctStageCommand(t *testing.T) {
	stages := []flow.Stage{
		{ID: "a", Command: "claude"},
		{ID: "b", Command: "codex"},
		{ID: "c"}, // empty Command — falls back to the default client command, not its own entry
	}
	set := buildPreparedCommandSet(stages, "claude")
	for _, cmd := range []string{"claude", "codex"} {
		if !set.IsPrepared(cmd) {
			t.Fatalf("expected %q to be prepared", cmd)
		}
	}
}

func TestBuildPreparedCommandSet_DedupsAcrossStages(t *testing.T) {
	stages := []flow.Stage{
		{ID: "a", Command: "claude"},
		{ID: "b", Command: "claude"},
		{ID: "c", Command: "claude"},
	}
	set := buildPreparedCommandSet(stages, "claude")
	all := set.All()
	count := 0
	for _, r := range all {
		if r.Command == "claude" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("\"claude\" repeated across 3 stages (+ default) must appear exactly once in the prepared set, got %d: %+v", count, all)
	}
}

func TestBuildPreparedCommandSet_UnconfiguredCommandNotPrepared(t *testing.T) {
	stages := []flow.Stage{{ID: "a", Command: "claude"}}
	set := buildPreparedCommandSet(stages, "claude")
	if set.IsPrepared("some-command-nobody-configured") {
		t.Fatal("a command never configured anywhere (not the default, not any stage's) must not be prepared")
	}
}

func TestIsCommandPrepared_DelegatesToBuiltSet(t *testing.T) {
	o := &Orchestrator{preparedCommands: buildPreparedCommandSet([]flow.Stage{{ID: "a", Command: "codex"}}, "claude")}
	if !o.IsCommandPrepared("claude") {
		t.Fatal("IsCommandPrepared(claude) = false, want true (default command)")
	}
	if !o.IsCommandPrepared("codex") {
		t.Fatal("IsCommandPrepared(codex) = false, want true (stage a's command)")
	}
	if o.IsCommandPrepared("ghost-cmd") {
		t.Fatal("IsCommandPrepared(ghost-cmd) = true, want false (never configured)")
	}
}
