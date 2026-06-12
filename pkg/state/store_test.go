package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpen_EmptyRunDir(t *testing.T) {
	dir := t.TempDir()
	stages := []string{"a", "b"}

	store, err := Open(dir, stages)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	if got := store.Get("a"); got != StatusPending {
		t.Errorf("Get(a) = %q, want %q", got, StatusPending)
	}
	if got := store.Get("b"); got != StatusPending {
		t.Errorf("Get(b) = %q, want %q", got, StatusPending)
	}

	if _, err := os.Stat(filepath.Join(dir, "events.jsonl")); err != nil {
		t.Errorf("events.jsonl not created: %v", err)
	}
}

func TestApply_AppendsToEventsLog(t *testing.T) {
	dir := t.TempDir()
	store, _ := Open(dir, []string{"a"})
	defer store.Close()

	tr := Transition{
		StageID: "a",
		From:    StatusPending,
		To:      StatusPlanning,
		Event:   "start_planning",
	}
	if err := store.Apply(tr); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got := store.Get("a"); got != StatusPlanning {
		t.Errorf("Get(a) after Apply = %q, want %q", got, StatusPlanning)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("events.jsonl lines = %d, want 1", len(lines))
	}

	var got Transition
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("unmarshal line: %v", err)
	}
	if got.Seq != 1 {
		t.Errorf("Seq = %d, want 1", got.Seq)
	}
	if got.StageID != "a" || got.To != StatusPlanning {
		t.Errorf("transition mismatch: %+v", got)
	}
}

func TestApply_RejectsWrongFrom(t *testing.T) {
	dir := t.TempDir()
	store, _ := Open(dir, []string{"a"})
	defer store.Close()

	tr := Transition{StageID: "a", From: StatusRunning, To: StatusDone, Event: "complete"}
	err := store.Apply(tr)
	if err == nil {
		t.Fatal("Apply with wrong From: want error, got nil")
	}
}

func TestOpen_ReplaysExistingEvents(t *testing.T) {
	dir := t.TempDir()

	store1, _ := Open(dir, []string{"a"})
	_ = store1.Apply(Transition{StageID: "a", From: StatusPending, To: StatusPlanning, Event: "start_planning"})
	_ = store1.Apply(Transition{StageID: "a", From: StatusPlanning, To: StatusAwaitingApproval, Event: "plan_ready"})
	store1.Close()

	store2, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store2.Close()

	if got := store2.Get("a"); got != StatusAwaitingApproval {
		t.Errorf("after replay Get(a) = %q, want %q", got, StatusAwaitingApproval)
	}

	if err := store2.Apply(Transition{StageID: "a", From: StatusAwaitingApproval, To: StatusReady, Event: "approve"}); err != nil {
		t.Fatalf("Apply after replay: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines after replay+Apply = %d, want 3", len(lines))
	}
	var last Transition
	_ = json.Unmarshal([]byte(lines[2]), &last)
	if last.Seq != 3 {
		t.Errorf("last Seq = %d, want 3", last.Seq)
	}
}

func TestOpen_TruncatesPartialLine(t *testing.T) {
	dir := t.TempDir()

	store1, _ := Open(dir, []string{"a"})
	_ = store1.Apply(Transition{StageID: "a", From: StatusPending, To: StatusPlanning, Event: "start_planning"})
	store1.Close()

	// дописываем битую строку (имитация crash посреди записи)
	f, _ := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0644)
	f.WriteString(`{"seq":2,"stage_id":"a","from":"plan`)
	f.Close()

	store2, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store2.Close()

	// файл должен быть обрезан до целой первой строки
	data, _ := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if strings.Contains(string(data), `"from":"plan`) {
		t.Error("partial line not truncated")
	}

	// новый Apply должен идти с Seq=2, не Seq=3
	_ = store2.Apply(Transition{StageID: "a", From: StatusPlanning, To: StatusAwaitingApproval, Event: "plan_ready"})
	lines := strings.Split(strings.TrimRight(string(mustReadFile(t, dir)), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(lines))
	}
}

func mustReadFile(t *testing.T, dir string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return data
}

func TestApply_WritesSnapshotJSON(t *testing.T) {
	dir := t.TempDir()
	store, _ := Open(dir, []string{"a"})
	defer store.Close()

	_ = store.Apply(Transition{StageID: "a", From: StatusPending, To: StatusPlanning, Event: "start_planning"})

	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatalf("read state.json: %v", err)
	}
	if !strings.Contains(string(data), `"status": "planning"`) {
		t.Errorf("state.json not updated, content: %s", data)
	}
}

func TestOpen_LegacyStateJSONFallback(t *testing.T) {
	dir := t.TempDir()

	legacy := &RunState{
		FlowName:   "old-flow",
		StartedAt:  time.Now(),
		StageOrder: []string{"a", "b"},
		Stages: map[string]StageState{
			"a": {Status: StatusDone, UpdatedAt: time.Now()},
			"b": {Status: StatusAwaitingApproval, UpdatedAt: time.Now()},
		},
	}
	data, _ := json.MarshalIndent(legacy, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, "state.json"), data, 0644)

	store, err := Open(dir, []string{"a", "b"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	if got := store.Get("a"); got != StatusDone {
		t.Errorf("Get(a) = %q, want %q", got, StatusDone)
	}
	if got := store.Get("b"); got != StatusAwaitingApproval {
		t.Errorf("Get(b) = %q, want %q", got, StatusAwaitingApproval)
	}

	evData, _ := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if !strings.Contains(string(evData), `"event":"legacy_load"`) {
		t.Errorf("legacy_load event missing, content: %s", evData)
	}
}

func TestSnapshot_ReturnsCopy(t *testing.T) {
	dir := t.TempDir()
	store, _ := Open(dir, []string{"a", "b"})
	defer store.Close()

	_ = store.Apply(Transition{StageID: "a", From: StatusPending, To: StatusPlanning, Event: "start_planning"})

	snap := store.Snapshot()
	if snap.Stages["a"].Status != StatusPlanning {
		t.Errorf("snapshot status = %q, want %q", snap.Stages["a"].Status, StatusPlanning)
	}

	snap.Stages["a"] = StageState{Status: StatusDone}
	if store.Get("a") != StatusPlanning {
		t.Error("Snapshot leaked reference: original mutated")
	}
}

func TestApply_CrashAfterFsync_Recovers(t *testing.T) {
	dir := t.TempDir()
	store1, _ := Open(dir, []string{"a"})

	SetApplyHook(func(tr Transition) {
		if tr.Seq == 2 {
			store1.eventsLog.Close()
			panic("simulated crash")
		}
	})
	defer SetApplyHook(nil)

	_ = store1.Apply(Transition{StageID: "a", From: StatusPending, To: StatusPlanning, Event: "start_planning"})
	func() {
		defer func() { _ = recover() }()
		_ = store1.Apply(Transition{StageID: "a", From: StatusPlanning, To: StatusAwaitingApproval, Event: "plan_ready"})
	}()

	// Open again — should recover state from events.jsonl
	store2, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store2.Close()
	if got := store2.Get("a"); got != StatusAwaitingApproval {
		t.Errorf("after crash recovery Get(a) = %q, want %q", got, StatusAwaitingApproval)
	}
}
