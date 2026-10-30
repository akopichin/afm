package state

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
)

func summaryStep(source, reason string) *SummaryStep {
	return &SummaryStep{Source: source, Reason: reason}
}

func TestSummaryCorrections_StepsCountPerSourceAndNameTheLatest(t *testing.T) {
	rs := NewRunState([]string{"s"})
	rs.applyTransition(&Transition{Seq: 1, StageID: "s", From: StatusPending, To: StatusRunning, Event: "start_run"})
	if rs.Stages["s"].Summary != nil {
		t.Fatalf("a stage that never reserved a correction has summary %+v, want none", rs.Stages["s"].Summary)
	}

	rs.applyTransition(&Transition{Seq: 2, StageID: "s", From: StatusRunning, To: StatusRetrying, Event: "schedule_retry", SummaryStep: summaryStep(SummarySourcePostExit, "missing")})
	rs.applyTransition(&Transition{Seq: 3, StageID: "s", From: StatusRetrying, To: StatusRunning, Event: "resume_after_retry"})
	rs.applyTransition(&Transition{Seq: 4, StageID: "s", From: StatusRunning, To: StatusRunning, Event: "reserve_summary_hook", SummaryStep: summaryStep(SummarySourceStopHook, "empty")})

	want := &SummaryCorrections{PostExit: 1, Hook: 1, Reason: "empty", ReservedSeq: 4, PostExitSeq: 2}
	if got := rs.Stages["s"].Summary; !reflect.DeepEqual(got, want) {
		t.Fatalf("summary = %+v, want %+v", got, want)
	}
}

func TestSummaryCorrections_CarriedUntilTheUserStartsOver(t *testing.T) {
	for _, reset := range []struct {
		name  string
		event string
		from  StageStatus
		to    StageStatus
	}{
		{"manual retry", "manual_retry", StatusFailed, StatusPending},
		{"revise", "revise", StatusRunning, StatusRevising},
	} {
		t.Run(reset.name, func(t *testing.T) {
			rs := NewRunState([]string{"s"})
			rs.applyTransition(&Transition{Seq: 1, StageID: "s", From: StatusPending, To: StatusRunning, Event: "start_run"})
			rs.applyTransition(&Transition{Seq: 2, StageID: "s", From: StatusRunning, To: StatusRetrying, Event: "schedule_retry", SummaryStep: summaryStep(SummarySourcePostExit, "missing")})

			// Pause, Continue, a quota park or a peer wait are not a new cycle.
			rs.applyTransition(&Transition{Seq: 3, StageID: "s", From: StatusRetrying, To: StatusPaused, Event: "pause"})
			rs.applyTransition(&Transition{Seq: 4, StageID: "s", From: StatusPaused, To: StatusRetrying, Event: "continue"})
			if got := rs.Stages["s"].Summary; got == nil || got.PostExit != 1 {
				t.Fatalf("after pause and continue summary = %+v, want the spent correction kept", got)
			}

			rs.applyTransition(&Transition{Seq: 5, StageID: "s", From: reset.from, To: reset.to, Event: reset.event})
			if got := rs.Stages["s"].Summary; got != nil {
				t.Fatalf("after %s summary = %+v, want a new cycle", reset.name, got)
			}
		})
	}
}

func TestSummaryCorrections_ReplayMatchesLiveProjection(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"s"})
	if err != nil {
		t.Fatal(err)
	}
	apply := func(from, to StageStatus, event string, step *SummaryStep) {
		t.Helper()
		if err := store.Apply(&Transition{StageID: "s", From: from, To: to, Event: event, SummaryStep: step}); err != nil {
			t.Fatal(err)
		}
	}
	apply(StatusPending, StatusRunning, "start_run", nil)
	apply(StatusRunning, StatusRetrying, "schedule_retry", summaryStep(SummarySourcePostExit, "missing"))
	apply(StatusRetrying, StatusRunning, "resume_after_retry", nil)
	apply(StatusRunning, StatusRetrying, "schedule_retry", summaryStep(SummarySourcePostExit, "empty"))
	live := store.Snapshot().Stages["s"].Summary
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	rs, err := LoadRunState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := rs.Stages["s"].Summary; !reflect.DeepEqual(got, live) || got == nil || got.PostExit != 2 {
		t.Fatalf("replayed summary = %+v, live = %+v, want equal with two post-exit corrections", got, live)
	}
}

// A journal written before the field replays unchanged, and a transition that spends nothing writes nothing new.
func TestSummaryCorrections_OldJournalCompatible(t *testing.T) {
	var old Transition
	line := `{"seq":2,"time":"2026-10-29T10:00:00Z","stage_id":"s","from":"running","to":"retrying","event":"schedule_retry","reason":"attempt 1/5 in 5s"}`
	if err := json.Unmarshal([]byte(line), &old); err != nil {
		t.Fatal(err)
	}
	if old.SummaryStep != nil {
		t.Fatalf("old transition decoded with a summary step: %+v", old.SummaryStep)
	}
	data, err := json.Marshal(Transition{StageID: "s", Event: "start_run"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "summary") {
		t.Fatalf("a transition without a step is journaled as %s", data)
	}
}

// A running stage that reserves the hook reminder stays running: a wait for another status stays pending, and one
// for running was resolved when it was registered, so the self-transition resolves nothing it should not.
func TestSummaryCorrections_SelfTransitionResolvesNoWait(t *testing.T) {
	rs := NewRunState([]string{"s"})
	rs.applyTransition(&Transition{Seq: 1, StageID: "s", From: StatusPending, To: StatusRunning, Event: "start_run"})
	cond := agentcomm.WaitCondition{Kind: agentcomm.WaitOnStageStatus, StageID: "s", Status: string(StatusDone)}
	if err := applyControlToRunState(rs, meshWaitRegisteredControl("w1", "user", cond, "", "", 0)); err != nil {
		t.Fatal(err)
	}
	rs.applyTransition(&Transition{Seq: 2, StageID: "s", From: StatusRunning, To: StatusRunning, Event: "reserve_summary_hook", SummaryStep: summaryStep(SummarySourceStopHook, "missing")})
	if got := rs.Assistants.Mesh.Waits["w1"].Status; got != agentcomm.WaitPending {
		t.Fatalf("wait for done after the self-transition = %s, want pending", got)
	}
}

// A self-transition that records a correction moves nothing: the projection keeps every field — the quota intent
// and the peer continuation included — except the correction itself, the stage's latest seq and its timestamp.
func TestSummaryCorrections_SelfTransitionLeavesTheRestOfTheProjection(t *testing.T) {
	for _, source := range []string{SummarySourceStopHook, SummarySourcePostExitIssued} {
		t.Run(source, func(t *testing.T) {
			rs := NewRunState([]string{"s"})
			rs.applyTransition(&Transition{Seq: 1, StageID: "s", From: StatusPending, To: StatusAwaitingPeer, Event: "park_peer", PeerWaits: []agentcomm.WaitID{"w1"}})
			rs.applyTransition(&Transition{Seq: 2, StageID: "s", From: StatusAwaitingPeer, To: StatusRunning, Event: "peer_replied"})
			st := rs.Stages["s"]
			st.Quota = &QuotaWait{Provider: "claude", Waits: 2, ManualProbe: true, VerifyResume: true}
			rs.Stages["s"] = st
			before := rs.Stages["s"]
			if !before.PeerContinued {
				t.Fatal("setup: the resume after a peer wait must continue the earlier turns")
			}

			rs.applyTransition(&Transition{Seq: 3, Time: before.UpdatedAt.Add(time.Second), StageID: "s", From: StatusRunning, To: StatusRunning, Event: "reserve_summary_hook", SummaryStep: summaryStep(source, "missing")})

			after := rs.Stages["s"]
			if after.Summary == nil {
				t.Fatal("the self-transition recorded no correction")
			}
			after.Summary, after.Seq, after.UpdatedAt = before.Summary, before.Seq, before.UpdatedAt
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("projection changed beyond the correction:\n got %+v\nwant %+v", after, before)
			}
		})
	}
}

// A restart replays the journal: the stage that resumed after a peer wait and then reserved a correction still
// starts its next turn with the earlier turns' digest.
func TestSummaryCorrections_PeerContinuationSurvivesReserveAndReplay(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"s"})
	if err != nil {
		t.Fatal(err)
	}
	apply := func(from, to StageStatus, event string, step *SummaryStep) {
		t.Helper()
		if err := store.Apply(&Transition{StageID: "s", From: from, To: to, Event: event, SummaryStep: step}); err != nil {
			t.Fatal(err)
		}
	}
	apply(StatusPending, StatusRunning, "start_run", nil)
	apply(StatusRunning, StatusAwaitingPeer, "park_peer", nil)
	apply(StatusAwaitingPeer, StatusRunning, "peer_replied", nil)
	apply(StatusRunning, StatusRunning, "reserve_summary_hook", summaryStep(SummarySourceStopHook, "missing"))
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	rs, err := LoadRunState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := rs.Stages["s"]; !got.PeerContinued || got.Summary == nil || got.Summary.Hook != 1 {
		t.Fatalf("after replay peer_continued=%v summary=%+v, want the continuation kept and one reminder", got.PeerContinued, got.Summary)
	}
}

// The mark that a post-exit correction was launched leaves its count and its reservation alone; the next
// reservation or a fresh start clears it.
func TestSummaryCorrections_IssuedMarkFollowsTheReservation(t *testing.T) {
	rs := NewRunState([]string{"s"})
	rs.applyTransition(&Transition{Seq: 1, StageID: "s", From: StatusPending, To: StatusRunning, Event: "start_run"})
	rs.applyTransition(&Transition{Seq: 2, StageID: "s", From: StatusRunning, To: StatusRetrying, Event: "schedule_retry", SummaryStep: summaryStep(SummarySourcePostExit, "missing")})
	rs.applyTransition(&Transition{Seq: 3, StageID: "s", From: StatusRetrying, To: StatusRunning, Event: "resume_after_retry"})
	if got := rs.Stages["s"].Summary; got.Issued {
		t.Fatalf("a reserved correction is marked launched: %+v", got)
	}

	rs.applyTransition(&Transition{Seq: 4, StageID: "s", From: StatusRunning, To: StatusRunning, Event: "reserve_summary_hook", SummaryStep: summaryStep(SummarySourcePostExitIssued, "")})
	want := &SummaryCorrections{PostExit: 1, Reason: "missing", ReservedSeq: 2, PostExitSeq: 2, Issued: true}
	if got := rs.Stages["s"].Summary; !reflect.DeepEqual(got, want) {
		t.Fatalf("after the launch summary = %+v, want %+v", got, want)
	}

	rs.applyTransition(&Transition{Seq: 5, StageID: "s", From: StatusRunning, To: StatusRetrying, Event: "schedule_retry", SummaryStep: summaryStep(SummarySourcePostExit, "empty")})
	want = &SummaryCorrections{PostExit: 2, Reason: "empty", ReservedSeq: 5, PostExitSeq: 5}
	if got := rs.Stages["s"].Summary; !reflect.DeepEqual(got, want) {
		t.Fatalf("after the next reservation summary = %+v, want %+v", got, want)
	}
}

// The author's outcome is known as soon as the stage changes status after the launch (a park, a transport retry, an
// end): every such transition drops the launch mark, so a restart continues the same reserved correction.
func TestSummaryCorrections_KnownParkDropsTheIssuedMark(t *testing.T) {
	for _, exit := range []struct {
		name  string
		event string
		to    StageStatus
	}{
		{"usage limit", "await_quota", StatusAwaitingQuota},
		{"user question", "ask_user", StatusAwaitingUserInput},
		{"peer wait", "await_peer", StatusAwaitingPeer},
		{"pause", "pause", StatusPaused},
		{"transport retry", "schedule_retry", StatusRetrying},
		{"assistants wait", "await_assistants", StatusAwaitingAssistants},
		{"failure", "fail", StatusFailed},
		{"completion", "complete", StatusDone},
	} {
		t.Run(exit.name, func(t *testing.T) {
			rs := NewRunState([]string{"s"})
			rs.applyTransition(&Transition{Seq: 1, StageID: "s", From: StatusPending, To: StatusRunning, Event: "start_run"})
			rs.applyTransition(&Transition{Seq: 2, StageID: "s", From: StatusRunning, To: StatusRetrying, Event: "schedule_retry", SummaryStep: summaryStep(SummarySourcePostExit, "missing")})
			rs.applyTransition(&Transition{Seq: 3, StageID: "s", From: StatusRetrying, To: StatusRunning, Event: "resume_after_retry"})
			rs.applyTransition(&Transition{Seq: 4, StageID: "s", From: StatusRunning, To: StatusRunning, Event: "reserve_summary_hook", SummaryStep: summaryStep(SummarySourcePostExitIssued, "")})

			rs.applyTransition(&Transition{Seq: 5, StageID: "s", From: StatusRunning, To: exit.to, Event: exit.event})

			want := &SummaryCorrections{PostExit: 1, Reason: "missing", ReservedSeq: 2, PostExitSeq: 2}
			if got := rs.Stages["s"].Summary; !reflect.DeepEqual(got, want) {
				t.Fatalf("after the exit summary = %+v, want the same correction reserved and not launched: %+v", got, want)
			}
		})
	}
}

// A Stop hook reminder is recorded inside the live author's turn: it must not drop the launch mark of the
// correction that author is running.
func TestSummaryCorrections_HookReminderKeepsTheIssuedMark(t *testing.T) {
	rs := NewRunState([]string{"s"})
	rs.applyTransition(&Transition{Seq: 1, StageID: "s", From: StatusPending, To: StatusRunning, Event: "start_run"})
	rs.applyTransition(&Transition{Seq: 2, StageID: "s", From: StatusRunning, To: StatusRetrying, Event: "schedule_retry", SummaryStep: summaryStep(SummarySourcePostExit, "missing")})
	rs.applyTransition(&Transition{Seq: 3, StageID: "s", From: StatusRetrying, To: StatusRunning, Event: "resume_after_retry"})
	rs.applyTransition(&Transition{Seq: 4, StageID: "s", From: StatusRunning, To: StatusRunning, Event: "reserve_summary_hook", SummaryStep: summaryStep(SummarySourcePostExitIssued, "")})
	rs.applyTransition(&Transition{Seq: 5, StageID: "s", From: StatusRunning, To: StatusRunning, Event: "reserve_summary_hook", SummaryStep: summaryStep(SummarySourceStopHook, "empty")})

	want := &SummaryCorrections{PostExit: 1, Hook: 1, Reason: "empty", ReservedSeq: 5, PostExitSeq: 2, Issued: true}
	if got := rs.Stages["s"].Summary; !reflect.DeepEqual(got, want) {
		t.Fatalf("after the hook reminder summary = %+v, want %+v", got, want)
	}
}

// The launch mark replays from the journal exactly as the live projection derived it, including its drop.
func TestSummaryCorrections_IssuedMarkReplaysLikeLive(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"s"})
	if err != nil {
		t.Fatal(err)
	}
	apply := func(from, to StageStatus, event string, step *SummaryStep) {
		t.Helper()
		if err := store.Apply(&Transition{StageID: "s", From: from, To: to, Event: event, SummaryStep: step}); err != nil {
			t.Fatal(err)
		}
	}
	apply(StatusPending, StatusRunning, "start_run", nil)
	apply(StatusRunning, StatusRetrying, "schedule_retry", summaryStep(SummarySourcePostExit, "missing"))
	apply(StatusRetrying, StatusRunning, "resume_after_retry", nil)
	apply(StatusRunning, StatusRunning, "reserve_summary_hook", summaryStep(SummarySourcePostExitIssued, ""))
	issued := store.Snapshot().Stages["s"].Summary
	apply(StatusRunning, StatusRetrying, "schedule_retry", nil)
	dropped := store.Snapshot().Stages["s"].Summary
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if !issued.Issued || dropped.Issued {
		t.Fatalf("live marks: issued=%+v dropped=%+v, want the mark set and then dropped", issued, dropped)
	}

	rs, err := LoadRunState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := rs.Stages["s"].Summary; !reflect.DeepEqual(got, dropped) {
		t.Fatalf("replayed summary = %+v, live = %+v", got, dropped)
	}
}

// A Stop hook reminder inside a post-exit correction moves ReservedSeq to itself; the correction keeps its own seq.
func TestSummaryCorrections_HookReminderKeepsThePostExitSeq(t *testing.T) {
	rs := NewRunState([]string{"s"})
	rs.applyTransition(&Transition{Seq: 1, StageID: "s", From: StatusPending, To: StatusRunning, Event: "start_run"})
	rs.applyTransition(&Transition{Seq: 2, StageID: "s", From: StatusRunning, To: StatusRetrying, Event: "schedule_retry", SummaryStep: summaryStep(SummarySourcePostExit, "missing")})
	rs.applyTransition(&Transition{Seq: 3, StageID: "s", From: StatusRetrying, To: StatusRunning, Event: "resume_after_retry"})
	rs.applyTransition(&Transition{Seq: 4, StageID: "s", From: StatusRunning, To: StatusRunning, Event: "reserve_summary_hook", SummaryStep: summaryStep(SummarySourceStopHook, "empty")})

	got := rs.Stages["s"].Summary
	if got.ReservedSeq != 4 || got.PostExitSeq != 2 {
		t.Fatalf("summary = %+v, want the hook reservation 4 and the correction 2", got)
	}
}
