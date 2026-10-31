package sideagent

import (
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentjobs"
	"github.com/akopichin/afm/pkg/executor"
)

// Every Assistant turn runs with the job identity the caller configured, so it is measured in its own journal.
func TestSideAgent_TurnIsMeasuredAsAJob(t *testing.T) {
	store := newTestStore(t)
	runner := &fakeRunner{actions: []fakeAction{{"text", "answer"}}}
	job := &executor.JobConfig{Meta: agentjobs.JobMeta{AgentID: "side:run-test"}}
	m := New(Config{Store: store, Command: "codex", Job: job,
		NewExecutor: func(cfg executor.Config) AgentRunner { runner.cfg = cfg; return runner }})
	if _, err := m.Send("c-1", "hi", TurnTarget{}); err != nil {
		t.Fatal(err)
	}
	if !m.Drain(ctxWithTimeout(t, time.Second)) {
		t.Fatal("drain")
	}
	if runner.cfg.Job != job {
		t.Fatalf("turn job = %+v, want the configured job", runner.cfg.Job)
	}
}
