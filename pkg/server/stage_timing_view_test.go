package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/state"
)

func timingViewOf(t *testing.T, tm state.StageTiming) *StageTimingView {
	t.Helper()
	rs := state.RunState{
		StageOrder: []string{"s1"},
		Stages:     map[string]state.StageState{"s1": {Status: state.StatusRunning, Timing: tm}},
	}
	views := buildStageViews(rs, t.TempDir(), nil, nil)
	if len(views) != 1 || views[0].Timing == nil {
		t.Fatalf("views = %+v, want one view with timing", views)
	}
	return views[0].Timing
}

func TestStageTimingView_JSONContract(t *testing.T) {
	since := time.Date(2026, 10, 30, 20, 44, 12, 0, time.FixedZone("x", 3*3600))
	view := timingViewOf(t, state.StageTiming{
		Started: true, WorkMs: 1500, ApprovalMs: 2000, HelpersMs: 3000, BackoffMs: 4000, SchedulingMs: 5000, PausedMs: 6000,
		OpenKind: state.TimeApproval, OpenSince: since,
	})

	data, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"started":true,"complete":true,"work_ms":1500,"approval_ms":2000,"helpers_ms":3000,` +
		`"backoff_ms":4000,"scheduling_ms":5000,"paused_ms":6000,"open":{"kind":"approval","since":"2026-10-30T17:44:12Z"}}`
	if string(data) != want {
		t.Fatalf("timing JSON:\n got  %s\n want %s", data, want)
	}
}

func TestStageTimingView_ZeroSumsStayInTheJSON(t *testing.T) {
	data, err := json.Marshal(timingViewOf(t, state.StageTiming{Started: true}))
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"started":true,"complete":true,"work_ms":0,"approval_ms":0,"helpers_ms":0,"backoff_ms":0,"scheduling_ms":0,"paused_ms":0}`
	if string(data) != want {
		t.Fatalf("timing JSON:\n got  %s\n want %s", data, want)
	}
}

func TestStageTimingView_NotStarted(t *testing.T) {
	data, err := json.Marshal(timingViewOf(t, state.StageTiming{}))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"started":false,"complete":true}` {
		t.Fatalf("timing JSON = %s", data)
	}
}

func TestStageTimingView_IncompleteHasNoSumsAndNoOpen(t *testing.T) {
	data, err := json.Marshal(timingViewOf(t, state.StageTiming{
		Started: true, Incomplete: true, WorkMs: 99, OpenKind: state.TimeWork, OpenSince: time.Now(),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"started":true,"complete":false}` {
		t.Fatalf("timing JSON = %s", data)
	}
}

func TestStageTimingView_FilledWithoutAccounting(t *testing.T) {
	view := timingViewOf(t, state.StageTiming{Started: true, WorkMs: 1})
	if !view.Started || view.WorkMs != 1 {
		t.Fatalf("timing = %+v", view)
	}
}

// open is read from the stored interval, so a finished run exposes none.
func TestBuildStageViews_TimingOpenAbsentAfterEndRun(t *testing.T) {
	runDir := t.TempDir()
	store, err := state.Open(runDir, []string{"s1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.BeginRun(); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: "s1", From: state.StatusPending, To: state.StatusRunning, Event: "start_run"}); err != nil {
		t.Fatal(err)
	}

	live := buildStageViews(store.Snapshot(), runDir, nil, nil)[0].Timing
	if live == nil || !live.Started || live.Open == nil || live.Open.Kind != string(state.TimeWork) {
		t.Fatalf("live timing = %+v, want started with open work", live)
	}

	if err := store.EndRun("interrupted"); err != nil {
		t.Fatal(err)
	}
	ended := buildStageViews(store.Snapshot(), runDir, nil, nil)[0].Timing
	if ended == nil || !ended.Started || ended.Open != nil {
		t.Fatalf("ended timing = %+v, want started without open", ended)
	}
}
