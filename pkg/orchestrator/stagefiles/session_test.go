package stagefiles

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Устаревший ход, прочитавший «файла нет» до того, как преемник создал сессию, не должен её затереть.
func TestPublishNew_DoesNotOverwriteExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "autonomous.session.json")
	if err := publishNew(path, []byte(`{"session_id":"successor"}`)); err != nil {
		t.Fatal(err)
	}

	err := publishNew(path, []byte(`{"session_id":"stale"}`))
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("second publish err = %v, want fs.ErrExist", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != `{"session_id":"successor"}` {
		t.Fatalf("session file overwritten: %s", got)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temp files leaked: %v", entries)
	}
}

func TestLoadOrCreateSession_ConcurrentCreatorsAgree(t *testing.T) {
	dir := t.TempDir()
	const creators = 16
	ids := make([]string, creators)
	var wg sync.WaitGroup
	for i := range creators {
		wg.Go(func() {
			id, err := LoadOrCreateSession(dir, "autonomous")
			if err != nil {
				t.Errorf("creator %d: %v", i, err)
			}
			ids[i] = id
		})
	}
	wg.Wait()

	onDisk, err := LoadOrCreateSession(dir, "autonomous")
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		if id != onDisk {
			t.Fatalf("creator %d got %q, session on disk is %q", i, id, onDisk)
		}
	}
}
