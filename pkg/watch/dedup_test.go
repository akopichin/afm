package watch

import (
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/executor"
)

// TestDedup_StableKey: the SAME (checkpointID, taskID, generation) always yields the SAME key; changing any
// one of the three inputs — notably generation, which marks a superseded attempt lineage — yields a different
// one, even when the checkpoint id otherwise matches exactly.
func TestDedup_StableKey(t *testing.T) {
	k1 := DedupKey("s1/implementation@000005", "t-abc", 1)
	k2 := DedupKey("s1/implementation@000005", "t-abc", 1)
	if k1 != k2 {
		t.Fatalf("same inputs must yield the same key: %q != %q", k1, k2)
	}
	if k3 := DedupKey("s1/implementation@000005", "t-abc", 2); k3 == k1 {
		t.Fatal("a different generation must yield a different key, even for the same checkpoint id")
	}
	if k4 := DedupKey("s1/implementation@000006", "t-abc", 1); k4 == k1 {
		t.Fatal("a different checkpoint id must yield a different key")
	}
	if k5 := DedupKey("s1/implementation@000005", "t-xyz", 1); k5 == k1 {
		t.Fatal("a different task id must yield a different key")
	}
}

// TestLoop_AssistantMessageIsNotMainAgentOutput: an observation whose Actor identifies a HELPER/watcher
// invocation (rather than the stage's own main-agent phase) must be rejected by IsMainAgentObservation — the
// collector-level guard against a watcher's own output looping back as if it were new main-agent content.
func TestLoop_AssistantMessageIsNotMainAgentOutput(t *testing.T) {
	mainAgent := executor.Observation{Actor: "s1/implementation"}
	if !IsMainAgentObservation(mainAgent) {
		t.Fatal("a normal main-agent observation must be accepted")
	}
	helperOwn := executor.Observation{Actor: "task:t-abc/helper"}
	if IsMainAgentObservation(helperOwn) {
		t.Fatal("a helper/watcher's own output must NOT be accepted as main-agent output")
	}
}

// TestLoop_ChildNotAutoSubscribedToAncestors: a brand new Collector (standing in for a newly attached
// watcher, e.g. a delegated child) starts completely empty — it is never implicitly seeded from, or made to
// share state with, another Collector's already-accumulated pending observations just because both exist at
// the same time. Each Collector only ever reacts to what is explicitly fed to IT via its own Observe calls
// (dev-plan.md T5.5: "создание ребёнка не подписывает его на все сообщения всех предков и соседей; источники
// задаются явно"). The orchestrator-level sibling of this guarantee — TWO bound watcher tasks rooted at
// DIFFERENT stages never cross-feed each other's observations — lives in
// pkg/orchestrator/watch_tasks_test.go's TestLoop_ChildNotAutoSubscribedToAncestors, since that requires the
// real bindWatcher/feedWatchCollectors wiring this package deliberately knows nothing about.
func TestLoop_ChildNotAutoSubscribedToAncestors(t *testing.T) {
	clock := newFakeClock()
	ancestor := NewCollector(Config{Debounce: 0, Cooldown: 0, MinNewContentBytes: 1, MaxPasses: 10}, clock, nil)
	for i := 0; i < 50; i++ {
		ancestor.Observe(obs(i, substantialContent(100)))
	}
	clock.Advance(time.Hour)
	if _, ready := ancestor.Ready(); !ready {
		t.Fatal("test setup: the ancestor collector should be ready to fire given all this accumulated content")
	}

	// A freshly created "child" Collector, built at the exact same moment, must be completely unaffected by
	// the ancestor's own accumulated state.
	child := NewCollector(Config{Debounce: 0, Cooldown: 0, MinNewContentBytes: 1, MaxPasses: 10}, clock, nil)
	if _, ready := child.Ready(); ready {
		t.Fatal("a brand new Collector must never be auto-subscribed to an ancestor's already-pending content")
	}
	if child.Passes() != 0 {
		t.Fatalf("child.Passes() = %d, want 0 — it has never been explicitly fed anything", child.Passes())
	}
}

// TestLoop_SummaryDoesNotTriggerItself: the checkpoint summary text a Collector just fired (RenderBundle's
// output) must never be fed back into ANY Collector and treated as fresh main-agent content that could
// trigger a second, immediate pass — even if some future caller mistakenly tried to loop it back in. The
// production filter wired by bindWatcher (IsMainAgentObservation) rejects it because its Actor identifies the
// watcher's OWN invocation, exactly like TestLoop_AssistantMessageIsNotMainAgentOutput's single-level case,
// but exercised here through a real fire -> render -> (attempted) re-feed cycle.
func TestLoop_SummaryDoesNotTriggerItself(t *testing.T) {
	clock := newFakeClock()
	c := NewCollector(Config{Debounce: 0, Cooldown: 0, MinNewContentBytes: 1, MaxPasses: 10}, clock, IsMainAgentObservation)

	c.Observe(obs(1, "substantial main-agent content"))
	clock.Advance(time.Second)
	bundle, ready := c.Ready()
	if !ready {
		t.Fatal("expected the first substantial observation to fire")
	}
	c.SetBusy(false)

	// Render the fired bundle into the watcher's own checkpoint summary and feed it back as though it were new
	// output from the watcher's OWN turn (Actor identifies a helper/task invocation) — this must be rejected
	// outright by the SAME filter production wires, never counted as new main-agent content.
	summary := RenderBundle(bundle)
	c.Observe(executor.Observation{
		Seq: 2, Complete: true, Type: executor.ObservationCheckpoint,
		Content: summary, Actor: "task:t-self/helper",
	})
	clock.Advance(time.Hour)
	if _, ready := c.Ready(); ready {
		t.Fatal("the watcher's own synthesized summary must never be treated as new main-agent content")
	}
	if c.Passes() != 1 {
		t.Fatalf("passes = %d, want 1 — the self-fed summary must not have counted as, or caused, a second pass", c.Passes())
	}
}
