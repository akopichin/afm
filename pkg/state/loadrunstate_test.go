package state

import (
	"os"
	"path/filepath"
	"testing"
)

// LoadRunState читает авторитетное состояние из лога, даже если snapshot отстал.
func TestLoadRunState_RebuildsFromLogWhenSnapshotStale(t *testing.T) {
	dir := t.TempDir()

	s, err := Open(dir, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(&Transition{StageID: "a", From: StatusPending, To: StatusPlanning, Event: "x"}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// Портим snapshot: возвращаем его в устаревшее состояние.
	stale := `{"stages":{"a":{"status":"pending","updated_at":"2020-01-01T00:00:00Z"}},"last_seq":0}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(stale), 0644); err != nil {
		t.Fatal(err)
	}

	rs, err := LoadRunState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rs.Stages["a"].Status != StatusPlanning {
		t.Fatalf("LoadRunState must read from log: want planning, got %q", rs.Stages["a"].Status)
	}
}
