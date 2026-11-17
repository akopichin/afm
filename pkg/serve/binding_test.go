package serve

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/runregistry"
)

func TestMergeRunBinding(t *testing.T) {
	long := make([]string, maxRunHistory+5)
	for i := range long {
		long[i] = "old-" + strconv.Itoa(i)
	}
	tests := []struct {
		name     string
		saved    runBinding
		based    string
		observed runBinding
		want     runBinding
	}{
		{"first observation", runBinding{}, "", runBinding{RunID: "run-1", RunsDir: "/r"}, runBinding{RunID: "run-1", RunsDir: "/r"}},
		{"next cycle", runBinding{RunID: "run-1", RunsDir: "/r"}, "run-1", runBinding{RunID: "run-2"},
			runBinding{RunID: "run-2", PreviousRunIDs: []string{"run-1"}, RunsDir: "/r"}},
		{"record knows unseen cycles", runBinding{RunID: "run-1"}, "run-1", runBinding{RunID: "run-3", PreviousRunIDs: []string{"run-1", "run-2"}},
			runBinding{RunID: "run-3", PreviousRunIDs: []string{"run-1", "run-2"}}},
		{"an earlier cycle never comes back", runBinding{RunID: "run-3", PreviousRunIDs: []string{"run-1", "run-2"}}, "run-3", runBinding{RunID: "run-2", PreviousRunIDs: []string{"run-1"}},
			runBinding{RunID: "run-3", PreviousRunIDs: []string{"run-1", "run-2"}}},
		{"a probe that raced with a newer commit keeps its run as an earlier one", runBinding{RunID: "run-3", PreviousRunIDs: []string{"run-1"}}, "run-1", runBinding{RunID: "run-2", Live: true},
			runBinding{RunID: "run-3", PreviousRunIDs: []string{"run-1", "run-2"}}},
		{"a raced record that proves it is newer", runBinding{RunID: "run-2", PreviousRunIDs: []string{"run-1"}}, "run-1", runBinding{RunID: "run-3", PreviousRunIDs: []string{"run-1", "run-2"}},
			runBinding{RunID: "run-3", PreviousRunIDs: []string{"run-1", "run-2"}}},
		{"an answer given after the newest commit takes its run back from the history", runBinding{RunID: "run-2", PreviousRunIDs: []string{"run-1", "run-3"}}, "run-2", runBinding{RunID: "run-3", Live: true},
			runBinding{RunID: "run-3", PreviousRunIDs: []string{"run-1", "run-2"}}},
		{"a record does not take a run back from the history", runBinding{RunID: "run-3", PreviousRunIDs: []string{"run-1", "run-2"}}, "run-3", runBinding{RunID: "run-2"},
			runBinding{RunID: "run-3", PreviousRunIDs: []string{"run-1", "run-2"}}},
		{"no runs directory keeps the known one", runBinding{RunID: "run-1", RunsDir: "/r"}, "run-1", runBinding{RunID: "run-1"},
			runBinding{RunID: "run-1", RunsDir: "/r"}},
		{"history is bounded to the newest", runBinding{RunID: "cur", PreviousRunIDs: long}, "cur", runBinding{RunID: "cur"},
			runBinding{RunID: "cur", PreviousRunIDs: long[len(long)-maxRunHistory:]}},
		{"a raced run joins the bounded history as its newest entry", runBinding{RunID: "cur", PreviousRunIDs: long}, "other", runBinding{RunID: "raced", Live: true},
			runBinding{RunID: "cur", PreviousRunIDs: append(slices.Clone(long[len(long)-maxRunHistory+1:]), "raced")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeRunBinding(tt.saved, tt.based, tt.observed)
			if got.RunID != tt.want.RunID || got.RunsDir != tt.want.RunsDir || !slices.Equal(got.PreviousRunIDs, tt.want.PreviousRunIDs) {
				t.Errorf("merge = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// managedAt saves a receipt for requestID whose process was last seen serving runID.
func managedAt(t *testing.T, h *Host, requestID, runID string) string {
	t.Helper()
	if _, _, err := h.CreateReceipt(requestID, baseParams, LaunchIdentity{}, now0); err != nil {
		t.Fatal(err)
	}
	launchID := LaunchID(requestID)
	if _, err := h.UpdateReceipt(launchID, now0, func(r *Receipt) {
		r.State, r.RunID, r.PID, r.ProcessStart, r.InstanceID = StateReady, runID, 100, "m1", InstanceIDFor(launchID)
	}); err != nil {
		t.Fatal(err)
	}
	return launchID
}

func startupApp(h *Host, inv *Inventory) *App {
	return &App{host: h, inventory: inv, opts: Options{Log: io.Discard}}
}

// The host may start only after the process went through cycles it never saw and exited: what its record knows is
// saved before the record is removed, so the reports of every cycle still resolve.
func TestStartupReapSavesTheExitedRecordsRunsFirst(t *testing.T) {
	w := &fakeWorld{} // the process is gone and its endpoint silent
	inv, h, regDir := newInventory(t, w)
	launchID := managedAt(t, h, "req", "run-1")
	publish(t, regDir, func(r *runregistry.Record) {
		ofLaunch("req")(r)
		r.RunID, r.PreviousRunIDs, r.RunsDir = "run-3", []string{"run-1", "run-2"}, "/work/.afm/runs"
	})
	if err := startupApp(h, inv).reapExited(inv.Discovery); err != nil {
		t.Fatal(err)
	}
	if list, _ := runregistry.List(regDir); len(list) != 0 {
		t.Errorf("the exited record stayed: %+v", list)
	}
	got, _ := h.Receipt(launchID)
	if got.RunID != "run-3" || !slices.Equal(got.PreviousRunIDs, []string{"run-1", "run-2"}) || got.RunsDir != "/work/.afm/runs" {
		t.Errorf("receipt = %+v", got)
	}
	for _, id := range []string{"run-1", "run-2", "run-3"} {
		if v, err := inv.Find(context.Background(), id); err != nil || v.RunsDir != "/work/.afm/runs" {
			t.Errorf("Find(%s) = %+v, %v", id, v, err)
		}
	}
}

func TestStartupReapKeepsARecordItCouldNotSave(t *testing.T) {
	w := &fakeWorld{}
	inv, h, regDir := newInventory(t, w)
	launchID := managedAt(t, h, "req", "run-1")
	publish(t, regDir, func(r *runregistry.Record) { ofLaunch("req")(r); r.RunID = "run-2" })
	if err := os.WriteFile(h.receiptPath(launchID), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := startupApp(h, inv).reapExited(inv.Discovery); err != nil {
		t.Fatal(err)
	}
	if list, _ := runregistry.List(regDir); len(list) != 1 {
		t.Errorf("a record whose runs were not saved must stay: %+v", list)
	}
}

func TestStartupReapSavesTheRunsOfAnAttachedProcess(t *testing.T) {
	w := &fakeWorld{markers: map[int]string{100: "m1"}}
	inv, h, regDir := newInventory(t, w)
	rec := publish(t, regDir, func(r *runregistry.Record) { r.RunsDir = "/work/.afm/runs" })
	w.runtimes = answering(rec)
	if _, err := inv.Attach(context.Background(), rec.APIURL); err != nil {
		t.Fatal(err)
	}
	publish(t, regDir, func(r *runregistry.Record) {
		r.RunID, r.PreviousRunIDs, r.RunsDir = "run-2", []string{"run-1"}, "/work/.afm/runs"
	})
	w.runtimes, w.markers = nil, nil
	if err := startupApp(h, inv).reapExited(inv.Discovery); err != nil {
		t.Fatal(err)
	}
	list, _ := h.LoadAttached()
	if len(list) != 1 || list[0].RunID != "run-2" || !slices.Equal(list[0].PreviousRunIDs, []string{"run-1"}) {
		t.Errorf("attached = %+v", list)
	}
}

// An attached process that has a registry record follows its cycles in attached.json too, so they resolve after
// the process and its record are gone.
func TestRegisteredAttachmentKeepsItsRunsAfterExit(t *testing.T) {
	w := &fakeWorld{markers: map[int]string{100: "m1"}}
	inv, _, regDir := newInventory(t, w)
	rec := publish(t, regDir, func(r *runregistry.Record) { r.RunsDir = "/work/.afm/runs" })
	w.runtimes = answering(rec)
	ctx := context.Background()
	if _, err := inv.Attach(ctx, rec.APIURL); err != nil {
		t.Fatal(err)
	}
	next := publish(t, regDir, func(r *runregistry.Record) {
		r.RunID, r.PreviousRunIDs, r.RunsDir = "run-2", []string{"run-1"}, "/work/.afm/runs"
	})
	w.runtimes = answering(next)
	if v, err := inv.Resolve(ctx, "run-2"); err != nil || v.Ownership != OwnershipAttached {
		t.Fatalf("Resolve(run-2) = %+v, %v", v, err)
	}
	if err := runregistry.Remove(regDir, rec.InstanceID); err != nil {
		t.Fatal(err)
	}
	w.runtimes, w.markers = nil, nil
	for _, id := range []string{"run-1", "run-2"} {
		if v, err := inv.Find(ctx, id); err != nil || v.RunsDir != "/work/.afm/runs" {
			t.Errorf("Find(%s) after exit = %+v, %v", id, v, err)
		}
	}
}

func TestAttachingAgainKeepsWhatTheHostKnew(t *testing.T) {
	t.Run("history and runs directory", func(t *testing.T) {
		endpoint := "http://127.0.0.1:9007"
		w := &fakeWorld{runtimes: map[string]Runtime{endpoint: {ProtocolVersion: 1, RunID: "run-1", InstanceID: testInstance}}}
		inv, h, regDir := newInventory(t, w)
		publish(t, regDir, func(r *runregistry.Record) { r.APIURL, r.RunsDir = endpoint, "/work/.afm/runs" })
		ctx := context.Background()
		if _, err := inv.Attach(ctx, endpoint); err != nil {
			t.Fatal(err)
		}
		if err := runregistry.Remove(regDir, testInstance); err != nil {
			t.Fatal(err)
		}
		w.runtimes[endpoint] = Runtime{ProtocolVersion: 1, RunID: "run-2", InstanceID: testInstance}
		if _, err := inv.Resolve(ctx, "run-2"); err != nil {
			t.Fatal(err)
		}
		again, err := inv.Attach(ctx, endpoint)
		if err != nil {
			t.Fatal(err)
		}
		if again.RunID != "run-2" || !slices.Equal(again.PreviousRunIDs, []string{"run-1"}) || again.RunsDir != "/work/.afm/runs" {
			t.Errorf("attached again = %+v", again)
		}
		if list, _ := h.LoadAttached(); len(list) != 1 {
			t.Errorf("attached = %+v", list)
		}
		if _, err := inv.Find(ctx, "run-1"); err != nil {
			t.Errorf("Find(run-1) after attaching again: %v", err)
		}
	})
	t.Run("a process attached at its third cycle", func(t *testing.T) {
		w := &fakeWorld{markers: map[int]string{100: "m1"}}
		inv, _, regDir := newInventory(t, w)
		rec := publish(t, regDir, func(r *runregistry.Record) {
			r.RunID, r.PreviousRunIDs, r.RunsDir = "run-3", []string{"run-1", "run-2"}, "/work/.afm/runs"
		})
		w.runtimes = answering(rec)
		got, err := inv.Attach(context.Background(), rec.APIURL)
		if err != nil || !slices.Equal(got.PreviousRunIDs, []string{"run-1", "run-2"}) {
			t.Errorf("attached = %+v, %v", got, err)
		}
	})
	t.Run("an endpoint without instance id carries nothing over", func(t *testing.T) {
		endpoint := "http://127.0.0.1:9005"
		w := &fakeWorld{runtimes: map[string]Runtime{endpoint: {ProtocolVersion: 1, RunID: "old-1"}}}
		inv, _, _ := newInventory(t, w)
		ctx := context.Background()
		if _, err := inv.Attach(ctx, endpoint); err != nil {
			t.Fatal(err)
		}
		w.runtimes[endpoint] = Runtime{ProtocolVersion: 1, RunID: "other-2"}
		got, err := inv.Attach(ctx, endpoint)
		if err != nil || got.RunID != "other-2" || len(got.PreviousRunIDs) != 0 {
			t.Errorf("attached = %+v, %v", got, err)
		}
	})
}

// A record that names a launch but not the instance the host gave it is another process: nothing is merged.
func TestRecordOfAnotherInstanceIsNotMergedIntoTheLaunch(t *testing.T) {
	w := &fakeWorld{markers: map[int]string{100: "m1"}}
	inv, h, regDir := newInventory(t, w)
	if _, _, err := h.CreateReceipt("req", baseParams, LaunchIdentity{}, now0); err != nil {
		t.Fatal(err)
	}
	rec := publish(t, regDir, func(r *runregistry.Record) { r.LaunchID, r.RunID = LaunchID("req"), "run-9" })
	w.runtimes = answering(rec)
	views, err := inv.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range views {
		if v.RunID == "run-9" && v.Ownership != OwnershipDiscovered {
			t.Errorf("foreign record owned as %s", v.Ownership)
		}
	}
	if got, _ := h.Receipt(LaunchID("req")); got.RunID != "" || got.PID != 0 {
		t.Errorf("receipt took the foreign record: %+v", got)
	}
	if _, err := inv.Find(context.Background(), "run-9"); !errors.Is(err, ErrRunNotFound) {
		t.Errorf("the foreign run must not be found as owned: %v", err)
	}
}

// probeScript answers every probe with the current runtime; a held probe waits for its release and then answers
// what it was told, so a test can order a slow observation after a faster one.
type probeScript struct {
	instance string
	mu       sync.Mutex
	runID    string
	held     *heldProbe
}

type heldProbe struct {
	answer           Runtime
	entered, release chan struct{}
}

func (p *probeScript) answer(runID string) Runtime {
	return Runtime{ProtocolVersion: 1, RunID: runID, InstanceID: p.instance, Ready: true}
}

func (p *probeScript) set(runID string) { p.mu.Lock(); p.runID = runID; p.mu.Unlock() }

// hold makes the next probe wait for release and answer runID.
func (p *probeScript) hold(runID string) *heldProbe {
	h := &heldProbe{answer: p.answer(runID), entered: make(chan struct{}), release: make(chan struct{})}
	p.mu.Lock()
	p.held = h
	p.mu.Unlock()
	return h
}

func (p *probeScript) probe(context.Context, string) (Runtime, error) {
	p.mu.Lock()
	h, runID := p.held, p.runID
	p.held = nil
	p.mu.Unlock()
	if h == nil {
		return p.answer(runID), nil
	}
	close(h.entered)
	<-h.release
	return h.answer, nil
}

// raceObservations runs slow until its probe is held, lets fast observe fresh and commit, then releases slow and
// joins it.
func raceObservations(t *testing.T, probes *probeScript, slowRun, fastRun string, slow, fast func() error) {
	t.Helper()
	held := probes.hold(slowRun)
	done := make(chan error, 1)
	go func() { done <- slow() }()
	released := false
	defer func() {
		if !released {
			close(held.release)
			<-done
		}
	}()
	select {
	case <-held.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the slow observation never probed")
	}
	probes.set(fastRun)
	if err := fast(); err != nil {
		t.Fatalf("fast observation: %v", err)
	}
	close(held.release)
	released = true
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("slow observation: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the slow observation did not return")
	}
}

// A reattach whose probe answered before another attach committed a newer run must not move the binding back,
// and the run it saw is still a run of the process.
func TestADelayedReattachCannotMoveTheRunBack(t *testing.T) {
	const endpoint = "http://127.0.0.1:9007"
	inv, h, _ := newInventory(t, &fakeWorld{})
	probes := &probeScript{instance: testInstance, runID: "run-1"}
	inv.Discovery.Probe = probes.probe
	ctx := context.Background()
	if _, err := inv.Attach(ctx, endpoint); err != nil {
		t.Fatal(err)
	}
	attach := func() error { _, err := inv.Attach(ctx, endpoint); return err }
	raceObservations(t, probes, "run-2", "run-3", attach, attach)

	list, _ := h.LoadAttached()
	if len(list) != 1 || list[0].RunID != "run-3" || !slices.Equal(list[0].PreviousRunIDs, []string{"run-1", "run-2"}) {
		t.Fatalf("attached = %+v, want run-3 after run-1 and run-2", list)
	}
	if v, err := inv.Resolve(ctx, "run-3"); err != nil || v.RunID != "run-3" {
		t.Errorf("Resolve(run-3) = %+v, %v", v, err)
	}
	if _, err := inv.Find(ctx, "run-2"); err != nil {
		t.Errorf("Find(run-2): %v", err)
	}
}

// Every producer of an observation reads the binding and the registry before it probes: a slower observation then
// never moves the run back, even once the record names the newer run, and the run it saw stays findable.
func TestARacingObservationKeepsTheNewerRunAndItsOwn(t *testing.T) {
	type producer struct {
		// instance is who the process answers as; setup leaves it bound to run-1 and returns how to read the binding
		// and how to observe once.
		instance string
		// record marks the process's registry record; nil means the process has none.
		record func(*runregistry.Record)
		setup  func(t *testing.T, inv *Inventory, h *Host, regDir string) (read func() runBinding, observe func() error)
	}
	list := func(inv *Inventory) func() error {
		return func() error { _, err := inv.List(context.Background()); return err }
	}
	attached := func(h *Host) func() runBinding {
		return func() runBinding {
			list, _ := h.LoadAttached()
			if len(list) != 1 {
				return runBinding{}
			}
			return list[0].binding()
		}
	}
	managed := func(h *Host, launchID string) func() runBinding {
		return func() runBinding { r, _ := h.Receipt(launchID); return r.binding() }
	}
	attachRegistered := func(t *testing.T, inv *Inventory, regDir string) runregistry.Record {
		rec := publish(t, regDir, nil)
		if _, err := inv.Attach(context.Background(), rec.APIURL); err != nil {
			t.Fatal(err)
		}
		return rec
	}
	registered := func(*runregistry.Record) {}
	producers := map[string]producer{
		"listing a launch": {InstanceIDFor(LaunchID("req")), ofLaunch("req"), func(t *testing.T, inv *Inventory, h *Host, regDir string) (func() runBinding, func() error) {
			launchID := managedAt(t, h, "req", "run-1")
			publish(t, regDir, ofLaunch("req"))
			return managed(h, launchID), list(inv)
		}},
		"watching a launch": {InstanceIDFor(LaunchID("req")), ofLaunch("req"), func(t *testing.T, inv *Inventory, h *Host, regDir string) (func() runBinding, func() error) {
			launchID := managedAt(t, h, "req", "run-1")
			publish(t, regDir, ofLaunch("req"))
			l := NewLauncher(LauncherConfig{Host: h, Discovery: inv.Discovery, Now: func() time.Time { return now0 }})
			return managed(h, launchID), func() error { l.observe(launchID); return nil }
		}},
		"listing an attached process with a record": {testInstance, registered, func(t *testing.T, inv *Inventory, h *Host, regDir string) (func() runBinding, func() error) {
			attachRegistered(t, inv, regDir)
			return attached(h), list(inv)
		}},
		"reattaching a process with a record": {testInstance, registered, func(t *testing.T, inv *Inventory, h *Host, regDir string) (func() runBinding, func() error) {
			rec := attachRegistered(t, inv, regDir)
			return attached(h), func() error { _, err := inv.Attach(context.Background(), rec.APIURL); return err }
		}},
		"listing an attached process without a record": {testInstance, nil, func(t *testing.T, inv *Inventory, h *Host, _ string) (func() runBinding, func() error) {
			if _, err := inv.Attach(context.Background(), "http://127.0.0.1:9007"); err != nil {
				t.Fatal(err)
			}
			return attached(h), list(inv)
		}},
	}
	for name, p := range producers {
		t.Run(name, func(t *testing.T) {
			inv, h, regDir := newInventory(t, &fakeWorld{markers: map[int]string{100: "m1"}})
			probes := &probeScript{instance: p.instance, runID: "run-1"}
			inv.Discovery.Probe = probes.probe
			read, observe := p.setup(t, inv, h, regDir)
			fast := observe
			if p.record != nil {
				// While the slower observation waits for its answer, the process moves on and its record says so.
				fast = func() error {
					publish(t, regDir, func(r *runregistry.Record) {
						p.record(r)
						r.RunID, r.PreviousRunIDs = "run-3", []string{"run-1", "run-2"}
					})
					return observe()
				}
			}
			raceObservations(t, probes, "run-2", "run-3", observe, fast)

			if got := read(); got.RunID != "run-3" || !slices.Equal(got.PreviousRunIDs, []string{"run-1", "run-2"}) {
				t.Fatalf("binding = %+v, want run-3 after run-1 and run-2", got)
			}
			if _, err := inv.Find(context.Background(), "run-2"); err != nil {
				t.Errorf("Find(run-2) of the slower observation: %v", err)
			}
		})
	}
}

// A raced observation can be the newer one: it is kept as an earlier run, and the next answer of the process
// makes it current again instead of leaving the binding behind for good.
func TestTheNextAnswerRepairsARunFiledAsEarlier(t *testing.T) {
	inv, h, regDir := newInventory(t, &fakeWorld{markers: map[int]string{100: "m1"}})
	probes := &probeScript{instance: InstanceIDFor(LaunchID("req")), runID: "run-1"}
	inv.Discovery.Probe = probes.probe
	launchID := managedAt(t, h, "req", "run-1")
	publish(t, regDir, ofLaunch("req"))
	list := func() error { _, err := inv.List(context.Background()); return err }
	raceObservations(t, probes, "run-3", "run-2", list, list)
	if r, _ := h.Receipt(launchID); r.RunID != "run-2" || !slices.Contains(r.PreviousRunIDs, "run-3") {
		t.Fatalf("after the race: %+v, want run-2 current and run-3 kept", r)
	}

	probes.set("run-3")
	ctx := context.Background()
	if v, err := inv.Resolve(ctx, "run-3"); err != nil || v.RunID != "run-3" {
		t.Fatalf("Resolve(run-3) once the process answers it = %+v, %v", v, err)
	}
	var changed *RunChangedError
	if _, err := inv.Resolve(ctx, "run-2"); !errors.As(err, &changed) || changed.CurrentRunID != "run-3" {
		t.Errorf("Resolve(run-2) = %v, want run_changed to run-3", err)
	}
}

// The process exits while the watcher waits for its answer: the run it answered still belongs to the launch, and
// the launch stays over.
func TestAnObservationOvertakenByTheExitKeepsItsRun(t *testing.T) {
	inv, h, regDir := newInventory(t, &fakeWorld{markers: map[int]string{100: "m1"}})
	instance := InstanceIDFor(LaunchID("req"))
	probes := &probeScript{instance: instance, runID: "run-1"}
	inv.Discovery.Probe = probes.probe
	launchID := managedAt(t, h, "req", "run-1")
	publish(t, regDir, ofLaunch("req"))
	l := NewLauncher(LauncherConfig{Host: h, Discovery: inv.Discovery, Now: func() time.Time { return now0 }})

	held := probes.hold("run-2")
	observed := make(chan struct{})
	go func() { l.observe(launchID); close(observed) }()
	select {
	case <-held.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the watcher never probed")
	}
	if err := runregistry.Remove(regDir, instance); err != nil {
		t.Fatal(err)
	}
	l.finish(launchID, func(r *Receipt) { l.ended(r, "exited") }) // what the reaper records once the child exits
	close(held.release)
	select {
	case <-observed:
	case <-time.After(10 * time.Second):
		t.Fatal("the watcher did not return")
	}

	r, _ := h.Receipt(launchID)
	if r.RunID != "run-2" || !slices.Equal(r.PreviousRunIDs, []string{"run-1"}) {
		t.Errorf("receipt runs = %s after %v, want run-2 after run-1", r.RunID, r.PreviousRunIDs)
	}
	if r.State != StateExited || !r.ProcessGone || r.holdsRuntime() {
		t.Errorf("receipt = %+v, want the exit kept", r)
	}
	if _, err := inv.Find(context.Background(), "run-2"); err != nil {
		t.Errorf("Find(run-2) after the exit: %v", err)
	}
}

// Startup sees a live process serve a cycle the host did not know. The process may then exit and drop its record
// before anything else looks, so the startup scan saves that cycle itself.
func TestStartupScanSavesALiveObservation(t *testing.T) {
	for name, attached := range map[string]bool{"managed": false, "attached": true} {
		t.Run(name, func(t *testing.T) {
			w := &fakeWorld{markers: map[int]string{100: "m1"}}
			inv, h, regDir := newInventory(t, w)
			ownRecord := ofLaunch("req")
			if attached {
				first := publish(t, regDir, nil)
				w.runtimes = answering(first)
				if _, err := inv.Attach(context.Background(), first.APIURL); err != nil {
					t.Fatal(err)
				}
				ownRecord = func(*runregistry.Record) {}
			} else {
				managedAt(t, h, "req", "run-1")
			}
			rec := publish(t, regDir, func(r *runregistry.Record) {
				ownRecord(r)
				r.RunID, r.PreviousRunIDs, r.RunsDir = "run-2", []string{"run-1"}, "/work/.afm/runs"
			})
			w.runtimes = answering(rec)
			if err := startupApp(h, inv).reapExited(inv.Discovery); err != nil {
				t.Fatal(err)
			}
			if err := runregistry.Remove(regDir, rec.InstanceID); err != nil {
				t.Fatal(err)
			}
			w.runtimes, w.markers = nil, nil
			if _, err := inv.Find(context.Background(), "run-2"); err != nil {
				t.Errorf("the cycle startup saw is lost once the process exited: %v", err)
			}
		})
	}
}

// Startup read a record of a cycle the host did not know, then the process unlinked it and exited before the
// process check: the recheck finds no record, so the scan's own observation is what keeps that cycle.
func TestStartupSavesAnExitedObservationWhoseRecordVanished(t *testing.T) {
	for name, attached := range map[string]bool{"managed": false, "attached": true} {
		t.Run(name, func(t *testing.T) {
			w := &fakeWorld{markers: map[int]string{100: "m1"}}
			inv, h, regDir := newInventory(t, w)
			ownRecord := ofLaunch("req")
			if attached {
				first := publish(t, regDir, nil)
				w.runtimes = answering(first)
				if _, err := inv.Attach(context.Background(), first.APIURL); err != nil {
					t.Fatal(err)
				}
				ownRecord = func(*runregistry.Record) {}
			} else {
				managedAt(t, h, "req", "run-1")
			}
			rec := publish(t, regDir, func(r *runregistry.Record) {
				ownRecord(r)
				r.RunID, r.PreviousRunIDs, r.RunsDir = "run-2", []string{"run-1"}, "/work/.afm/runs"
			})
			inv.Discovery.Probe = func(context.Context, string) (Runtime, error) {
				return Runtime{}, errors.New("the exiting process closed its listener")
			}
			var checked atomic.Bool
			inv.Discovery.ProcessStart = func(int) (string, error) {
				if !checked.Swap(true) {
					if err := runregistry.Remove(regDir, rec.InstanceID); err != nil {
						t.Error(err)
					}
				}
				return "", os.ErrNotExist
			}
			if err := startupApp(h, inv).reapExited(inv.Discovery); err != nil {
				t.Fatal(err)
			}
			if !checked.Load() {
				t.Fatal("startup never checked the process")
			}
			if _, err := inv.Find(context.Background(), "run-2"); err != nil {
				t.Errorf("the cycle startup read is lost once its record vanished: %v", err)
			}
		})
	}
}

// Startup judged a container's record exited, but the recheck before removing it finds the container answering:
// that answer is an observation too and is saved before it is dropped.
func TestStartupRecheckSavesWhatTheRunAnswered(t *testing.T) {
	w := &fakeWorld{}
	inv, h, regDir := newInventory(t, w)
	managedAt(t, h, "req", "run-1")
	rec := publish(t, regDir, func(r *runregistry.Record) { ofLaunch("req")(r); r.Docker, r.RunsDir = true, "/work/.afm/runs" })
	var probes int
	inv.Discovery.Probe = func(context.Context, string) (Runtime, error) {
		if probes++; probes == 1 {
			return Runtime{}, errors.New("connection refused")
		}
		return Runtime{ProtocolVersion: 1, RunID: "run-2", InstanceID: rec.InstanceID, Ready: true}, nil
	}
	if err := startupApp(h, inv).reapExited(inv.Discovery); err != nil {
		t.Fatal(err)
	}
	if probes != 2 {
		t.Fatalf("probes = %d, want the scan and the recheck", probes)
	}
	if err := runregistry.Remove(regDir, rec.InstanceID); err != nil {
		t.Fatal(err)
	}
	inv.Discovery.Probe = func(context.Context, string) (Runtime, error) { return Runtime{}, errors.New("connection refused") }
	if _, err := inv.Find(context.Background(), "run-2"); err != nil {
		t.Errorf("the cycle the recheck saw is lost once the container is gone: %v", err)
	}
}

// The reaper's last look at a Docker run whose container still answers is an observation too: the cycle it saw
// resolves after the container is gone.
func TestReaperSavesTheCycleAnOrphanedContainerServes(t *testing.T) {
	w := &fakeWorld{} // the host-side process is gone
	inv, h, regDir := newInventory(t, w)
	launchID := managedAt(t, h, "req", "run-1")
	rec := publish(t, regDir, func(r *runregistry.Record) {
		ofLaunch("req")(r)
		r.Docker, r.RunID, r.PreviousRunIDs, r.RunsDir = true, "run-2", []string{"run-1"}, "/work/.afm/runs"
	})
	w.runtimes = answering(rec)
	l := NewLauncher(LauncherConfig{Host: h, Discovery: inv.Discovery, Now: func() time.Time { return now0 }})
	defer l.Close()
	l.reaped(launchID, 0, nil)
	if got, _ := h.Receipt(launchID); got.State != StateReady || got.ExitCode == nil {
		t.Fatalf("receipt = %+v, want the launch kept while its container answers", got)
	}
	if err := runregistry.Remove(regDir, rec.InstanceID); err != nil {
		t.Fatal(err)
	}
	w.runtimes = nil
	if _, err := inv.Find(context.Background(), "run-2"); err != nil {
		t.Errorf("the cycle the reaper saw is lost once the container is gone: %v", err)
	}
}

// An uncertain launch's deadline passes while a listing finds its process. The expiry decided on the older receipt
// must not mark lost a launch whose process is known by now: admission would stop counting a live process.
func TestUncertainExpiryKeepsAProcessFoundMeanwhile(t *testing.T) {
	w := &fakeWorld{markers: map[int]string{100: "m1"}}
	inv, h, regDir := newInventory(t, w)
	r, _, err := h.CreateReceipt("req", baseParams, LaunchIdentity{}, now0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.UpdateReceipt(r.LaunchID, now0, func(cur *Receipt) { cur.State = StateUncertain }); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseOnce)
	var hold atomic.Bool
	hold.Store(true)
	l := NewLauncher(LauncherConfig{Host: h, Discovery: inv.Discovery, Now: func() time.Time {
		// The first reading of the clock is the expiry's decision: hold it there.
		if hold.CompareAndSwap(true, false) {
			close(entered)
			<-release
		}
		return now0.Add(3 * time.Minute)
	}})
	defer l.Close()
	expired := make(chan struct{})
	go func() { l.observe(r.LaunchID); close(expired) }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the expiry never decided")
	}
	w.runtimes = answering(publish(t, regDir, ofLaunch("req")))
	if _, err := inv.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	releaseOnce()
	select {
	case <-expired:
	case <-time.After(10 * time.Second):
		t.Fatal("the expiry did not finish")
	}
	if got, _ := h.Receipt(r.LaunchID); got.State == StateLost || !got.holdsRuntime() || got.PID != 100 {
		t.Errorf("receipt = %+v, want the found process kept", got)
	}
}
