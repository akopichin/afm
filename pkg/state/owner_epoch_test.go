package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOwnerEpoch_PreviousOwnerLiveReplayAndCopies(t *testing.T) {
	s, dir := openRunningStore(t, "a")
	first := s.Stage("a").OwnerEpoch
	revise := mustApply(t, s, Transition{StageID: "a", From: StatusRunning, To: StatusRevising, Event: "revise"})
	got := s.Stage("a")
	if got.OwnerEpoch != revise.Seq || got.PrevOwnerEpoch != first {
		t.Fatalf("revise owners=%+v", got)
	}
	paused := mustApply(t, s, Transition{StageID: "a", From: StatusRevising, To: StatusPaused, Event: "pause"})
	continued := mustApply(t, s, Transition{StageID: "a", From: StatusPaused, To: StatusRevising, Event: "continue"})
	mustApply(t, s, Transition{StageID: "a", From: StatusRevising, To: StatusRunning, Event: "test_same_owner", SameOwner: true})
	want := s.Stage("a")
	if want.PrevOwnerEpoch != paused.Seq || want.OwnerEpoch != continued.Seq {
		t.Fatalf("owners after same-owner transition=%+v", want)
	}
	snapshot := s.Snapshot()
	snapshot.SetStageStatusAt("a", StatusRetrying, time.Now())
	if st := snapshot.Stages["a"]; st.OwnerEpoch != want.OwnerEpoch || st.PrevOwnerEpoch != want.PrevOwnerEpoch {
		t.Fatalf("SetStageStatusAt lost owners: %+v", st)
	}
	snapshot.Stages["a"] = StageState{}
	if s.Stage("a") != want {
		t.Fatal("snapshot mutation changed store")
	}
	if err := os.Remove(filepath.Join(dir, "state.json")); err != nil {
		t.Fatal(err)
	}
	replay, err := LoadRunState(dir)
	if err != nil {
		t.Fatal(err)
	}
	want.UpdatedAt = want.UpdatedAt.UTC()
	if replay.Stages["a"] != want {
		t.Fatalf("replay=%+v want=%+v", replay.Stages["a"], want)
	}
	s.Close()
	reopened, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.Stage("a") != want {
		t.Fatal("Open replay differs")
	}
}

func TestOwnerEpoch_LegacyJournalDerivesPreviousOwner(t *testing.T) {
	dir := t.TempDir()
	old := `{"seq":1,"time":"2026-01-01T00:00:00Z","stage_id":"a","from":"pending","to":"running","event":"start_run"}` + "\n" +
		`{"seq":2,"time":"2026-01-01T00:00:01Z","stage_id":"a","from":"running","to":"revising","event":"revise"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	rs, err := LoadRunState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := rs.Stages["a"]; got.OwnerEpoch != 2 || got.PrevOwnerEpoch != 1 || got.SummaryRecoveryUsed != 0 {
		t.Fatalf("legacy state=%+v", got)
	}
}
