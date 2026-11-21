package serve

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/runregistry"
)

func newInventory(t *testing.T, w *fakeWorld) (*Inventory, *Host, string) {
	t.Helper()
	h, _ := acquire(t)
	regDir := t.TempDir()
	w.now = now0
	return &Inventory{Host: h, Discovery: w.discovery(regDir), Now: func() time.Time { return now0 }}, h, regDir
}

func publish(t *testing.T, dir string, mutate func(*runregistry.Record)) runregistry.Record {
	t.Helper()
	rec := baseRecord()
	if mutate != nil {
		mutate(&rec)
	}
	if err := runregistry.Publish(dir, rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

// ofLaunch makes a record the process of the host launch accepted for requestID.
func ofLaunch(requestID string) func(*runregistry.Record) {
	return func(r *runregistry.Record) {
		r.LaunchID = LaunchID(requestID)
		r.InstanceID = InstanceIDFor(r.LaunchID)
	}
}

func answering(rec runregistry.Record) map[string]Runtime {
	return map[string]Runtime{rec.APIURL: {ProtocolVersion: 1, RunID: rec.RunID, InstanceID: rec.InstanceID, Ready: true}}
}

func TestInventoryOwnership(t *testing.T) {
	w := &fakeWorld{markers: map[int]string{100: "m1"}}
	inv, h, regDir := newInventory(t, w)
	rec := publish(t, regDir, ofLaunch("req"))
	w.runtimes = answering(rec)

	// Without a receipt a record that claims a launch is only discovered.
	views, err := inv.List(context.Background())
	if err != nil || len(views) != 1 || views[0].Ownership != OwnershipDiscovered {
		t.Fatalf("views = %+v, err = %v", views, err)
	}

	if _, _, err := h.CreateReceipt("req", baseParams, LaunchIdentity{}, now0); err != nil {
		t.Fatal(err)
	}
	views, _ = inv.List(context.Background())
	if len(views) != 1 || views[0].Ownership != OwnershipManaged || views[0].Launch == nil {
		t.Fatalf("with a receipt: %+v", views)
	}
}

func TestInventoryListsReceiptsWithoutProcess(t *testing.T) {
	w := &fakeWorld{}
	inv, h, _ := newInventory(t, w)
	if _, _, err := h.CreateReceipt("req", baseParams, LaunchIdentity{}, now0); err != nil {
		t.Fatal(err)
	}
	views, _ := inv.List(context.Background())
	if len(views) != 1 || views[0].Ownership != OwnershipManaged || views[0].Liveness != "" || views[0].Launch.State != StateAccepted {
		t.Errorf("views = %+v", views)
	}
}

func TestResolve(t *testing.T) {
	w := &fakeWorld{markers: map[int]string{100: "m1"}}
	inv, h, regDir := newInventory(t, w)
	rec := publish(t, regDir, func(r *runregistry.Record) {
		ofLaunch("req")(r)
		r.PreviousRunIDs = []string{"run-0"}
	})
	w.runtimes = answering(rec)
	if _, _, err := h.CreateReceipt("req", baseParams, LaunchIdentity{}, now0); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	v, err := inv.Resolve(ctx, "run-1")
	if err != nil || v.InstanceID != rec.InstanceID || v.Endpoint != "http://127.0.0.1:9001" {
		t.Fatalf("Resolve(run-1) = %+v, %v", v, err)
	}
	var changed *RunChangedError
	if _, err := inv.Resolve(ctx, "run-0"); !errors.As(err, &changed) || changed.CurrentRunID != "run-1" {
		t.Errorf("Resolve(old run) error = %v, want run_changed to run-1", err)
	}
	if _, err := inv.Resolve(ctx, "nope"); !errors.Is(err, ErrRunNotFound) {
		t.Errorf("Resolve(unknown) error = %v", err)
	}

	w.runtimes = nil
	var unavailable *RunUnavailableError
	if _, err := inv.Resolve(ctx, "run-1"); !errors.As(err, &unavailable) || unavailable.Liveness != LiveUnreachable {
		t.Errorf("Resolve(unreachable) error = %v", err)
	}
}

// A manual run whose endpoint proves its identity is listed as attached and forwarded to, without an explicit
// attach; the host still never signals it and keeps nothing durable for it. Unverified records stay discovered.
func TestVerifiedManualRunIsAttached(t *testing.T) {
	w := &fakeWorld{markers: map[int]string{100: "m1"}}
	inv, h, regDir := newInventory(t, w)
	rec := publish(t, regDir, nil)
	w.runtimes = answering(rec)
	ctx := context.Background()
	views, err := inv.List(ctx)
	if err != nil || len(views) != 1 || views[0].Ownership != OwnershipAttached || views[0].Liveness != LiveActive {
		t.Fatalf("views = %+v, err = %v", views, err)
	}
	if v, err := inv.Resolve(ctx, "run-1"); err != nil || v.Ownership != OwnershipAttached {
		t.Errorf("Resolve = %+v, %v", v, err)
	}
	ctl := &Controller{Runs: inv}
	if _, err := ctl.Cancel(ctx, "run-1"); !errors.Is(err, ErrNotManaged) {
		t.Errorf("cancel of a verified manual run = %v, want not managed", err)
	}
	if _, err := ctl.Release(ctx, "run-1"); !errors.Is(err, ErrNotManaged) {
		t.Errorf("release of a verified manual run = %v, want not managed", err)
	}
	if list, _ := h.LoadAttached(); len(list) != 0 {
		t.Errorf("listing recorded an attachment: %+v", list)
	}

	// Another instance answers on the run's port: nothing proves the record's process.
	w.runtimes = map[string]Runtime{rec.APIURL: {ProtocolVersion: 1, RunID: "run-1", InstanceID: otherInstance}}
	var unavailable *RunUnavailableError
	if _, err := inv.Resolve(ctx, "run-1"); !errors.As(err, &unavailable) || unavailable.Liveness != LiveIdentityMismatch {
		t.Errorf("a forged record must be refused with its identity mismatch, got %v", err)
	}
	if _, err := ctl.Cancel(ctx, "run-1"); !errors.As(err, &unavailable) || unavailable.Liveness != LiveIdentityMismatch {
		t.Errorf("cancel of a forged record = %v, want identity_mismatch", err)
	}
}

func TestAttach(t *testing.T) {
	w := &fakeWorld{markers: map[int]string{100: "m1"}}
	inv, h, regDir := newInventory(t, w)
	rec := publish(t, regDir, func(r *runregistry.Record) { r.RunsDir = "/work/.afm/runs" })
	w.runtimes = answering(rec)
	ctx := context.Background()

	got, err := inv.Attach(ctx, rec.APIURL+"/")
	if err != nil {
		t.Fatal(err)
	}
	if got.InstanceID != testInstance || got.RunID != "run-1" || got.RunsDir != "/work/.afm/runs" || got.Endpoint != rec.APIURL {
		t.Errorf("attached = %+v", got)
	}
	// Attaching again replaces the entry instead of adding a second one.
	if _, err := inv.Attach(ctx, rec.APIURL); err != nil {
		t.Fatal(err)
	}
	if list, _ := h.LoadAttached(); len(list) != 1 {
		t.Fatalf("attached = %+v", list)
	}
	v, err := inv.Resolve(ctx, "run-1")
	if err != nil || v.Ownership != OwnershipAttached {
		t.Errorf("Resolve after attach = %+v, %v", v, err)
	}
}

func TestAttachRefusals(t *testing.T) {
	w := &fakeWorld{runtimes: map[string]Runtime{}}
	inv, _, _ := newInventory(t, w)
	ctx := context.Background()
	if _, err := inv.Attach(ctx, "http://192.0.2.1:8080"); err == nil {
		t.Error("a non-loopback endpoint must be refused")
	}
	if _, err := inv.Attach(ctx, "http://127.0.0.1:9"); err == nil {
		t.Error("an endpoint that does not answer must be refused")
	}
	w.runtimes["http://127.0.0.1:9002"] = Runtime{ProtocolVersion: 1}
	if _, err := inv.Attach(ctx, "http://127.0.0.1:9002"); err == nil {
		t.Error("a process without a run must be refused")
	}
}

// An afm that predates instance_id is keyed by endpoint and run: a new run on the same port is not that run.
func TestAttachedWithoutInstanceID(t *testing.T) {
	w := &fakeWorld{runtimes: map[string]Runtime{"http://127.0.0.1:9005": {ProtocolVersion: 1, RunID: "old-1"}}}
	inv, _, _ := newInventory(t, w)
	ctx := context.Background()
	if _, err := inv.Attach(ctx, "http://127.0.0.1:9005"); err != nil {
		t.Fatal(err)
	}
	if v, err := inv.Resolve(ctx, "old-1"); err != nil || v.Liveness != LiveActive {
		t.Fatalf("Resolve = %+v, %v", v, err)
	}
	w.runtimes["http://127.0.0.1:9005"] = Runtime{ProtocolVersion: 1, RunID: "other-2"}
	var unavailable *RunUnavailableError
	if _, err := inv.Resolve(ctx, "old-1"); !errors.As(err, &unavailable) || unavailable.Liveness != LiveIdentityMismatch {
		t.Errorf("after the port got another run: %v", err)
	}
}

// A verified attached instance that repeats serves a new run id: the listing follows it and the earlier id
// still reports run_changed instead of not_found.
func TestAttachedFollowsTheRepeatedRun(t *testing.T) {
	endpoint := "http://127.0.0.1:9007"
	w := &fakeWorld{runtimes: map[string]Runtime{endpoint: {ProtocolVersion: 1, RunID: "run-1", InstanceID: "inst-a"}}}
	inv, h, _ := newInventory(t, w)
	ctx := context.Background()
	if _, err := inv.Attach(ctx, endpoint); err != nil {
		t.Fatal(err)
	}
	w.runtimes[endpoint] = Runtime{ProtocolVersion: 1, RunID: "run-2", InstanceID: "inst-a"}

	v, err := inv.Resolve(ctx, "run-2")
	if err != nil || v.RunID != "run-2" || !slices.Equal(v.PreviousRunIDs, []string{"run-1"}) {
		t.Fatalf("Resolve(run-2) = %+v, %v", v, err)
	}
	var changed *RunChangedError
	if _, err := inv.Resolve(ctx, "run-1"); !errors.As(err, &changed) || changed.CurrentRunID != "run-2" {
		t.Errorf("Resolve(run-1) = %v, want run_changed to run-2", err)
	}
	// The history is durable: a third cycle keeps both earlier ids.
	w.runtimes[endpoint] = Runtime{ProtocolVersion: 1, RunID: "run-3", InstanceID: "inst-a"}
	if v, err = inv.Resolve(ctx, "run-3"); err != nil || !slices.Equal(v.PreviousRunIDs, []string{"run-1", "run-2"}) {
		t.Errorf("after the third cycle: %+v, %v", v, err)
	}
	if list, _ := h.LoadAttached(); len(list) != 1 || list[0].RunID != "run-3" {
		t.Errorf("saved attachment = %+v", list)
	}
}

// After the process exits and its registry record is gone, the receipt still resolves the earlier cycles.
func TestFindResolvesEarlierCyclesFromTheReceiptAfterExit(t *testing.T) {
	w := &fakeWorld{}
	inv, h, _ := newInventory(t, w)
	if _, _, err := h.CreateReceipt("req", baseParams, LaunchIdentity{}, now0); err != nil {
		t.Fatal(err)
	}
	if _, err := h.UpdateReceipt(LaunchID("req"), now0, func(r *Receipt) {
		r.State, r.RunID, r.PreviousRunIDs, r.RunsDir = StateExited, "run-3", []string{"run-1", "run-2"}, "/work/.afm/runs"
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"run-1", "run-2", "run-3"} {
		v, err := inv.Find(context.Background(), id)
		if err != nil || v.RunsDir != "/work/.afm/runs" {
			t.Errorf("Find(%s) = %+v, %v", id, v, err)
		}
	}
}

func TestReportFindSelectsTheExactInstance(t *testing.T) {
	inv, h, _ := newInventory(t, &fakeWorld{})
	ctx := context.Background()
	for _, request := range []string{"first", "second"} {
		if _, _, err := h.CreateReceipt(request, baseParams, LaunchIdentity{}, now0); err != nil {
			t.Fatal(err)
		}
		if _, err := h.UpdateReceipt(LaunchID(request), now0, func(r *Receipt) {
			r.State, r.RunID, r.RunsDir = StateExited, "shared-run", "/work/"+request+"/.afm/runs"
			r.InstanceID = InstanceIDFor(LaunchID(request))
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, request := range []string{"first", "second"} {
		id := InstanceIDFor(LaunchID(request))
		view, err := inv.find(ctx, "shared-run", id)
		if err != nil || view.RunsDir != "/work/"+request+"/.afm/runs" {
			t.Errorf("find(shared-run, %s) = %+v, %v", id, view, err)
		}
	}
	if _, err := inv.find(ctx, "shared-run", "unknown"); !errors.Is(err, ErrRunNotFound) {
		t.Errorf("find with unknown instance = %v, want %v", err, ErrRunNotFound)
	}
}

// An active run the host neither launched nor attached still needs an explicit attach.
func TestResolveActiveDiscoveredNeedsAttach(t *testing.T) {
	w := &fakeWorld{markers: map[int]string{100: "m1"}}
	inv, _, regDir := newInventory(t, w)
	rec := publish(t, regDir, ofLaunch("foreign"))
	w.runtimes = answering(rec)
	if _, err := inv.Resolve(context.Background(), "run-1"); !errors.Is(err, ErrNotOwned) {
		t.Errorf("active discovered run = %v, want not owned", err)
	}
}
