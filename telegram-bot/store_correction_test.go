package main

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestStoreUpdateDoesNotLeakFailedMutations(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Update(func(st *State) error {
		st.Outbox["kept"] = Outbound{ID: "kept"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := errors.New("abort")
	err = store.Update(func(st *State) error {
		st.Outbox["new"] = Outbound{ID: "new"}
		st.Users[17] = UserSession{Selected: "wrong"}
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("got %v, want abort", err)
	}
	got := store.Snapshot()
	if _, ok := got.Outbox["new"]; ok || len(got.Users) != 0 || got.Outbox["kept"].ID != "kept" {
		t.Fatalf("failed mutation leaked into memory: %+v", got)
	}
	store.path = filepath.Join(t.TempDir(), "missing-directory", "state.json")
	if err := store.Update(func(st *State) error { st.Outbox["unsaved"] = Outbound{ID: "unsaved"}; return nil }); err == nil {
		t.Fatal("expected disk save failure")
	}
	if _, ok := store.Snapshot().Outbox["unsaved"]; ok {
		t.Fatal("unsaved outbox item remained live in memory")
	}
}
