package main

import (
	"errors"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentjobs"
	"github.com/akopichin/afm/pkg/clock"
	"github.com/akopichin/afm/pkg/sideagent"
	"github.com/akopichin/afm/pkg/state"
)

// A new journal's header says when recording began and how much active run time came before it; reopening
// the journal on resume keeps that header.
func TestOpenAgentJobs_HeaderHoldsRecordingStartAndBase(t *testing.T) {
	fc := clock.NewFake(time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC))
	state.SetClockForTest(fc)
	t.Cleanup(func() { state.SetClockForTest(nil) })
	dir := t.TempDir()
	store, err := state.Open(dir, []string{"build"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.BeginRun(); err != nil {
		t.Fatal(err)
	}
	fc.Advance(7 * time.Minute)
	if err := store.EndRun(state.RunStatusInterrupted); err != nil {
		t.Fatal(err)
	}

	before := time.Now().UTC()
	jobs := openAgentJobs(dir, store)
	after := time.Now().UTC()
	if err := jobs.Err(); err != nil {
		t.Fatal(err)
	}
	header := jobs.Snapshot().Header
	if header.BaseOffsetMS != 420_000 || header.RecordingStartedAt.Before(before) || header.RecordingStartedAt.After(after) {
		t.Fatalf("header = %+v, want base 420000 and a start between %s and %s", header, before, after)
	}
	_ = jobs.Close()

	reopened := openAgentJobs(dir, store)
	t.Cleanup(func() { _ = reopened.Close() })
	if got := reopened.Snapshot().Header; !got.RecordingStartedAt.Equal(header.RecordingStartedAt) || got.BaseOffsetMS != header.BaseOffsetMS {
		t.Fatalf("reopened header = %+v, want the original %+v", got, header)
	}
}

// The Assistant's jobs journal closes with its other stores, and only once its turn is drained.
func TestCloseStores_ClosesTheAssistantJobsJournalOnlyWhenDrained(t *testing.T) {
	dir := t.TempDir()
	store, err := sideagent.Open(dir, "run-jobs")
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := agentjobs.Open(store.Dir(), agentjobs.Header{RecordingStartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	rt := &sideAgentRuntime{store: store, jobs: jobs}
	late := agentjobs.Record{Type: agentjobs.TypeStartFailed, JobMeta: agentjobs.JobMeta{JobID: "j1"}, At: time.Now(), Reason: "setup"}

	rt.closeStores(false)
	if err := jobs.Append(late); err != nil {
		t.Fatalf("append while not drained = %v, want the journal still open", err)
	}
	rt.closeStores(true)
	late.JobID = "j2"
	if err := jobs.Append(late); !errors.Is(err, agentjobs.ErrClosed) {
		t.Fatalf("append after close = %v, want ErrClosed", err)
	}
}
