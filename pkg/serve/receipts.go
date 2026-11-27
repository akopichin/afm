package serve

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/akopichin/afm/pkg/runregistry"
)

// Receipt states. uncertain is non-terminal: the host crashed after accepting but before it knew the child's pid.
const (
	StateAccepted  = "accepted"
	StateStarting  = "starting"
	StateReady     = "ready"
	StateFailed    = "failed"
	StateUncertain = "uncertain"
	StateLost      = "lost"
	StateExited    = "exited"
)

// ErrRequestConflict means a request_id was reused with different parameters.
var ErrRequestConflict = errors.New("request_id was already used with different parameters")

// LaunchParams are the typed, allowlist-resolved parameters of a start request. The task text is kept only as
// its digest; the text itself lives in the tasks directory.
type LaunchParams struct {
	MatchingImage bool     `json:"matching_image,omitempty"`
	Project       string   `json:"project"`
	Pipeline      string   `json:"pipeline,omitempty"`
	Flow          string   `json:"flow,omitempty"`
	Mode          string   `json:"mode"`
	Worktree      string   `json:"worktree,omitempty"`
	NoWorktree    bool     `json:"no_worktree,omitempty"`
	MCPAllow      []string `json:"mcp_allow,omitempty"`
	TaskSHA256    string   `json:"task_sha256,omitempty"`
}

// Receipt is the durable record of one accepted start request, stored in launches/<launch_id>.json.
type Receipt struct {
	SchemaVersion int          `json:"schema_version"`
	LaunchID      string       `json:"launch_id"`
	RequestID     string       `json:"request_id"`
	ParamsHash    string       `json:"params_hash"`
	Params        LaunchParams `json:"params"`
	// Identity is what admission compared when the request was accepted; it never follows later config edits.
	Identity LaunchIdentity `json:"identity,omitzero"`
	State    string         `json:"state"`
	// ProcessGone is proof that the launch's process no longer runs; until then a failed launch still holds
	// its running slot and its identity.
	ProcessGone  bool   `json:"process_gone,omitempty"`
	PID          int    `json:"pid,omitempty"`
	ProcessStart string `json:"process_start,omitempty"`
	InstanceID   string `json:"instance_id,omitempty"`
	RunID        string `json:"run_id,omitempty"`
	// PreviousRunIDs are the earlier repeat cycles of the process, kept so their reports resolve after it exits.
	PreviousRunIDs []string  `json:"previous_run_ids,omitempty"`
	RunsDir        string    `json:"runs_dir,omitempty"`
	ExitCode       *int      `json:"exit_code,omitempty"`
	Error          string    `json:"error,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// LaunchIdentity is the checkout a launch works in and the name of its run directory: two launches with the same
// identity would run the same flow over each other. The zero value is an identity that is not known.
type LaunchIdentity struct {
	Checkout string `json:"checkout"`
	RunName  string `json:"run_name"`
}

// startingSlot reports whether the launch counts against max_starting.
func (r Receipt) startingSlot() bool {
	return r.State == StateAccepted || r.State == StateStarting || r.State == StateUncertain
}

// holdsRuntime reports whether the launch's process may still run: such a launch counts against max_running and
// keeps its identity, even after its start failed, until its exit is proven.
func (r Receipt) holdsRuntime() bool {
	switch r.State {
	case StateAccepted, StateStarting, StateUncertain, StateReady:
		return true
	case StateFailed:
		return r.PID != 0 && !r.ProcessGone
	default:
		return false
	}
}

// LaunchID is the stable child identity derived from the client's request_id.
func LaunchID(requestID string) string {
	sum := sha256.Sum256([]byte(requestID))
	return hex.EncodeToString(sum[:])
}

// TaskDigest is the value stored in LaunchParams.TaskSHA256; an empty task has no digest.
func TaskDigest(task string) string {
	if task == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(task))
	return hex.EncodeToString(sum[:])
}

// ParamsHash identifies the canonical parameters: the order of MCPAllow does not matter.
func ParamsHash(p LaunchParams) string {
	p.MCPAllow = slices.Sorted(slices.Values(p.MCPAllow))
	data, _ := json.Marshal(p) // a struct of strings cannot fail to marshal
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// CreateReceipt records an accepted request and its admitted identity atomically (temp, fsync, link). The same
// request_id with the same parameters returns the existing receipt, with others ErrRequestConflict; the identity
// is not compared, since it may resolve differently later.
func (h *Host) CreateReceipt(requestID string, params LaunchParams, identity LaunchIdentity, now time.Time) (r Receipt, created bool, err error) {
	if err := h.lockWriter(); err != nil {
		return Receipt{}, false, err
	}
	defer h.mu.Unlock()
	launchID := LaunchID(requestID)
	r = Receipt{
		SchemaVersion: stateSchemaVer,
		LaunchID:      launchID,
		RequestID:     requestID,
		ParamsHash:    ParamsHash(params),
		Params:        params,
		Identity:      identity,
		State:         StateAccepted,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	created, err = h.linkReceipt(r)
	if err != nil {
		return Receipt{}, false, err
	}
	if created {
		return r, true, nil
	}
	existing, err := h.readReceipt(launchID)
	if err != nil {
		return Receipt{}, false, err
	}
	if existing.ParamsHash != r.ParamsHash {
		return existing, false, ErrRequestConflict
	}
	return existing, false, nil
}

// UpdateReceipt applies fn to a receipt and rewrites it atomically.
func (h *Host) UpdateReceipt(launchID string, now time.Time, fn func(*Receipt)) (Receipt, error) {
	if err := h.lockWriter(); err != nil {
		return Receipt{}, err
	}
	defer h.mu.Unlock()
	r, err := h.readReceipt(launchID)
	if err != nil {
		return Receipt{}, err
	}
	before := r
	fn(&r)
	if reflect.DeepEqual(before, r) {
		return r, nil
	}
	r.UpdatedAt = now
	if err := writeJSON(h.receiptPath(launchID), r); err != nil {
		return Receipt{}, err
	}
	return r, nil
}

// Receipt returns one receipt; os.ErrNotExist means the launch is unknown.
func (h *Host) Receipt(launchID string) (Receipt, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.readReceipt(launchID)
}

// Receipts lists all receipts, oldest first.
func (h *Host) Receipts() ([]Receipt, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	entries, err := os.ReadDir(filepath.Join(h.dir, launchesDirName))
	if err != nil {
		return nil, err
	}
	var out []Receipt
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		r, err := h.readReceipt(id)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	slices.SortStableFunc(out, func(a, b Receipt) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return out, nil
}

func (h *Host) receiptPath(launchID string) string {
	return filepath.Join(h.dir, launchesDirName, launchID+".json")
}

func (h *Host) readReceipt(launchID string) (Receipt, error) {
	var r Receipt
	if err := readJSON(h.receiptPath(launchID), &r); err != nil {
		return Receipt{}, err
	}
	return r, nil
}

// linkReceipt publishes r under its final name only if nobody has yet; false means the name already exists.
func (h *Host) linkReceipt(r Receipt) (bool, error) {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(filepath.Join(h.dir, launchesDirName), "."+r.LaunchID+".tmp-*")
	if err != nil {
		return false, fmt.Errorf("create receipt temp: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // gone already after a successful link
	_, err = tmp.Write(append(data, '\n'))
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return false, fmt.Errorf("write receipt temp: %w", err)
	}
	err = os.Link(tmp.Name(), h.receiptPath(r.LaunchID))
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("publish receipt: %w", err)
	}
	return true, syncDir(filepath.Join(h.dir, launchesDirName))
}

func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// maxRunHistory bounds the earlier run ids kept for one process.
const maxRunHistory = 64

// runBinding is what the host knows about the runs of one process: the current run, its earlier repeat cycles
// and where their directories are.
type runBinding struct {
	RunID          string
	PreviousRunIDs []string
	RunsDir        string
	// Live means RunID is what the process itself answered after the caller read the saved binding.
	Live bool
}

// mergeRunBinding folds an observation into the saved binding (history only grows); based is the saved run read
// before observing. A run is current only if provably newer; one that lost the race stays in the history, and only
// a live answer given after the last commit takes a run back out of it, repairing a raced run that was newer.
func mergeRunBinding(saved runBinding, based string, observed runBinding) runBinding {
	out := runBinding{RunID: saved.RunID, RunsDir: saved.RunsDir}
	if observed.RunsDir != "" {
		out.RunsDir = observed.RunsDir
	}
	history := append(slices.Clone(saved.PreviousRunIDs), observed.PreviousRunIDs...)
	switch {
	case observed.RunID == "" || observed.RunID == saved.RunID:
		// Nothing new about the current run.
	case saved.RunID == "" || slices.Contains(observed.PreviousRunIDs, saved.RunID),
		saved.RunID == based && (observed.Live || !slices.Contains(saved.PreviousRunIDs, observed.RunID)):
		history = append(history, saved.RunID)
		out.RunID = observed.RunID
	default:
		history = append(history, observed.RunID)
	}
	for _, id := range history {
		if id != "" && id != out.RunID && !slices.Contains(out.PreviousRunIDs, id) {
			out.PreviousRunIDs = append(out.PreviousRunIDs, id)
		}
	}
	if len(out.PreviousRunIDs) > maxRunHistory {
		out.PreviousRunIDs = out.PreviousRunIDs[len(out.PreviousRunIDs)-maxRunHistory:]
	}
	return out
}

func (r *Receipt) binding() runBinding {
	return runBinding{RunID: r.RunID, PreviousRunIDs: r.PreviousRunIDs, RunsDir: r.RunsDir}
}

func (r *Receipt) setBinding(b runBinding) {
	r.RunID, r.PreviousRunIDs, r.RunsDir = b.RunID, b.PreviousRunIDs, b.RunsDir
}

// recordBinding is what a registry record says about its process's runs; runtime, when the endpoint answered,
// is fresher than the record about the current run.
func recordBinding(rec runregistry.Record, runtime *Runtime) runBinding {
	b := runBinding{RunID: rec.RunID, PreviousRunIDs: rec.PreviousRunIDs, RunsDir: rec.RunsDir}
	if runtime == nil || runtime.RunID == "" {
		return b
	}
	b.Live = true
	if runtime.RunID != rec.RunID {
		b.PreviousRunIDs = append(slices.Clone(rec.PreviousRunIDs), rec.RunID)
		b.RunID = runtime.RunID
	}
	return b
}

// recordOfLaunch reports whether a registry record belongs to the launch's own process: the launch and instance
// ids must match, and so must the process once the receipt knows it.
func recordOfLaunch(r Receipt, rec runregistry.Record) bool {
	if rec.LaunchID != r.LaunchID || rec.InstanceID != InstanceIDFor(r.LaunchID) {
		return false
	}
	return r.PID == 0 || (rec.PID == r.PID && (r.ProcessStart == "" || rec.ProcessStart == r.ProcessStart))
}

// mergeObserved folds what the launch's own record, and the endpoint that answered for it, say about its runs into
// the receipt and leaves its state alone; a record of another process is ignored.
func (r *Receipt) mergeObserved(rec runregistry.Record, runtime *Runtime, based string) {
	if recordOfLaunch(*r, rec) {
		r.setBinding(mergeRunBinding(r.binding(), based, recordBinding(rec, runtime)))
	}
}

// bindRecord merges what the launch's own record says into the receipt: its process, its runs directory and its
// runs. based is the receipt's current run as the caller saw it before observing.
func (r *Receipt) bindRecord(rec runregistry.Record, runtime *Runtime, based string) {
	if r.PID == 0 {
		r.PID, r.ProcessStart = rec.PID, rec.ProcessStart
	}
	r.InstanceID = rec.InstanceID
	r.setBinding(mergeRunBinding(r.binding(), based, recordBinding(rec, runtime)))
}
