package serve

import (
	"context"
	"errors"
	"io/fs"
	"slices"
	"syscall"
	"time"

	"github.com/akopichin/afm/pkg/runregistry"
)

// Liveness is what the host could prove about a registered afm process.
type Liveness string

const (
	// LiveActive: the endpoint answers as the registered instance.
	LiveActive Liveness = "active"
	// LiveActiveOrphaned: a Docker run whose host-side afm is gone while its container still answers.
	LiveActiveOrphaned Liveness = "active_orphaned"
	// LiveUnreachable: the process is alive (or the lease is fresh) but the endpoint does not answer.
	LiveUnreachable Liveness = "unreachable"
	// LiveStale: the lease expired and nothing proves the process alive or dead.
	LiveStale Liveness = "stale"
	// LiveExited: the process is gone and its endpoint does not answer as that instance.
	LiveExited Liveness = "exited"
	// LiveIdentityMismatch: the endpoint answers as another instance, so the port or PID was reused.
	LiveIdentityMismatch Liveness = "identity_mismatch"
)

const (
	defaultLeaseTTL     = 2 * time.Minute
	defaultReprobeDelay = time.Second
)

// Discovery reads the process registry and judges each record against the live system. The probes are fields so
// tests can describe any combination of endpoint and process state.
type Discovery struct {
	RegistryDir string
	// Probe asks an endpoint for its identity; nil means ProbeRuntime.
	Probe func(ctx context.Context, endpoint string) (Runtime, error)
	// ProcessStart returns the start marker of a PID; nil means runregistry.ProcessStart.
	ProcessStart func(pid int) (string, error)
	Now          func() time.Time
	// LeaseTTL is how long a record's last-seen time counts as fresh; zero means two minutes.
	LeaseTTL time.Duration
	// ReprobeDelay separates the two probes made before a run is called stale, so a probe that stalled while the
	// machine slept does not decide; zero means one second.
	ReprobeDelay time.Duration
}

// Observation is a registry record with the host's verdict.
type Observation struct {
	Record   runregistry.Record
	Liveness Liveness
	// Runtime is set when the endpoint answered as the record's instance.
	Runtime *Runtime
}

type processState int

const (
	processUnknown processState = iota
	processAlive
	processGone
)

// Scan classifies every readable record.
func (d *Discovery) Scan(ctx context.Context) ([]Observation, error) {
	records, err := runregistry.List(d.RegistryDir)
	if err != nil {
		return nil, err
	}
	out := make([]Observation, 0, len(records))
	for _, rec := range records {
		out = append(out, d.Classify(ctx, rec))
	}
	return out, nil
}

// Classify judges one record. A dead PID alone never proves a run gone when its endpoint answers, and a live
// endpoint of another instance is never taken for the record's process.
func (d *Discovery) Classify(ctx context.Context, rec runregistry.Record) Observation {
	obs := Observation{Record: rec}
	rt, answered := d.probe(ctx, rec)
	proc := d.processState(rec)
	switch {
	case answered && rt.InstanceID != rec.InstanceID:
		obs.Liveness = LiveIdentityMismatch
	case answered:
		obs.Runtime = &rt
		obs.Liveness = LiveActive
		if rec.Docker && proc == processGone {
			obs.Liveness = LiveActiveOrphaned
		}
	case proc == processGone:
		obs.Liveness = LiveExited
	case proc == processAlive || !d.leaseExpired(rec):
		obs.Liveness = LiveUnreachable
	default:
		return d.reprobe(ctx, obs)
	}
	return obs
}

// reprobe gives a run whose lease expired one more chance to answer before it is called stale.
func (d *Discovery) reprobe(ctx context.Context, obs Observation) Observation {
	delay := d.ReprobeDelay
	if delay == 0 {
		delay = defaultReprobeDelay
	}
	select {
	case <-time.After(delay):
	case <-ctx.Done():
	}
	if rt, answered := d.probe(ctx, obs.Record); answered && rt.InstanceID == obs.Record.InstanceID {
		obs.Runtime = &rt
		obs.Liveness = LiveActive
		return obs
	}
	obs.Liveness = LiveStale
	return obs
}

// Reap removes the record of an exited process after re-reading and re-judging it: a process that replaced the old
// one rewrites the same file. keep saves what the recheck observed, live or not, before the record may go; a record
// that changed since obs, or that keep could not save, stays.
func (d *Discovery) Reap(ctx context.Context, obs Observation, keep func(Observation) error) (bool, error) {
	if obs.Liveness != LiveExited {
		return false, nil
	}
	records, err := runregistry.List(d.RegistryDir)
	if err != nil {
		return false, err
	}
	for _, rec := range records {
		if rec.InstanceID != obs.Record.InstanceID {
			continue
		}
		if !sameRecord(rec, obs.Record) {
			return false, nil
		}
		again := d.Classify(ctx, rec)
		if err := keep(again); err != nil {
			return false, err
		}
		if again.Liveness != LiveExited {
			return false, nil
		}
		return true, runregistry.Remove(d.RegistryDir, rec.InstanceID)
	}
	return false, nil
}

// sameRecord reports whether two reads of a record describe the same process with the same runs.
func sameRecord(a, b runregistry.Record) bool {
	return a.PID == b.PID && a.ProcessStart == b.ProcessStart && a.LaunchID == b.LaunchID && a.RunID == b.RunID &&
		a.RunsDir == b.RunsDir && slices.Equal(a.PreviousRunIDs, b.PreviousRunIDs)
}

func (d *Discovery) probe(ctx context.Context, rec runregistry.Record) (Runtime, bool) {
	if rec.APIURL == "" {
		return Runtime{}, false
	}
	rt, err := d.probeEndpoint(ctx, rec.APIURL)
	return rt, err == nil
}

func (d *Discovery) probeEndpoint(ctx context.Context, endpoint string) (Runtime, error) {
	if d.Probe == nil {
		return ProbeRuntime(ctx, endpoint)
	}
	return d.Probe(ctx, endpoint)
}

func (d *Discovery) processState(rec runregistry.Record) processState {
	start := d.ProcessStart
	if start == nil {
		start = runregistry.ProcessStart
	}
	marker, err := start(rec.PID)
	switch {
	case errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH):
		return processGone
	case err != nil || rec.ProcessStart == "":
		return processUnknown
	case marker == rec.ProcessStart:
		return processAlive
	default:
		return processGone
	}
}

func (d *Discovery) leaseExpired(rec runregistry.Record) bool {
	ttl := d.LeaseTTL
	if ttl == 0 {
		ttl = defaultLeaseTTL
	}
	return d.Now().Sub(rec.UpdatedAt) > ttl
}
