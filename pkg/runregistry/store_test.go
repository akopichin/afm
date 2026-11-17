package runregistry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newRecord(t *testing.T) Record {
	t.Helper()
	return Record{
		InstanceID: NewInstanceID(),
		PID:        os.Getpid(),
		Mode:       "core",
		Project:    Project{Root: "/work/app", RunName: "demo"},
		RunsDir:    "/work/app/.afm/runs",
		APIURL:     "http://127.0.0.1:8080",
		RunID:      "demo-1",
	}
}

func TestInstanceID(t *testing.T) {
	id := NewInstanceID()
	if !ValidInstanceID(id) || len(id) != 32 {
		t.Fatalf("NewInstanceID() = %q, want 32 hex characters", id)
	}
	if id == NewInstanceID() {
		t.Error("two instance ids are equal")
	}
	for _, bad := range []string{"", "abc", strings.Repeat("g", 32), strings.Repeat("A", 32), strings.Repeat("a", 33), "../" + strings.Repeat("a", 29)} {
		if ValidInstanceID(bad) {
			t.Errorf("ValidInstanceID(%q) = true", bad)
		}
	}
}

func TestDir(t *testing.T) {
	if got, want := Dir("/home/u"), filepath.Join("/home/u", ".afm", "active-runs"); got != want {
		t.Errorf("Dir = %q, want %q", got, want)
	}
}

func TestPublishRoundTripAndPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "active-runs")
	rec := newRecord(t)
	if err := Publish(dir, rec); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("dir = %v, %v; want mode 0700", info, err)
	}
	path := filepath.Join(dir, rec.InstanceID+".json")
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("file = %v, %v; want mode 0600", info, err)
	}

	rec.RunID = "demo-2"
	rec.PreviousRunIDs = []string{"demo-1"}
	rec.Ready = true
	if err := Publish(dir, rec); err != nil {
		t.Fatalf("Publish update: %v", err)
	}
	got, err := List(dir)
	if err != nil || len(got) != 1 {
		t.Fatalf("List = %v, %v; want one record", got, err)
	}
	if got[0].RunID != "demo-2" || !got[0].Ready || got[0].SchemaVersion != SchemaVersion || got[0].PreviousRunIDs[0] != "demo-1" {
		t.Errorf("record = %+v", got[0])
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("directory has %d entries, want only the record (no temp files)", len(entries))
	}
}

func TestPublishRejectsInvalidInstanceID(t *testing.T) {
	rec := newRecord(t)
	rec.InstanceID = "../escape"
	if err := Publish(t.TempDir(), rec); err == nil {
		t.Fatal("Publish accepted an invalid instance id")
	}
}

func TestPublishTrimsPreviousRunIDs(t *testing.T) {
	dir := t.TempDir()
	rec := newRecord(t)
	for i := 0; i < MaxPreviousRunIDs+5; i++ {
		rec.PreviousRunIDs = append(rec.PreviousRunIDs, "run")
	}
	if err := Publish(dir, rec); err != nil {
		t.Fatal(err)
	}
	got, _ := List(dir)
	if len(got) != 1 || len(got[0].PreviousRunIDs) != MaxPreviousRunIDs {
		t.Fatalf("previous run ids = %v, want %d", got, MaxPreviousRunIDs)
	}
}

func TestListMissingDirIsEmpty(t *testing.T) {
	got, err := List(filepath.Join(t.TempDir(), "absent"))
	if err != nil || len(got) != 0 {
		t.Errorf("List = %v, %v; want empty", got, err)
	}
}

func TestListSkipsGarbage(t *testing.T) {
	dir := t.TempDir()
	good := newRecord(t)
	if err := Publish(dir, good); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	other := NewInstanceID()
	write(other+".json.tmp-123", `{"schema_version":1}`)
	write(NewInstanceID()+".json", `{not json`)
	write(NewInstanceID()+".json", `{"schema_version":99,"instance_id":"x"}`)
	// A record whose file name differs from its instance id is not this registry's own.
	write(NewInstanceID()+".json", `{"schema_version":1,"instance_id":"`+other+`","pid":1}`)
	write("notes.txt", "hello")
	write("README.json", `{}`)
	if err := os.Mkdir(filepath.Join(dir, NewInstanceID()+".json"), 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].InstanceID != good.InstanceID {
		t.Errorf("List = %+v, want only %s", got, good.InstanceID)
	}
}

func TestRemoveOnlyOwnRecord(t *testing.T) {
	dir := t.TempDir()
	a, b := newRecord(t), newRecord(t)
	for _, r := range []Record{a, b} {
		if err := Publish(dir, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := Remove(dir, a.InstanceID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := Remove(dir, a.InstanceID); err != nil {
		t.Errorf("second Remove: %v, want nil", err)
	}
	if err := Remove(dir, "../"+b.InstanceID); err == nil {
		t.Error("Remove accepted an invalid instance id")
	}
	got, _ := List(dir)
	if len(got) != 1 || got[0].InstanceID != b.InstanceID {
		t.Errorf("List = %+v, want only the other record", got)
	}
}
