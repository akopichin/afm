package state

import (
	"testing"
	"time"
)

// TestLoadRunState_PreservesTransitionTime гарантирует, что LoadRunState не
// перештамповывает UpdatedAt текущим временем при replay events.jsonl:
// afm check должен показывать реальное время транзакции (Transition.Time),
// а не момент чтения лога. Регрессия: SetStageStatus всегда ставил
// time.Now(), из-за чего UPDATED в afm check "ехал" вперёд при каждом вызове.
//
// Формулировка теста: два последовательных LoadRunState с небольшой паузой
// между ними должны вернуть ОДИНАКОВЫЙ UpdatedAt — если бы replay
// перештамповывал время, второй вызов дал бы более позднее значение.
func TestLoadRunState_PreservesTransitionTime(t *testing.T) {
	dir := t.TempDir()

	s, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(&Transition{StageID: "a", From: StatusPending, To: StatusPlanning, Event: "start_planning"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	rs1, err := LoadRunState(dir)
	if err != nil {
		t.Fatalf("LoadRunState (1): %v", err)
	}
	updated1 := rs1.Stages["a"].UpdatedAt

	time.Sleep(20 * time.Millisecond)

	rs2, err := LoadRunState(dir)
	if err != nil {
		t.Fatalf("LoadRunState (2): %v", err)
	}
	updated2 := rs2.Stages["a"].UpdatedAt

	if !updated1.Equal(updated2) {
		t.Fatalf("UpdatedAt changed between replays (got restamped with time.Now()): first=%s second=%s",
			updated1, updated2)
	}

	// Дополнительно сверяем с реальным временем транзакции из events.jsonl —
	// именно оно должно попасть в UpdatedAt, а не время чтения лога.
	history, err := readEventsForTest(t, dir)
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("events.jsonl transitions = %d, want 1", len(history))
	}
	wantTime := history[0].Time
	if !updated1.Equal(wantTime) {
		t.Fatalf("UpdatedAt = %s, want Transition.Time = %s", updated1, wantTime)
	}

	// И убедимся, что это время действительно в прошлом относительно "сейчас" —
	// защита от теста, который случайно проходит из-за совпадения секунд.
	if !updated1.Before(time.Now()) {
		t.Fatalf("UpdatedAt = %s, want strictly before now", updated1)
	}
}

// readEventsForTest читает events.jsonl через тот же путь replay, что
// использует Store.Open, чтобы получить авторитетный Transition.Time для
// сравнения. Использует новый Store.Open + History(), не дублируя парсинг.
func readEventsForTest(t *testing.T, dir string) ([]Transition, error) {
	t.Helper()
	s, err := Open(dir, []string{"a"})
	if err != nil {
		return nil, err
	}
	defer s.Close()
	return s.History()
}
