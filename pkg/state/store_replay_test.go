package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// хвостовой обрыв (последняя строка без \n) — безопасно усекается, Open проходит.
func TestOpen_TornTailTruncates(t *testing.T) {
	dir := t.TempDir()
	good := `{"seq":1,"stage_id":"a","from":"pending","to":"planning","event":"x"}` + "\n"
	torn := `{"seq":2,"stage_id":"a","from":"planni` // оборвано, без \n
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(good+torn), 0644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatalf("Open on torn tail: want success, got %v", err)
	}
	defer s.Close()
	if got := s.Get("a"); got != StatusPlanning {
		t.Fatalf("state after torn-tail replay: want planning, got %q", got)
	}
}

// битая строка В СЕРЕДИНЕ (валидная строка после неё) — карантин + ошибка, файл цел.
func TestOpen_MidCorruptionQuarantines(t *testing.T) {
	dir := t.TempDir()
	line1 := `{"seq":1,"stage_id":"a","from":"pending","to":"planning","event":"x"}` + "\n"
	bad := `NOT JSON AT ALL` + "\n"
	line3 := `{"seq":2,"stage_id":"a","from":"planning","to":"done","event":"y"}` + "\n"
	orig := []byte(line1 + bad + line3)
	p := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(p, orig, 0644); err != nil {
		t.Fatal(err)
	}
	_, err := Open(dir, []string{"a"})
	if !errors.Is(err, ErrCorruptLog) {
		t.Fatalf("Open on mid-corruption: want ErrCorruptLog, got %v", err)
	}
	// оригинал не тронут
	after, _ := os.ReadFile(p)
	if string(after) != string(orig) {
		t.Fatal("original events.jsonl was modified")
	}
	// карантинная копия существует
	matches, _ := filepath.Glob(filepath.Join(dir, "events.jsonl.corrupt-*"))
	if len(matches) != 1 {
		t.Fatalf("want 1 quarantine copy, got %d", len(matches))
	}
}
