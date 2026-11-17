package serve

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/akopichin/afm/contracts/fsutil"
)

const (
	hostLockFile     = "host.lock"
	hostInfoFile     = "host.json"
	attachedFile     = "attached.json"
	launchesDirName  = "launches"
	logsDirName      = "logs"
	tasksDirName     = "tasks"
	stateDirMode     = 0o700
	stateFileMode    = 0o600
	stateSchemaVer   = 1
	staleTmpGlobName = ".*.tmp-*"
)

var errLocked = errors.New("host lock is held")

// errHostClosed refuses a write after Close: the lock is released, and the next host may already own the state.
var errHostClosed = errors.New("the host has released its state directory")

// HostInfo is host.json: who owns the state directory and where its MCP gateway listens.
type HostInfo struct {
	PID       int       `json:"pid"`
	Endpoint  string    `json:"endpoint,omitempty"`
	StartedAt time.Time `json:"started_at"`
}

// LockedError reports that another host owns the state directory; Info is what that host wrote to host.json.
type LockedError struct{ Info HostInfo }

func (e *LockedError) Error() string {
	return fmt.Sprintf("afm serve is already running (pid %d, endpoint %q)", e.Info.PID, e.Info.Endpoint)
}

// Attached is an externally started run the host only observes.
type Attached struct {
	Endpoint   string `json:"endpoint"`
	RunID      string `json:"run_id"`
	InstanceID string `json:"instance_id,omitempty"`
	// RunsDir is where the run's directories live, when a registry record told; afm_run_report reads from it.
	RunsDir string `json:"runs_dir,omitempty"`
	// PreviousRunIDs are the earlier repeat cycles the host learned of; attaching again keeps them.
	PreviousRunIDs []string  `json:"previous_run_ids,omitempty"`
	AttachedAt     time.Time `json:"attached_at"`
}

func (a *Attached) binding() runBinding {
	return runBinding{RunID: a.RunID, PreviousRunIDs: a.PreviousRunIDs, RunsDir: a.RunsDir}
}

func (a *Attached) setBinding(b runBinding) {
	a.RunID, a.PreviousRunIDs, a.RunsDir = b.RunID, b.PreviousRunIDs, b.RunsDir
}

// DefaultDir is ~/.afm/serve.
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("serve state dir: %w", err)
	}
	return filepath.Join(home, ".afm", "serve"), nil
}

// Host is the single writer of the state directory; it holds host.lock until Close.
type Host struct {
	dir  string
	lock *os.File

	mu     sync.Mutex // serializes in-process writers; host.lock covers other processes
	closed bool       // set by Close under mu, so no write lands after the lock is released
	info   HostInfo
}

// AcquireHost creates the state directory (0700), takes host.lock and publishes host.json. When another host
// holds the lock the error is a *LockedError carrying that host's host.json.
func AcquireHost(dir string, pid int, now time.Time) (*Host, error) {
	for _, sub := range []string{"", launchesDirName, logsDirName, tasksDirName} {
		if err := mkdirPrivate(filepath.Join(dir, sub)); err != nil {
			return nil, err
		}
	}
	lock, err := lockFile(filepath.Join(dir, hostLockFile))
	if errors.Is(err, errLocked) {
		return nil, &LockedError{Info: readHostInfo(dir)}
	}
	if err != nil {
		return nil, err
	}
	h := &Host{dir: dir, lock: lock}
	if err := h.removeStaleTmp(); err != nil {
		h.release()
		return nil, err
	}
	h.info = HostInfo{PID: pid, StartedAt: now}
	if err := writeJSON(filepath.Join(dir, hostInfoFile), h.info); err != nil {
		h.release()
		return nil, err
	}
	return h, nil
}

// SetEndpoint rewrites host.json; the endpoint is known only after the gateway listens.
func (h *Host) SetEndpoint(endpoint string) error {
	if err := h.lockWriter(); err != nil {
		return err
	}
	defer h.mu.Unlock()
	h.info.Endpoint = endpoint
	return writeJSON(filepath.Join(h.dir, hostInfoFile), h.info)
}

// Close removes host.json and releases the lock once the writes in flight are done; later writes are refused.
// The children keep running, and their reapers keep waiting for them, but only the next host records their end.
func (h *Host) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil
	}
	h.closed = true
	err := os.Remove(filepath.Join(h.dir, hostInfoFile))
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	h.release()
	return err
}

// lockWriter takes the writer lock of a host that still owns the state directory; the caller unlocks h.mu.
func (h *Host) lockWriter() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return errHostClosed
	}
	return nil
}

func (h *Host) release() { h.lock.Close() }

// LogPath and TaskPath are fixed by launch id so recovery finds them without extra records.
func (h *Host) LogPath(launchID string) string {
	return filepath.Join(h.dir, logsDirName, launchID+".log")
}

func (h *Host) TaskPath(launchID string) string {
	return filepath.Join(h.dir, tasksDirName, launchID+".md")
}

// LoadAttached returns the recorded attached runs; none recorded is not an error.
func (h *Host) LoadAttached() ([]Attached, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var doc attachedDoc
	err := readJSON(filepath.Join(h.dir, attachedFile), &doc)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return doc.Attached, err
}

// UpdateAttached applies change to the recorded attached runs and saves the result. The read, the change and
// the write happen under one lock, so concurrent updates never overwrite each other.
func (h *Host) UpdateAttached(change func(list []Attached) []Attached) error {
	if err := h.lockWriter(); err != nil {
		return err
	}
	defer h.mu.Unlock()
	var doc attachedDoc
	if err := readJSON(filepath.Join(h.dir, attachedFile), &doc); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	list := change(doc.Attached)
	return writeJSON(filepath.Join(h.dir, attachedFile), attachedDoc{SchemaVersion: stateSchemaVer, Attached: list})
}

type attachedDoc struct {
	SchemaVersion int        `json:"schema_version"`
	Attached      []Attached `json:"attached"`
}

// removeStaleTmp drops receipt temp files left by a crash between write and link; the lock makes this safe.
func (h *Host) removeStaleTmp() error {
	stale, err := filepath.Glob(filepath.Join(h.dir, launchesDirName, staleTmpGlobName))
	if err != nil {
		return err
	}
	for _, p := range stale {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale receipt temp: %w", err)
		}
	}
	return nil
}

func mkdirPrivate(path string) error {
	if err := os.MkdirAll(path, stateDirMode); err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if err := os.Chmod(path, stateDirMode); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	return nil
}

func readHostInfo(dir string) HostInfo {
	var info HostInfo
	_ = readJSON(filepath.Join(dir, hostInfoFile), &info) // best effort: the holder may not have written it yet
	return info
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	return nil
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.AtomicWriteFile(path, append(data, '\n'), stateFileMode)
}
