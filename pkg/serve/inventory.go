package serve

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"time"

	"github.com/akopichin/afm/pkg/runregistry"
)

// Ownership says what the host may do with a run: it signals only what it started, forwards to that and to attached
// runs (attached explicitly, or manual runs whose endpoint proved its identity), and only lists everything else.
type Ownership string

const (
	OwnershipManaged    Ownership = "managed"
	OwnershipAttached   Ownership = "attached"
	OwnershipDiscovered Ownership = "discovered"
)

// RunView is one run as the host sees it: a registry record or an attached endpoint with its verdict, or a
// launch that has no process yet.
type RunView struct {
	RunID          string
	PreviousRunIDs []string
	InstanceID     string
	Endpoint       string
	Ownership      Ownership
	// Liveness is empty for a launch that has no process record.
	Liveness Liveness
	Mode     string
	Docker   bool
	Ready    bool
	Project  runregistry.Project
	RunsDir  string
	PID      int
	// ProcessStart is the start marker the record carries; control checks it before every signal.
	ProcessStart string
	// Launch is the receipt of a run this host started.
	Launch *Receipt
}

// Errors of resolving a run_id.
var (
	ErrRunNotFound = errors.New("no such run")
	ErrNotOwned    = errors.New("the run was discovered but not attached; attach it first")
)

// RunChangedError says the run_id named an earlier run of a process that moved on.
type RunChangedError struct{ CurrentRunID string }

func (e *RunChangedError) Error() string {
	return fmt.Sprintf("the run changed; the process now serves %s", e.CurrentRunID)
}

// RunUnavailableError says the run is known but cannot be reached or trusted now.
type RunUnavailableError struct {
	RunID    string
	Liveness Liveness
}

func (e *RunUnavailableError) Error() string {
	return fmt.Sprintf("run %s is %s", e.RunID, e.Liveness)
}

// Inventory joins the process registry with what the host owns: receipts of its launches and attached endpoints.
type Inventory struct {
	Host      *Host
	Discovery *Discovery
	Now       func() time.Time
}

// List returns every run the host knows about, launches first in the order they were accepted. What a registry
// record or an endpoint says about a managed or attached process's runs is saved before it is shown: the record
// may be gone by the next listing.
func (inv *Inventory) List(ctx context.Context) ([]RunView, error) {
	// Read before the probes, so a slower probe that raced with a newer listing cannot move a binding back.
	receipts, err := inv.Host.Receipts()
	if err != nil {
		return nil, err
	}
	attached, err := inv.Host.LoadAttached()
	if err != nil {
		return nil, err
	}
	observed, err := inv.Discovery.Scan(ctx)
	if err != nil {
		return nil, err
	}
	var views []RunView
	seenLaunch := map[string]bool{}
	seenInstance := map[string]bool{}
	for _, obs := range observed {
		view := viewOf(obs)
		seenInstance[view.InstanceID] = true
		if r := receiptFor(receipts, obs.Record.LaunchID); r != nil && recordOfLaunch(*r, obs.Record) {
			bound, err := inv.Host.UpdateReceipt(r.LaunchID, inv.Now(), func(cur *Receipt) { cur.bindRecord(obs.Record, obs.Runtime, r.RunID) })
			if err != nil {
				return nil, err
			}
			view.Ownership, view.Launch = OwnershipManaged, &bound
			view.RunID, view.PreviousRunIDs, view.RunsDir = bound.RunID, bound.PreviousRunIDs, bound.RunsDir
			seenLaunch[r.LaunchID] = true
		} else if a := attachedFor(attached, view.InstanceID); a != nil {
			bound, err := inv.rebind(*a, a.RunID, recordBinding(obs.Record, obs.Runtime))
			if err != nil {
				return nil, err
			}
			view.Ownership = OwnershipAttached
			view.RunID, view.PreviousRunIDs, view.RunsDir = bound.RunID, bound.PreviousRunIDs, bound.RunsDir
		}
		views = append(views, view)
	}
	for i := range receipts {
		if !seenLaunch[receipts[i].LaunchID] {
			views = append(views, launchOnlyView(&receipts[i]))
		}
	}
	for _, a := range attached {
		if a.InstanceID == "" || !seenInstance[a.InstanceID] {
			view, err := inv.attachedView(ctx, a)
			if err != nil {
				return nil, err
			}
			views = append(views, view)
		}
	}
	return views, nil
}

func attachedFor(list []Attached, instanceID string) *Attached {
	if instanceID == "" {
		return nil
	}
	for i := range list {
		if list[i].InstanceID == instanceID {
			return &list[i]
		}
	}
	return nil
}

// rebind merges an observation of an attached instance into attached.json and returns the saved binding. A
// failed write is returned, never hidden behind a view that shows history the host did not keep.
func (inv *Inventory) rebind(a Attached, based string, observed runBinding) (runBinding, error) {
	var bound runBinding
	err := inv.Host.UpdateAttached(func(list []Attached) []Attached {
		if cur := attachedFor(list, a.InstanceID); cur != nil {
			bound = mergeRunBinding(cur.binding(), based, observed)
			cur.setBinding(bound)
		}
		return list
	})
	return bound, err
}

// Resolve finds the run to control or forward to. A run_id of an earlier run of a repeating process is not
// answered with the current run's data: the caller must see that the run changed.
func (inv *Inventory) Resolve(ctx context.Context, runID string) (RunView, error) {
	views, err := inv.List(ctx)
	if err != nil {
		return RunView{}, err
	}
	for _, v := range views {
		if v.RunID != runID {
			continue
		}
		// The liveness reason comes first: a forged or stale record must be refused as untrusted, not as "attach it".
		switch {
		case v.Liveness != LiveActive && v.Liveness != LiveActiveOrphaned:
			return RunView{}, &RunUnavailableError{RunID: runID, Liveness: v.Liveness}
		case v.Ownership == OwnershipDiscovered:
			return RunView{}, ErrNotOwned
		}
		return v, nil
	}
	for _, v := range views {
		if v.Ownership != OwnershipDiscovered && slices.Contains(v.PreviousRunIDs, runID) {
			return RunView{}, &RunChangedError{CurrentRunID: v.RunID}
		}
	}
	return RunView{}, ErrRunNotFound
}

// Find returns a run the host owns (started or attached) whatever its liveness, for reading what it left on
// disk. An earlier run of a repeating process matches too: its directory is a sibling of the current one.
func (inv *Inventory) Find(ctx context.Context, runID string) (RunView, error) {
	return inv.find(ctx, runID, "")
}

func (inv *Inventory) find(ctx context.Context, runID, instanceID string) (RunView, error) {
	views, err := inv.List(ctx)
	if err != nil {
		return RunView{}, err
	}
	for _, v := range views {
		if instanceID != "" && v.InstanceID != instanceID {
			continue
		}
		if v.Ownership != OwnershipDiscovered && (v.RunID == runID || slices.Contains(v.PreviousRunIDs, runID)) {
			return v, nil
		}
	}
	return RunView{}, ErrRunNotFound
}

func viewOf(obs Observation) RunView {
	rec := obs.Record
	v := RunView{
		RunID: rec.RunID, PreviousRunIDs: rec.PreviousRunIDs, InstanceID: rec.InstanceID, Endpoint: rec.APIURL,
		Ownership: OwnershipDiscovered, Liveness: obs.Liveness, Mode: rec.Mode, Docker: rec.Docker, Ready: rec.Ready,
		Project: rec.Project, RunsDir: rec.RunsDir, PID: rec.PID, ProcessStart: rec.ProcessStart,
	}
	// The process is the authority on its current run; the record is only as fresh as its last write.
	if obs.Runtime != nil {
		v.RunID, v.Mode, v.Ready = obs.Runtime.RunID, obs.Runtime.Mode, obs.Runtime.Ready
		if rec.RunID != "" && rec.RunID != v.RunID {
			v.PreviousRunIDs = append(slices.Clone(rec.PreviousRunIDs), rec.RunID)
		}
		// A manual run that proved its identity is attached as listed; a launch belongs only to the host holding
		// its receipt. Nothing is recorded for it: afm_run_attach keeps its runs after it exits.
		if rec.LaunchID == "" {
			v.Ownership = OwnershipAttached
		}
	}
	return v
}

func launchOnlyView(r *Receipt) RunView {
	return RunView{
		RunID: r.RunID, PreviousRunIDs: r.PreviousRunIDs, InstanceID: r.InstanceID, Ownership: OwnershipManaged, Mode: r.Params.Mode,
		RunsDir: r.RunsDir, PID: r.PID, ProcessStart: r.ProcessStart, Launch: r,
	}
}

func receiptFor(receipts []Receipt, launchID string) *Receipt {
	if launchID == "" {
		return nil
	}
	for i := range receipts {
		if receipts[i].LaunchID == launchID {
			return &receipts[i]
		}
	}
	return nil
}

// attachedView judges an attached endpoint that has no registry record by asking it directly.
func (inv *Inventory) attachedView(ctx context.Context, a Attached) (RunView, error) {
	v := RunView{
		RunID: a.RunID, PreviousRunIDs: a.PreviousRunIDs, InstanceID: a.InstanceID, Endpoint: a.Endpoint,
		Ownership: OwnershipAttached, RunsDir: a.RunsDir,
	}
	rt, err := inv.Discovery.probeEndpoint(ctx, a.Endpoint)
	switch {
	case err != nil:
		v.Liveness = LiveUnreachable
	case a.InstanceID != "" && rt.InstanceID != a.InstanceID, a.InstanceID == "" && rt.RunID != a.RunID:
		// The old afm has no instance id to prove it is the same process, so another run on its port is a stranger.
		v.Liveness = LiveIdentityMismatch
	default:
		v.Liveness, v.Mode, v.Ready = LiveActive, rt.Mode, rt.Ready
		if a.InstanceID != "" && rt.RunID != "" && rt.RunID != a.RunID {
			bound, err := inv.rebind(a, a.RunID, runBinding{RunID: rt.RunID, Live: true})
			if err != nil {
				return RunView{}, err
			}
			v.RunID, v.PreviousRunIDs, v.RunsDir = bound.RunID, bound.PreviousRunIDs, bound.RunsDir
		}
	}
	return v, nil
}

// Attach makes a loopback endpoint a controlled run after it proved its identity. The process is only
// remembered: it is neither started nor signalled by the host. Attaching an instance again refreshes its entry
// and keeps the earlier runs it knows.
func (inv *Inventory) Attach(ctx context.Context, endpoint string) (Attached, error) {
	u, err := ParseLoopbackURL(endpoint)
	if err != nil {
		return Attached{}, err
	}
	base := (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
	// Read before the probe, so a reattach that raced with a newer commit cannot move the binding back, and a record
	// written after the answer is never taken for one that precedes it.
	before, err := inv.Host.LoadAttached()
	if err != nil {
		return Attached{}, err
	}
	records, _ := runregistry.List(inv.Discovery.RegistryDir) // only adds history; unreadable means none
	rt, err := inv.Discovery.probeEndpoint(ctx, base)
	if err != nil {
		return Attached{}, fmt.Errorf("attach %s: %w", base, err)
	}
	if rt.RunID == "" {
		return Attached{}, fmt.Errorf("attach %s: the process serves no run", base)
	}
	entry := Attached{Endpoint: base, RunID: rt.RunID, InstanceID: rt.InstanceID, AttachedAt: inv.Now()}
	var based string
	if a := attachedFor(before, entry.InstanceID); a != nil {
		based = a.RunID
	}
	observed := runBinding{RunID: rt.RunID, Live: true}
	for _, rec := range records {
		if rt.InstanceID != "" && rec.InstanceID == rt.InstanceID {
			observed = recordBinding(rec, &rt)
		}
	}
	err = inv.Host.UpdateAttached(func(list []Attached) []Attached {
		var saved runBinding
		for _, old := range list {
			// Without an instance id the same endpoint may serve an unrelated run: nothing is carried over then.
			if entry.InstanceID != "" && sameAttachment(old, entry) {
				saved = old.binding()
			}
		}
		entry.setBinding(mergeRunBinding(saved, based, observed))
		list = slices.DeleteFunc(list, func(a Attached) bool { return sameAttachment(a, entry) })
		return append(list, entry)
	})
	if err != nil {
		return Attached{}, err
	}
	return entry, nil
}

// sameAttachment keys an entry by instance id, or by endpoint when the process has none.
func sameAttachment(a, b Attached) bool {
	if a.InstanceID != "" || b.InstanceID != "" {
		return a.InstanceID == b.InstanceID
	}
	return a.Endpoint == b.Endpoint
}
