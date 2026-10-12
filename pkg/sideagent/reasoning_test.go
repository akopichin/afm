package sideagent

import "testing"

// The side agent is a default-claude invocation, so client.reasoning must reach the executor.Config it
// builds for a turn.
func TestManager_ForwardsReasoningToExecutor(t *testing.T) {
	ff := &fakeFactory{fakes: []*fakeRunner{{actions: []fakeAction{{"text", "ok"}}}}}
	m := New(Config{
		Store:       newTestStore(t),
		Command:     "claude",
		Reasoning:   "high",
		Context:     ContextBuilder{},
		NewExecutor: ff.make,
	})
	ch, cancel := m.Subscribe()
	defer cancel()
	if _, err := m.Send("c1", "hi"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	drainUntilTerminal(t, ch)
	if got := ff.fakes[0].cfg.Reasoning; got != "high" {
		t.Errorf("executor.Config.Reasoning = %q, want high", got)
	}
}
