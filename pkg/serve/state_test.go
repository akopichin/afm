package serve

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func acquire(t *testing.T) (*Host, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "serve")
	h, err := AcquireHost(dir, 4242, t0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	return h, dir
}

func TestAcquireHostCreatesPrivateDirAndHostJSON(t *testing.T) {
	_, dir := acquire(t)
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("state dir mode = %o, want 700", got)
	}
	got := readHostInfo(dir)
	if got.PID != 4242 || !got.StartedAt.Equal(t0) {
		t.Errorf("host.json = %+v", got)
	}
}

func TestSecondHostIsRefusedWithHostJSON(t *testing.T) {
	h, dir := acquire(t)
	if err := h.SetEndpoint("http://127.0.0.1:9999/mcp"); err != nil {
		t.Fatal(err)
	}
	_, err := AcquireHost(dir, 1, t0)
	var locked *LockedError
	if !errors.As(err, &locked) {
		t.Fatalf("err = %v, want *LockedError", err)
	}
	if locked.Info.PID != 4242 || locked.Info.Endpoint != "http://127.0.0.1:9999/mcp" {
		t.Errorf("locked info = %+v", locked.Info)
	}
}

func TestCloseReleasesLockAndRemovesHostJSON(t *testing.T) {
	h, dir := acquire(t)
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, hostInfoFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("host.json after Close: %v", err)
	}
	again, err := AcquireHost(dir, 7, t0)
	if err != nil {
		t.Fatalf("reacquire: %v", err)
	}
	again.Close()
}

// A host that released the lock writes nothing more: the next host owns the state directory.
func TestClosedHostWritesNothing(t *testing.T) {
	h, dir := acquire(t)
	r, _, err := h.CreateReceipt("req", LaunchParams{Project: "app", Mode: "core"}, LaunchIdentity{}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := h.UpdateReceipt(r.LaunchID, t0, func(cur *Receipt) { cur.RunID = "late" }); err == nil {
		t.Error("a closed host updated a receipt")
	}
	if _, _, err := h.CreateReceipt("req-late", LaunchParams{Project: "app", Mode: "core"}, LaunchIdentity{}, t0); err == nil {
		t.Error("a closed host created a receipt")
	}
	if err := h.UpdateAttached(func(list []Attached) []Attached { return append(list, Attached{RunID: "late"}) }); err == nil {
		t.Error("a closed host updated attached.json")
	}
	if err := h.SetEndpoint("http://127.0.0.1:1/mcp"); err == nil {
		t.Error("a closed host rewrote host.json")
	}

	next, err := AcquireHost(dir, 7, t0)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if got, err := next.Receipt(r.LaunchID); err != nil || got.RunID != "" {
		t.Errorf("receipt = %+v, %v; want it as the closed host left it", got, err)
	}
	if receipts, _ := next.Receipts(); len(receipts) != 1 {
		t.Errorf("receipts = %+v, want only the one made before Close", receipts)
	}
	if list, _ := next.LoadAttached(); len(list) != 0 {
		t.Errorf("attached = %+v, want none", list)
	}
}

func TestAttachedRoundTrip(t *testing.T) {
	h, _ := acquire(t)
	if got, err := h.LoadAttached(); err != nil || got != nil {
		t.Fatalf("empty = %v, %v", got, err)
	}
	want := []Attached{{Endpoint: "http://127.0.0.1:1", RunID: "r1", InstanceID: "i1", AttachedAt: t0}}
	if err := h.UpdateAttached(func(list []Attached) []Attached { return append(list, want...) }); err != nil {
		t.Fatal(err)
	}
	got, err := h.LoadAttached()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, %v", got, err)
	}
}

// Concurrent attaches must all survive: each update reads and writes under one lock.
func TestUpdateAttachedKeepsEveryConcurrentUpdate(t *testing.T) {
	h, _ := acquire(t)
	const n = 16
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			entry := Attached{Endpoint: "http://127.0.0.1:1", RunID: "r", InstanceID: fmt.Sprintf("i%d", i), AttachedAt: t0}
			if err := h.UpdateAttached(func(list []Attached) []Attached { return append(list, entry) }); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	got, err := h.LoadAttached()
	if err != nil || len(got) != n {
		t.Fatalf("attached = %d entries, %v; want %d", len(got), err, n)
	}
}
