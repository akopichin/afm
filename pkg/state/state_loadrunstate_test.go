package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Битая полная строка в середине лога → LoadRunState обязан вернуть ErrCorruptLog
// (как и Open), а не молча отдать устаревший префикс.
func TestLoadRunState_MidCorruptionReturnsErr(t *testing.T) {
	dir := t.TempDir()
	line1 := `{"seq":1,"stage_id":"a","from":"pending","to":"planning","event":"x"}` + "\n"
	bad := `NOT JSON AT ALL` + "\n"
	line3 := `{"seq":2,"stage_id":"a","from":"planning","to":"done","event":"y"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(line1+bad+line3), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadRunState(dir)
	if !errors.Is(err, ErrCorruptLog) {
		t.Fatalf("LoadRunState на порче в середине: want ErrCorruptLog, got %v", err)
	}
}

// Оборванный хвост (без \n) — НЕ порча: LoadRunState отдаёт валидный префикс без ошибки.
func TestLoadRunState_TornTailReturnsPrefixNoErr(t *testing.T) {
	dir := t.TempDir()
	good := `{"seq":1,"stage_id":"a","from":"pending","to":"planning","event":"x"}` + "\n"
	torn := `{"seq":2,"stage_id":"a","from":"planni` // оборвано, без \n
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(good+torn), 0644); err != nil {
		t.Fatal(err)
	}

	rs, err := LoadRunState(dir)
	if err != nil {
		t.Fatalf("LoadRunState на torn-tail: want nil err, got %v", err)
	}
	if got := rs.Stages["a"].Status; got != StatusPlanning {
		t.Fatalf("state: want planning, got %q", got)
	}
}

// Самый свежий run с порчей в середине лога не должен молча пропускаться в пользу
// более старого run с тем же stage id — иначе approve/retry/revise уйдёт не туда.
func TestFindLatestRunForStage_CorruptLatestRunSurfaced(t *testing.T) {
	base := t.TempDir()

	// Старый валидный run со стадией "a".
	old := filepath.Join(base, "flow-20260101-000001-aaaa")
	if err := os.MkdirAll(old, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "events.jsonl"),
		[]byte(`{"seq":1,"stage_id":"a","from":"pending","to":"planning","event":"x"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Более свежий run с битой строкой В СЕРЕДИНЕ лога, тоже содержит "a".
	newer := filepath.Join(base, "flow-20260101-000002-bbbb")
	if err := os.MkdirAll(newer, 0755); err != nil {
		t.Fatal(err)
	}
	line1 := `{"seq":1,"stage_id":"a","from":"pending","to":"planning","event":"x"}` + "\n"
	bad := `CORRUPT` + "\n"
	line3 := `{"seq":2,"stage_id":"a","from":"planning","to":"done","event":"y"}` + "\n"
	if err := os.WriteFile(filepath.Join(newer, "events.jsonl"), []byte(line1+bad+line3), 0644); err != nil {
		t.Fatal(err)
	}

	_, _, err := FindLatestRunForStage(base, "a")
	if !errors.Is(err, ErrCorruptLog) {
		t.Fatalf("порча в самом свежем run: want ErrCorruptLog surface, got %v", err)
	}
}
