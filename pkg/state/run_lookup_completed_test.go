package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// mkRun создаёт run-директорию name под base и для каждой стадии из statuses
// проводит настоящий переход pending -> statuses[id] через Store.Apply — так
// events.jsonl получается ровно таким же, какой пишет живой прогон. Стадии,
// отсутствующие в statuses, в лог вообще не попадают (как если бы стадия
// никогда не запускалась) — это поведение проверяет
// TestFindLatestCompletedRunDir_DonePlusNeverStartedPendingNotCompleted.
func mkRun(t *testing.T, base, name string, statuses map[string]StageStatus) string {
	t.Helper()
	runDir := filepath.Join(base, name)
	ids := make([]string, 0, len(statuses))
	for id := range statuses {
		ids = append(ids, id)
	}
	store, err := Open(runDir, ids)
	if err != nil {
		t.Fatalf("mkRun: Open: %v", err)
	}
	for id, status := range statuses {
		if status == StatusPending {
			continue // pending — статус по умолчанию, событие не нужно
		}
		tr := Transition{StageID: id, From: StatusPending, To: status, Event: "test"}
		if err := store.Apply(&tr); err != nil {
			t.Fatalf("mkRun: Apply(%s -> %s): %v", id, status, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("mkRun: Close: %v", err)
	}
	return runDir
}

// mkEmptyRun создаёт run-директорию с пустым events.jsonl — "активный, но ещё
// ничего не сделавший" run: ни одна стадия не должна засчитываться завершённой.
func mkEmptyRun(t *testing.T, base, name string) string {
	t.Helper()
	runDir := filepath.Join(base, name)
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatalf("mkEmptyRun: mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "events.jsonl"), nil, 0644); err != nil {
		t.Fatalf("mkEmptyRun: write events.jsonl: %v", err)
	}
	return runDir
}

// mkCorruptRun создаёт run-директорию с битой ПОЛНОЙ строкой в середине
// events.jsonl (валидная строка после неё есть) — тот же паттерн порчи, что и
// TestOpen_MidCorruptionQuarantines в store_replay_test.go, но записанный
// напрямую в файл: тест проверяет read-only путь (LoadRunState через
// FindLatestCompletedRunDir), который порченный файл НЕ трогает и НЕ
// карантинирует.
func mkCorruptRun(t *testing.T, base, name string) string {
	t.Helper()
	runDir := filepath.Join(base, name)
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatalf("mkCorruptRun: mkdir: %v", err)
	}
	line1 := `{"seq":1,"stage_id":"a","from":"pending","to":"planning","event":"x"}` + "\n"
	bad := `NOT JSON AT ALL` + "\n"
	line3 := `{"seq":2,"stage_id":"a","from":"planning","to":"done","event":"y"}` + "\n"
	data := []byte(line1 + bad + line3)
	if err := os.WriteFile(filepath.Join(runDir, "events.jsonl"), data, 0644); err != nil {
		t.Fatalf("mkCorruptRun: write events.jsonl: %v", err)
	}
	return runDir
}

func TestFindLatestCompletedRunDir_ExactSetAllDone(t *testing.T) {
	base := t.TempDir()
	mkRun(t, base, "flow-20260101-000000-aaaa", map[string]StageStatus{"a": StatusDone, "b": StatusDone})
	got, err := FindLatestCompletedRunDir(base, "flow", []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "flow-20260101-000000-aaaa" {
		t.Fatalf("got %s", got)
	}
}

func TestFindLatestCompletedRunDir_EmptyActiveRunNotCompleted(t *testing.T) {
	base := t.TempDir()
	mkRun(t, base, "flow-20260101-000000-aaaa", map[string]StageStatus{"a": StatusDone, "b": StatusDone})
	mkEmptyRun(t, base, "flow-20260301-000000-cccc") // newest, events.jsonl empty
	got, err := FindLatestCompletedRunDir(base, "flow", []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "flow-20260101-000000-aaaa" {
		t.Fatalf("empty active run must be skipped, got %s", got)
	}
}

func TestFindLatestCompletedRunDir_DonePlusNeverStartedPendingNotCompleted(t *testing.T) {
	base := t.TempDir()
	// newest run: only "a" ever transitioned (to done); "b" never emitted an event
	mkRun(t, base, "flow-20260301-000000-cccc", map[string]StageStatus{"a": StatusDone})
	mkRun(t, base, "flow-20260101-000000-aaaa", map[string]StageStatus{"a": StatusDone, "b": StatusDone})
	got, err := FindLatestCompletedRunDir(base, "flow", []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "flow-20260101-000000-aaaa" {
		t.Fatalf("partial run must be skipped, got %s", got)
	}
}

func TestFindLatestCompletedRunDir_DifferentTopologySkipped(t *testing.T) {
	base := t.TempDir()
	mkRun(t, base, "flow-20260301-000000-cccc", map[string]StageStatus{"a": StatusDone, "x": StatusDone}) // extra stage
	mkRun(t, base, "flow-20260101-000000-aaaa", map[string]StageStatus{"a": StatusDone, "b": StatusDone})
	got, err := FindLatestCompletedRunDir(base, "flow", []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "flow-20260101-000000-aaaa" {
		t.Fatalf("topology-mismatch run must be skipped, got %s", got)
	}
}

func TestFindLatestCompletedRunDir_AnchoredPrefix(t *testing.T) {
	base := t.TempDir()
	mkRun(t, base, "foo-bar-20260101-000000-aaaa", map[string]StageStatus{"a": StatusDone})
	_, err := FindLatestCompletedRunDir(base, "foo", []string{"a"})
	if !errors.Is(err, ErrNoRun) {
		t.Fatalf("want ErrNoRun, got %v", err)
	}
}

func TestFindLatestCompletedRunDir_CorruptNewestNotHidden(t *testing.T) {
	base := t.TempDir()
	mkRun(t, base, "flow-20260101-000000-aaaa", map[string]StageStatus{"a": StatusDone, "b": StatusDone})
	mkCorruptRun(t, base, "flow-20260301-000000-cccc")
	_, err := FindLatestCompletedRunDir(base, "flow", []string{"a", "b"})
	if !errors.Is(err, ErrCorruptLog) {
		t.Fatalf("want ErrCorruptLog, got %v", err)
	}
}
