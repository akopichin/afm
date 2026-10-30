package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustApply(t *testing.T, s *Store, tr Transition) Transition {
	t.Helper()
	if err := s.Apply(&tr); err != nil {
		t.Fatalf("Apply %s: %v", tr.Event, err)
	}
	return tr
}

func openRunningStore(t *testing.T, stages ...string) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir, stages)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for _, id := range stages {
		mustApply(t, s, Transition{StageID: id, From: StatusPending, To: StatusRunning, Event: "start_run"})
	}
	return s, dir
}

func reserve(t *testing.T, s *Store, id string, used int, reason string, epoch *uint64) (Transition, error) {
	t.Helper()
	tr := Transition{
		StageID: id, From: StatusRunning, To: StatusRunning, Event: "summary_recover",
		SummaryRecovery: &SummaryRecovery{Used: used, Reason: reason}, ExpectedEpoch: epoch,
	}
	return tr, s.Apply(&tr)
}

func TestSummaryRecovery_ReducerLiveReplayAndCache(t *testing.T) {
	s, dir := openRunningStore(t, "a")
	epoch := s.Stage("a").OwnerEpoch
	tr, err := reserve(t, s, "a", 1, "missing", &epoch)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Stage("a"); got.SummaryRecoveryUsed != 1 || got.SummaryRecoveryReason != "missing" || got.OwnerEpoch != tr.Seq {
		t.Fatalf("live state = %+v, want used=1 reason=missing epoch=%d", got, tr.Seq)
	}
	s.Close()

	// state.json — только кэш: replay из events.jsonl обязан дать то же состояние.
	if err := os.Remove(filepath.Join(dir, "state.json")); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadRunState(dir)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for name, got := range map[string]StageState{"LoadRunState": loaded.Stages["a"], "Open": reopened.Stage("a")} {
		if got.SummaryRecoveryUsed != 1 || got.SummaryRecoveryReason != "missing" || got.OwnerEpoch != tr.Seq {
			t.Errorf("%s = %+v, want used=1 reason=missing epoch=%d", name, got, tr.Seq)
		}
	}
}

func TestSummaryRecovery_OldJournalWithoutPayloadIsZero(t *testing.T) {
	dir := t.TempDir()
	old := `{"seq":1,"time":"2026-01-01T00:00:00Z","stage_id":"a","from":"pending","to":"running","event":"start_run"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	rs, err := LoadRunState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := rs.Stages["a"]; got.Status != StatusRunning || got.SummaryRecoveryUsed != 0 || got.SummaryRecoveryReason != "" {
		t.Fatalf("old journal state = %+v, want running with zero budget", got)
	}
}

func TestSummaryRecovery_PayloadSurvivesOrdinaryTransition(t *testing.T) {
	s, _ := openRunningStore(t, "a")
	if _, err := reserve(t, s, "a", 2, "empty", nil); err != nil {
		t.Fatal(err)
	}
	// Pause/Continue и transport retry не обнуляют бюджет (SetStageStatusAt сохраняет поля).
	mustApply(t, s, Transition{StageID: "a", From: StatusRunning, To: StatusPaused, Event: "pause"})
	mustApply(t, s, Transition{StageID: "a", From: StatusPaused, To: StatusRunning, Event: "continue"})
	mustApply(t, s, Transition{StageID: "a", From: StatusRunning, To: StatusRetrying, Event: "schedule_retry", SameOwner: true})
	got := s.Stage("a")
	if got.SummaryRecoveryUsed != 2 || got.SummaryRecoveryReason != "empty" || got.PausedFrom != StatusRunning {
		t.Fatalf("state = %+v, want used=2 reason=empty and PausedFrom preserved", got)
	}
}

func TestSummaryRecovery_ResetPayloadClearsBudgetAtomically(t *testing.T) {
	s, dir := openRunningStore(t, "a")
	if _, err := reserve(t, s, "a", 1, "missing", nil); err != nil {
		t.Fatal(err)
	}
	mustApply(t, s, Transition{StageID: "a", From: StatusRunning, To: StatusRevising, Event: "revise", SummaryRecovery: &SummaryRecovery{}})
	if got := s.Stage("a"); got.SummaryRecoveryUsed != 0 || got.SummaryRecoveryReason != "" {
		t.Fatalf("after reset = %+v, want zero budget", got)
	}
	rs, err := LoadRunState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := rs.Stages["a"]; got.SummaryRecoveryUsed != 0 {
		t.Fatalf("replayed reset = %+v, want zero budget", got)
	}
}

func TestOwnerEpoch_SameOwnerKeepsAndOthersChange(t *testing.T) {
	s, _ := openRunningStore(t, "a")
	epoch := s.Stage("a").OwnerEpoch

	mustApply(t, s, Transition{StageID: "a", From: StatusRunning, To: StatusAwaitingUserInput, Event: "ask_user", SameOwner: true})
	mustApply(t, s, Transition{StageID: "a", From: StatusAwaitingUserInput, To: StatusRunning, Event: "user_answered", SameOwner: true})
	if got := s.Stage("a").OwnerEpoch; got != epoch {
		t.Fatalf("question→answer changed the epoch: %d → %d", epoch, got)
	}

	mustApply(t, s, Transition{StageID: "a", From: StatusRunning, To: StatusPaused, Event: "pause"})
	mustApply(t, s, Transition{StageID: "a", From: StatusPaused, To: StatusRunning, Event: "continue"})
	if got := s.Stage("a").OwnerEpoch; got == epoch {
		t.Fatal("Pause→Continue must change the owner epoch even though the status is running again (ABA)")
	}
}

func TestOwnerEpoch_SiblingStageDoesNotInvalidate(t *testing.T) {
	s, _ := openRunningStore(t, "a", "b")
	epoch := s.Stage("a").OwnerEpoch
	mustApply(t, s, Transition{StageID: "b", From: StatusRunning, To: StatusPaused, Event: "pause"})
	if _, err := reserve(t, s, "a", 1, "missing", &epoch); err != nil {
		t.Fatalf("a sibling transition invalidated stage a: %v", err)
	}
}

func TestOwnerEpoch_StaleGuardIsConcurrentChangeAndWritesNothing(t *testing.T) {
	s, dir := openRunningStore(t, "a")
	stale := s.Stage("a").OwnerEpoch
	mustApply(t, s, Transition{StageID: "a", From: StatusRunning, To: StatusPaused, Event: "pause"})
	mustApply(t, s, Transition{StageID: "a", From: StatusPaused, To: StatusRunning, Event: "continue"})
	before, _ := os.ReadFile(filepath.Join(dir, "events.jsonl"))

	_, err := reserve(t, s, "a", 1, "missing", &stale)
	if !errors.Is(err, ErrConcurrentChange) {
		t.Fatalf("err = %v, want ErrConcurrentChange", err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if string(before) != string(after) {
		t.Fatal("a stale-owner transition must not reach the journal")
	}
	if got := s.Stage("a").SummaryRecoveryUsed; got != 0 {
		t.Fatalf("budget spent by a lost CAS: used=%d", got)
	}
}

func TestOwnerEpoch_RuntimeGuardIsNotJournaled(t *testing.T) {
	s, dir := openRunningStore(t, "a")
	epoch := s.Stage("a").OwnerEpoch
	if _, err := reserve(t, s, "a", 1, "missing", &epoch); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	last := data[strings.LastIndex(strings.TrimRight(string(data), "\n"), "\n")+1:]
	var raw map[string]any
	if err := json.Unmarshal(last, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["ExpectedEpoch"]; ok {
		t.Fatalf("runtime guard leaked into the journal: %s", last)
	}
	if sr, _ := raw["summary_recovery"].(map[string]any); sr["used"] != float64(1) || sr["reason"] != "missing" {
		t.Fatalf("journal payload = %v, want used=1 reason=missing", raw["summary_recovery"])
	}
}

func TestStage_ReturnsValueCopy(t *testing.T) {
	s, _ := openRunningStore(t, "a")
	if _, err := reserve(t, s, "a", 1, "missing", nil); err != nil {
		t.Fatal(err)
	}
	snap := s.Snapshot()
	st := snap.Stages["a"]
	st.SummaryRecoveryUsed = 99
	snap.Stages["a"] = st
	if got := s.Stage("a").SummaryRecoveryUsed; got != 1 {
		t.Fatalf("Snapshot mutation leaked into the store: used=%d", got)
	}
}
