package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// legacyFixtureLog — фиксированный лог legacy-формата (без control-записей), используемый и до, и после
// T2.1 как golden-источник для TestParse_LegacyLogUnchanged.
const legacyFixtureLog = `{"seq":1,"time":"2026-01-01T10:00:00Z","stage_id":"a","from":"pending","to":"planning","event":"start_planning"}
{"seq":2,"time":"2026-01-01T10:00:01Z","stage_id":"a","from":"planning","to":"awaiting_approval","event":"plan_ready"}
{"seq":3,"time":"2026-01-01T10:00:02Z","stage_id":"a","from":"awaiting_approval","to":"ready","event":"approve"}
{"seq":4,"time":"2026-01-01T10:00:03Z","stage_id":"a","from":"ready","to":"running","event":"start_run"}
{"seq":5,"time":"2026-01-01T10:00:04Z","stage_id":"a","from":"running","to":"done","event":"complete"}
`

func TestParse_LegacyLogUnchanged(t *testing.T) {
	rs := NewRunState([]string{"a"})
	res := parseEventLog([]byte(legacyFixtureLog), rs)

	if res.corrupted || res.unsupported != nil {
		t.Fatalf("legacy log must parse cleanly: corrupted=%v unsupported=%v", res.corrupted, res.unsupported)
	}
	if res.lastSeq != 5 {
		t.Fatalf("lastSeq = %d, want 5", res.lastSeq)
	}
	if len(res.history) != 5 {
		t.Fatalf("history len = %d, want 5", len(res.history))
	}
	if got := rs.Stages["a"].Status; got != StatusDone {
		t.Fatalf("stage a status = %q, want done", got)
	}
}

func TestParse_ControlRecordNotTreatedAsTransition(t *testing.T) {
	rs := NewRunState([]string{"a"})
	log := `{"seq":1,"time":"2026-01-01T10:00:00Z","stage_id":"a","from":"pending","to":"planning","event":"start_planning"}
{"seq":2,"time":"2026-01-01T10:00:01Z","kind":"policy_set","schema_version":1,"operation_id":"op-1","actor":{"kind":"user"},"target":{"stage_id":"a"},"payload":{}}
`
	res := parseEventLog([]byte(log), rs)
	if res.corrupted || res.unsupported != nil {
		t.Fatalf("mixed log must parse cleanly: corrupted=%v unsupported=%v", res.corrupted, res.unsupported)
	}
	// Control-запись не должна попасть в History() стадий: ровно один переход, несмотря на две строки.
	if len(res.history) != 1 {
		t.Fatalf("history len = %d, want 1 (control record must not appear in stage History)", len(res.history))
	}
	// Статус стадии не должен измениться control-записью.
	if got := rs.Stages["a"].Status; got != StatusPlanning {
		t.Fatalf("stage a status = %q, want planning (unchanged by the control record)", got)
	}
}

func TestParse_UnknownKindFailsClosed(t *testing.T) {
	rs := NewRunState([]string{"a"})
	log := `{"seq":1,"time":"2026-01-01T10:00:00Z","kind":"something_from_the_future","schema_version":1,"actor":{"kind":"system"}}
`
	res := parseEventLog([]byte(log), rs)
	if !errors.Is(res.unsupported, ErrUnsupportedRecord) {
		t.Fatalf("unsupported = %v, want ErrUnsupportedRecord", res.unsupported)
	}
	if res.corrupted {
		t.Fatal("unknown kind must fail closed, not be treated as corrupted/quarantined")
	}
	// Состояние не должно отражать частичный разбор.
	if len(res.history) != 0 {
		t.Fatalf("history len = %d, want 0 (no partial state on fail-closed)", len(res.history))
	}
}

func TestParse_UnsupportedSchemaVersionFailsClosed(t *testing.T) {
	rs := NewRunState([]string{"a"})
	log := `{"seq":1,"time":"2026-01-01T10:00:00Z","kind":"task_created","schema_version":2,"actor":{"kind":"system"}}
`
	res := parseEventLog([]byte(log), rs)
	if !errors.Is(res.unsupported, ErrUnsupportedRecord) {
		t.Fatalf("unsupported = %v, want ErrUnsupportedRecord", res.unsupported)
	}
	if res.corrupted {
		t.Fatal("unsupported schema_version must fail closed, not be treated as corrupted/quarantined")
	}
}

func TestParse_MixedLog(t *testing.T) {
	rs := NewRunState([]string{"a", "b"})
	log := `{"seq":1,"time":"2026-01-01T10:00:00Z","stage_id":"a","from":"pending","to":"planning","event":"start_planning"}
{"seq":2,"time":"2026-01-01T10:00:01Z","kind":"policy_set","schema_version":1,"actor":{"kind":"user"},"target":{"stage_id":"a"},"payload":{}}
{"seq":3,"time":"2026-01-01T10:00:02Z","stage_id":"b","from":"pending","to":"planning","event":"start_planning"}
{"seq":4,"time":"2026-01-01T10:00:03Z","kind":"quota_reserved","schema_version":1,"actor":{"kind":"assistant"},"target":{"task_id":"t-1"},"payload":{"amount":1}}
`
	res := parseEventLog([]byte(log), rs)
	if res.corrupted || res.unsupported != nil {
		t.Fatalf("mixed log must parse cleanly: corrupted=%v unsupported=%v", res.corrupted, res.unsupported)
	}
	if res.lastSeq != 4 {
		t.Fatalf("lastSeq = %d, want 4 (last_seq accounts for both legacy and control records)", res.lastSeq)
	}
	if len(res.history) != 2 {
		t.Fatalf("history len = %d, want 2 (only the two stage transitions)", len(res.history))
	}
}

// TestParse_ControlRecordWithEmptyStageIdDoesNotHitRunEventPath is a direct regression test for the
// landmine described in dev-plan.md and AGENTS.md: before T2.1, parseEventLog distinguished run-level
// events purely by `stage_id == "" && isRunEvent(event)`. A control record has no top-level stage_id
// (it lives under target.stage_id) and no top-level "event" field, so under the OLD code it would have been
// silently decoded as a Transition with StageID=="" and Event=="" — isRunEvent("") is false, so it would
// fall through to `rs.SetStageStatusAt("", "", t)`, fabricating a bogus stage entry with id "".
func TestParse_ControlRecordWithEmptyStageIdDoesNotHitRunEventPath(t *testing.T) {
	rs := NewRunState([]string{"a"})
	log := `{"seq":1,"time":"2026-01-01T10:00:00Z","kind":"policy_set","schema_version":1,"actor":{"kind":"system"},"target":{}}
`
	res := parseEventLog([]byte(log), rs)
	if res.corrupted || res.unsupported != nil {
		t.Fatalf("control record must parse cleanly: corrupted=%v unsupported=%v", res.corrupted, res.unsupported)
	}
	if _, ok := rs.Stages[""]; ok {
		t.Fatal(`rs.Stages contains a bogus "" entry — the control record was misparsed into the run-event/stage path`)
	}
	if len(rs.Stages) != 1 {
		t.Fatalf("rs.Stages = %+v, want exactly the configured stage %q", rs.Stages, "a")
	}
	if len(res.history) != 0 {
		t.Fatalf("history len = %d, want 0", len(res.history))
	}
	if res.lastSeq != 1 {
		t.Fatalf("lastSeq = %d, want 1", res.lastSeq)
	}
}

// Sanity check that decodeLine itself round-trips a Control record's fields (time, operation id, actor,
// target, payload) — the higher level tests above only check parseEventLog's side effects.
func TestDecodeLine_ControlFieldsRoundTrip(t *testing.T) {
	line := []byte(`{"seq":7,"time":"2026-02-03T04:05:06Z","kind":"task_created","schema_version":1,"operation_id":"op-42","actor":{"kind":"user","id":"u1"},"target":{"task_id":"t-1","stage_id":"a"},"payload":{"x":1}}`)
	dl, unsupported, err := decodeLine(line)
	if err != nil || unsupported {
		t.Fatalf("decodeLine: err=%v unsupported=%v", err, unsupported)
	}
	if dl.control == nil || dl.transition != nil {
		t.Fatalf("decodeLine = %+v, want a control record only", dl)
	}
	c := dl.control
	wantTime := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	if c.Seq != 7 || !c.Time.Equal(wantTime) || c.OperationID != "op-42" {
		t.Fatalf("control envelope mismatch: %+v", c)
	}
	if c.Actor.Kind != "user" || c.Actor.ID != "u1" {
		t.Fatalf("actor mismatch: %+v", c.Actor)
	}
	if c.Target.TaskID != "t-1" || c.Target.StageID != "a" {
		t.Fatalf("target mismatch: %+v", c.Target)
	}
}

// ---- T2.2: AppendControl ----

func testControl(opID string, stageID string) *Control {
	return &Control{
		Kind:          "policy_set",
		SchemaVersion: ControlSchemaVersion,
		OperationID:   opID,
		Actor:         Actor{Kind: "user"},
		Target:        Target{StageID: stageID},
		Payload:       json.RawMessage(`{}`),
	}
}

func TestAppendControl_AssignsMonotonicSeqSharedWithTransitions(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	if err := store.Apply(&Transition{StageID: "a", From: StatusPending, To: StatusPlanning, Event: "start_planning"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	c1 := testControl("op-1", "a")
	if err := store.AppendControl(c1); err != nil {
		t.Fatalf("AppendControl: %v", err)
	}
	if c1.Seq != 2 {
		t.Fatalf("AppendControl Seq = %d, want 2 (shared counter with the prior Apply)", c1.Seq)
	}

	if err := store.Apply(&Transition{StageID: "a", From: StatusPlanning, To: StatusAwaitingApproval, Event: "plan_ready"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Re-check via History: the second transition must carry Seq 3, strictly after the control record.
	hist, _ := store.History()
	if len(hist) != 2 || hist[1].Seq != 3 {
		t.Fatalf("history = %+v, want second transition Seq=3 (strictly after AppendControl's Seq=2)", hist)
	}

	c2 := testControl("op-2", "a")
	if err := store.AppendControl(c2); err != nil {
		t.Fatalf("AppendControl: %v", err)
	}
	if c2.Seq != 4 {
		t.Fatalf("second AppendControl Seq = %d, want 4", c2.Seq)
	}
}

// TestAppendControl_FsyncsBeforeReturn mirrors TestApply_CrashAfterFsync_Recovers: a simulated crash (close
// fd + panic) inside the hook, which AppendControl calls AFTER Write+Sync, must not lose the record — on
// reopen it is still there.
func TestAppendControl_FsyncsBeforeReturn(t *testing.T) {
	dir := t.TempDir()
	store1, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	crashed := false
	SetAppendControlHook(func(c Control) {
		crashed = true
		store1.eventsLog.Close()
		store1.lock.Unlock()
		panic("simulated crash")
	})
	defer SetAppendControlHook(nil)

	func() {
		defer func() { _ = recover() }()
		_ = store1.AppendControl(testControl("op-1", "a"))
	}()
	if !crashed {
		t.Fatal("hook did not fire")
	}

	rs, err := LoadRunState(dir)
	if err != nil {
		t.Fatalf("LoadRunState after crash: %v", err)
	}
	if rs.LastSeq != 1 {
		t.Fatalf("after crash+reopen LastSeq = %d, want 1 (the control record survived the simulated crash because fsync ran before the hook)", rs.LastSeq)
	}
}

// TestAppendControl_SnapshotWriteFailureIsNotFatal makes the state.json.tmp path a directory so os.Create
// fails regardless of uid (works even when the test runs as root, unlike a chmod-based approach) — the
// authoritative log write must still succeed and AppendControl must still return nil.
func TestAppendControl_SnapshotWriteFailureIsNotFatal(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	if err := os.Mkdir(filepath.Join(dir, "state.json.tmp"), 0755); err != nil {
		t.Fatalf("mkdir state.json.tmp: %v", err)
	}

	c := testControl("op-1", "a")
	if err := store.AppendControl(c); err != nil {
		t.Fatalf("AppendControl: %v, want nil (snapshot write failure must not be fatal)", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("events.jsonl is empty — the authoritative record must still be durably written")
	}
}

// TestAppendControl_StorageErrorIsTyped closes the underlying file descriptor directly (bypassing Store's
// own Close, which the Store doesn't know happened) to simulate a real OS-level write failure, as opposed to
// the "store is closed" case covered separately below.
func TestAppendControl_StorageErrorIsTyped(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { store.lock.Unlock() }() // eventsLog is already closed below; avoid double-close via Close()

	store.eventsLog.Close()

	err = store.AppendControl(testControl("op-1", "a"))
	if err == nil {
		t.Fatal("AppendControl on a failed log file: want error, got nil")
	}
	var se *StorageError
	if !errors.As(err, &se) {
		t.Fatalf("AppendControl error = %v (%T), want *StorageError", err, err)
	}
}

func TestAppendControl_AfterCloseReturnsError(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	err = store.AppendControl(testControl("op-1", "a"))
	if err == nil {
		t.Fatal("AppendControl after Close: want error, got nil")
	}
	if err.Error() == "" {
		t.Fatal("AppendControl after Close: error must not be empty/nil-pointer-deref masquerading as success")
	}
}
