package accounting

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// usageFileName is the fixed name of a run's durable usage ledger, always
// directly under the run directory (like events.jsonl / state.json).
const usageFileName = "usage.jsonl"

// ErrCorruptUsage is returned when a complete line in usage.jsonl fails to parse as a UsageRecord.
var ErrCorruptUsage = errors.New("accounting: corrupt usage.jsonl")

// Store is the single live writer of a run's usage.jsonl.
type Store struct {
	mu          sync.Mutex
	f           *os.File
	r           *Resolver
	ledger      Ledger
	unavailable bool
	lastErr     error

	// revision counts every change that can affect a CostSnapshot result: a successful Append, or the store
	// latching unavailable.
	revision       uint64
	cachedRevision uint64
	cachedBundle   *CostBundle
}

// Open opens <runDir>/usage.jsonl for append and replays any existing content into an in-memory Ledger, so
// a resumed run's Snapshot reflects everything recorded before the restart.
func Open(runDir string, r *Resolver) (*Store, error) {
	path := filepath.Join(runDir, usageFileName)

	ledger, loadErr := loadLedger(path)
	if loadErr != nil && !errors.Is(loadErr, ErrCorruptUsage) {
		return nil, loadErr
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}

	s := &Store{f: f, r: r}
	if ledger != nil {
		s.ledger = *ledger
	}
	if loadErr != nil {
		// Corrupt on load: keep the file open (never blocks resume) but stop
		// trusting/extending it.
		s.unavailable = true
		s.lastErr = loadErr
		return s, loadErr
	}
	return s, nil
}

// Load reads a run's usage.jsonl read-only and returns the Ledger it describes. A complete-but-unparseable
// interior line returns ErrCorruptUsage; the file itself is never touched.
func Load(runDir string) (*Ledger, error) {
	l, err := loadLedger(filepath.Join(runDir, usageFileName))
	if err != nil {
		return nil, err
	}
	return l, nil
}

// loadLedger is the single parser shared by Load (read-only) and Open
// (replay-on-resume). It never writes to disk.
func loadLedger(path string) (*Ledger, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Ledger{}, nil
		}
		return nil, err
	}

	l := &Ledger{}
	endsWithNewline := len(data) > 0 && data[len(data)-1] == '\n'
	lines := bytes.Split(data, []byte("\n"))
	for i, line := range lines {
		isLast := i == len(lines)-1
		if isLast {
			if !endsWithNewline {
				// Torn tail: an incomplete final line, never committed with
				// its trailing \n. Benign — ignore it, not corruption.
				break
			}
			if len(line) == 0 {
				// The empty element bytes.Split leaves after a trailing \n.
				continue
			}
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var rec UsageRecord
		if jsonErr := json.Unmarshal(line, &rec); jsonErr != nil {
			return nil, ErrCorruptUsage
		}
		l.Add(rec)
	}
	return l, nil
}

// Append writes and fsyncs usage before updating the ledger. Write failures disable accounting and must
// not fail the stage.
func (s *Store) Append(obs Observation, stageID, phase, scope string) error {
	return s.appendRecord(BuildRecord(s.r, obs, stageID, phase, scope))
}

// AppendTask is Append plus task-cost attribution (T3.8): the SAME pricing logic (BuildRecord), with
// task_id/root_stage_id/parent_task_id/profile_id folded on afterward via BuildTaskRecord. A task's usage
// lands in the SAME flat ledger as every other record — there is no separate "task ledger" to keep in sync,
// which is what lets RunSummary/SummaryByStage keep counting every invocation exactly once without any
// task-specific special-casing.
func (s *Store) AppendTask(obs Observation, meta TaskMeta, phase, scope string) error {
	return s.appendRecord(BuildTaskRecord(s.r, obs, meta, phase, scope))
}

// appendRecord is the single writer both Append and AppendTask funnel through: stamp timestamps, marshal,
// write+fsync, then update the in-memory ledger. Write failures disable accounting and must not fail the
// stage/task that triggered them.
func (s *Store) appendRecord(rec UsageRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.unavailable {
		return s.lastErr
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if rec.StartedAt == "" {
		rec.StartedAt = now
	}
	if rec.FinishedAt == "" {
		rec.FinishedAt = now
	}

	data, err := json.Marshal(rec)
	if err != nil {
		s.markUnavailable(err)
		return err
	}
	data = append(data, '\n')

	if _, err := s.f.Write(data); err != nil {
		s.markUnavailable(err)
		return err
	}
	if err := s.f.Sync(); err != nil {
		s.markUnavailable(err)
		return err
	}

	s.ledger.Add(rec)
	s.revision++
	return nil
}

// markUnavailable must be called with mu held.
func (s *Store) markUnavailable(err error) {
	s.unavailable = true
	s.lastErr = err
	s.revision++
}

// Snapshot returns a point-in-time copy of the ledger accumulated so far —
// safe for the caller to read/aggregate without racing further Appends.
func (s *Store) Snapshot() *Ledger {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &Ledger{recs: append([]UsageRecord(nil), s.ledger.recs...)}
}

// Unavailable reports whether a write/fsync (or a corrupt file found at
// Open) has permanently disabled further writes for this Store.
func (s *Store) Unavailable() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.unavailable
}

// Close closes the underlying file handle.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.f.Close()
}
