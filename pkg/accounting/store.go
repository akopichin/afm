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

// ErrCorruptUsage is returned when a complete (newline-terminated) line in
// usage.jsonl fails to parse as a UsageRecord. Unlike a torn tail (the
// benign, expected shape of a crash or a concurrent live writer mid-append),
// this means the file itself is damaged. The original file is never
// modified when this happens — accounting for the run is simply treated as
// unavailable from then on; this must never block FSM resume.
var ErrCorruptUsage = errors.New("accounting: corrupt usage.jsonl")

// Store is the single live writer of a run's usage.jsonl. The run's own
// .lock (held elsewhere by state.Store for the run directory) already
// guarantees there is only ever one live writer at a time, so Store takes no
// flock of its own — it just opens the file.
type Store struct {
	mu          sync.Mutex
	f           *os.File
	r           *Resolver
	ledger      Ledger
	unavailable bool
	lastErr     error
}

// Open opens (creating if needed) <runDir>/usage.jsonl for append and
// replays any existing content into an in-memory Ledger, so a resumed run's
// Snapshot reflects everything recorded before the restart.
//
// A pre-existing torn tail (a crash mid-append, or a reader racing a live
// writer) is tolerated silently, same as Load. A pre-existing CORRUPT
// interior line is different: it means usage.jsonl itself is damaged, so the
// returned Store starts Unavailable (further Append calls are no-ops) and
// Open still returns the file handle open — accounting must never block the
// run itself — along with the wrapped ErrCorruptUsage for the caller to log.
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

// Load reads a run's usage.jsonl read-only and returns the Ledger it
// describes. A missing file is not an error — it means no usage has been
// recorded yet (or accounting was disabled for the run) — and yields an
// empty Ledger. A torn tail (final line without a trailing newline) is
// ignored. A complete-but-unparseable interior line returns ErrCorruptUsage;
// the file itself is never touched.
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

// Append builds the full UsageRecord for one observation, appends it to
// usage.jsonl and fsyncs it, and ONLY THEN applies it to the in-memory
// ledger — a crash between the write and the fsync must never leave the
// ledger ahead of what's durably on disk. A write/fsync error marks the
// store unavailable (all further Append calls become no-ops returning the
// same error) and is returned to the caller for logging; this is
// observability only — the caller must not treat it as an FSM/agent error.
func (s *Store) Append(obs Observation, stageID, phase, scope string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.unavailable {
		return s.lastErr
	}

	rec := BuildRecord(s.r, obs, stageID, phase, scope)
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
	return nil
}

// markUnavailable must be called with mu held.
func (s *Store) markUnavailable(err error) {
	s.unavailable = true
	s.lastErr = err
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

// Close closes the underlying file handle. It does not fsync the parent
// directory — usage.jsonl is a best-effort accounting artifact, not the
// run's authoritative log (that durability guarantee belongs to
// events.jsonl in pkg/state).
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.f.Close()
}
