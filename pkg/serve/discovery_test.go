package serve

import (
	"context"
	"errors"
	"io/fs"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/runregistry"
)

const (
	testInstance  = "0123456789abcdef0123456789abcdef"
	otherInstance = "fedcba9876543210fedcba9876543210"
)

var now0 = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

type fakeWorld struct {
	runtimes map[string]Runtime // by endpoint; absent means connection refused
	markers  map[int]string     // by pid; absent means no such process
	unknown  bool               // process lookup is unsupported
	probes   int
	now      time.Time
}

func (w *fakeWorld) discovery(dir string) *Discovery {
	return &Discovery{
		RegistryDir: dir,
		Probe: func(_ context.Context, endpoint string) (Runtime, error) {
			w.probes++
			if rt, ok := w.runtimes[endpoint]; ok {
				return rt, nil
			}
			return Runtime{}, errors.New("connection refused")
		},
		ProcessStart: func(pid int) (string, error) {
			if w.unknown {
				return "", runregistry.ErrUnsupported
			}
			if m, ok := w.markers[pid]; ok {
				return m, nil
			}
			return "", fs.ErrNotExist
		},
		Now:          func() time.Time { return w.now },
		LeaseTTL:     time.Minute,
		ReprobeDelay: time.Millisecond,
	}
}

func keepNothing(Observation) error { return nil }

func baseRecord() runregistry.Record {
	return runregistry.Record{
		InstanceID: testInstance, PID: 100, ProcessStart: "m1", Mode: "core",
		APIURL: "http://127.0.0.1:9001", RunID: "run-1", UpdatedAt: now0,
	}
}

func TestClassify(t *testing.T) {
	answers := Runtime{ProtocolVersion: 1, RunID: "run-1", InstanceID: testInstance, Ready: true}
	tests := []struct {
		name   string
		mutate func(*runregistry.Record)
		world  fakeWorld
		want   Liveness
	}{
		{name: "endpoint answers the same instance", world: fakeWorld{
			runtimes: map[string]Runtime{"http://127.0.0.1:9001": answers}, markers: map[int]string{100: "m1"}}, want: LiveActive},
		{name: "port reused by another afm", world: fakeWorld{
			runtimes: map[string]Runtime{"http://127.0.0.1:9001": {ProtocolVersion: 1, InstanceID: otherInstance}}, markers: map[int]string{100: "m1"}}, want: LiveIdentityMismatch},
		{name: "port reused by an afm without instance id", world: fakeWorld{
			runtimes: map[string]Runtime{"http://127.0.0.1:9001": {ProtocolVersion: 1, RunID: "run-1"}}, markers: map[int]string{100: "m1"}}, want: LiveIdentityMismatch},
		{name: "docker host process gone but container answers", mutate: func(r *runregistry.Record) { r.Docker = true }, world: fakeWorld{
			runtimes: map[string]Runtime{"http://127.0.0.1:9001": answers}}, want: LiveActiveOrphaned},
		{name: "process alive, endpoint silent", world: fakeWorld{markers: map[int]string{100: "m1"}}, want: LiveUnreachable},
		{name: "process gone, endpoint silent", world: fakeWorld{}, want: LiveExited},
		{name: "pid reused by another process", world: fakeWorld{markers: map[int]string{100: "other"}}, want: LiveExited},
		{name: "no marker support, fresh lease", world: fakeWorld{unknown: true}, want: LiveUnreachable},
		{name: "no marker support, lease expired", mutate: func(r *runregistry.Record) { r.UpdatedAt = now0.Add(-time.Hour) },
			world: fakeWorld{unknown: true}, want: LiveStale},
		{name: "record without endpoint, process alive", mutate: func(r *runregistry.Record) { r.APIURL = "" },
			world: fakeWorld{markers: map[int]string{100: "m1"}}, want: LiveUnreachable},
		{name: "record points off loopback", mutate: func(r *runregistry.Record) { r.APIURL = "http://192.0.2.9:80" },
			world: fakeWorld{markers: map[int]string{100: "m1"}}, want: LiveUnreachable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.world.now = now0
			rec := baseRecord()
			if tt.mutate != nil {
				tt.mutate(&rec)
			}
			got := tt.world.discovery(t.TempDir()).Classify(context.Background(), rec)
			if got.Liveness != tt.want {
				t.Errorf("liveness = %s, want %s", got.Liveness, tt.want)
			}
		})
	}
}

// After a laptop wakes the lease is long expired, yet the endpoint answers: the second probe must win.
func TestClassifyReprobesBeforeCallingARunStale(t *testing.T) {
	w := &fakeWorld{unknown: true, now: now0.Add(time.Hour), runtimes: map[string]Runtime{}}
	d := w.discovery(t.TempDir())
	calls := 0
	d.Probe = func(context.Context, string) (Runtime, error) {
		calls++
		if calls == 1 {
			return Runtime{}, errors.New("stalled after wake")
		}
		return Runtime{ProtocolVersion: 1, RunID: "run-1", InstanceID: testInstance}, nil
	}
	got := d.Classify(context.Background(), baseRecord())
	if got.Liveness != LiveActive || calls != 2 {
		t.Errorf("liveness = %s after %d probes, want active after 2", got.Liveness, calls)
	}
}

func TestReapRemovesOnlyProvenExit(t *testing.T) {
	dir := t.TempDir()
	rec := baseRecord()
	if err := runregistry.Publish(dir, rec); err != nil {
		t.Fatal(err)
	}
	w := &fakeWorld{now: now0, markers: map[int]string{100: "m1"}}
	d := w.discovery(dir)

	obs := d.Classify(context.Background(), rec)
	if removed, _ := d.Reap(context.Background(), obs, keepNothing); removed || obs.Liveness != LiveUnreachable {
		t.Fatalf("an unreachable record must stay: removed=%v liveness=%s", removed, obs.Liveness)
	}

	w.markers = nil
	obs = d.Classify(context.Background(), rec)
	removed, err := d.Reap(context.Background(), obs, keepNothing)
	if err != nil || !removed {
		t.Fatalf("exited record: removed=%v err=%v", removed, err)
	}
	if list, _ := runregistry.List(dir); len(list) != 0 {
		t.Errorf("record still there: %v", list)
	}
}

// The process may have been replaced between the scan and the removal: the record is re-read and re-judged.
func TestReapKeepsARecordThatChangedMeanwhile(t *testing.T) {
	dir := t.TempDir()
	old := baseRecord()
	w := &fakeWorld{now: now0}
	d := w.discovery(dir)
	obs := d.Classify(context.Background(), old)
	if obs.Liveness != LiveExited {
		t.Fatalf("setup: liveness = %s", obs.Liveness)
	}
	fresh := old
	fresh.PID, fresh.ProcessStart = 200, "m2"
	if err := runregistry.Publish(dir, fresh); err != nil {
		t.Fatal(err)
	}
	w.markers = map[int]string{200: "m2"}
	if removed, _ := d.Reap(context.Background(), obs, keepNothing); removed {
		t.Error("a record rewritten by a live process was removed")
	}
	if list, _ := runregistry.List(dir); len(list) != 1 {
		t.Errorf("records = %v", list)
	}
}

func TestScanClassifiesEveryRecord(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{testInstance, otherInstance} {
		rec := baseRecord()
		rec.InstanceID = id
		if err := runregistry.Publish(dir, rec); err != nil {
			t.Fatal(err)
		}
	}
	w := &fakeWorld{now: now0}
	obs, err := w.discovery(dir).Scan(context.Background())
	if err != nil || len(obs) != 2 {
		t.Fatalf("Scan = %v, %v", obs, err)
	}
	for _, o := range obs {
		if o.Liveness != LiveExited {
			t.Errorf("%s: %s", o.Record.InstanceID, o.Liveness)
		}
	}
}
