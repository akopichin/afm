package agentjobs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// journalFile is the part of *os.File the writer uses; tests inject disk failures through it.
type journalFile interface {
	Write(p []byte) (int, error)
	Sync() error
	Truncate(size int64) error
	Close() error
}

// Store is the single live writer of a journal. Write failures latch it unavailable
// and never fail the agent run that triggered them.
type Store struct {
	mu     sync.Mutex
	f      journalFile // nil once the store is unavailable or closed
	j      *journal
	err    error // why the store is unavailable
	closed bool
}

// Open opens <dir>/agent-jobs.jsonl for append. A new file gets a header from h; an existing
// header is kept and a torn tail is cut off. The Store is never nil: with a non-nil err it
// is unavailable (see Err) and Append only reports that.
func Open(dir string, h Header) (*Store, error) {
	path := filepath.Join(dir, FileName)
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return unavailable(newJournal(), ReasonIOFailed, err)
	}

	j, validEnd := replay(data)
	fresh := len(bytes.TrimSpace(data[:validEnd])) == 0
	if fresh {
		// A crash between creating the file and writing its header leaves nothing to keep.
		j, validEnd = newJournal(), 0
		if h.RecordingStartedAt.IsZero() || h.BaseOffsetMS < 0 {
			return unavailable(j, ReasonCorruptHeader, fmt.Errorf("%w: header needs a start time", ErrInvalidRecord))
		}
		j.header = newHeader(h.RecordingStartedAt, h.BaseOffsetMS)
	} else if health := j.health(); health.Status == HealthUnavailable {
		return unavailable(j, health.Reasons[0], fmt.Errorf("agentjobs: %s in %s", health.Reasons[0], FileName))
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return unavailable(j, ReasonIOFailed, err)
	}
	s := &Store{f: f, j: j}
	if validEnd != len(data) {
		if err := f.Truncate(int64(validEnd)); err != nil {
			return s.latchAndReport(err)
		}
	}
	if fresh {
		if err := s.writeHeader(dir); err != nil {
			return s.latchAndReport(err)
		}
	}
	return s, nil
}

func (s *Store) writeHeader(dir string) error {
	line, err := json.Marshal(s.j.header)
	if err != nil {
		return err
	}
	if err := s.write(line); err != nil {
		return err
	}
	return syncDir(dir)
}

// unavailable returns a store that never writes. The caller's run keeps going without a journal.
func unavailable(j *journal, reason string, err error) (*Store, error) {
	j.reasons[reason] = true
	return &Store{j: j, err: err}, err
}

func (s *Store) latchAndReport(err error) (*Store, error) {
	s.latch(err)
	return s, err
}

// latch marks the store unavailable after a failed write; call with mu held (or before sharing).
func (s *Store) latch(err error) {
	s.err = err
	s.j.reasons[ReasonIOFailed] = true
	if s.f != nil {
		_ = s.f.Close()
		s.f = nil
	}
}

// write appends one line and makes it durable.
func (s *Store) write(line []byte) error {
	if _, err := s.f.Write(append(line, '\n')); err != nil {
		return err
	}
	return s.f.Sync()
}

// Append records a fact durably before making it visible in snapshots. A repeat of a stored
// fact is a no-op; a different fact for the same job returns ErrConflictingRecord and is not
// applied. A write failure latches the store unavailable.
func (s *Store) Append(rec Record) error {
	rec = rec.normalized()
	if err := rec.validate(); err != nil {
		return err
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRecord, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if s.f == nil {
		return s.err
	}
	verdict := s.j.classify(rec, string(line))
	if verdict == verdictRepeat {
		return nil
	}
	if verdict == verdictConflict {
		s.j.reasons[ReasonConflictingRecord] = true
		return fmt.Errorf("%w: job %s", ErrConflictingRecord, rec.JobID)
	}
	if err := s.write(line); err != nil {
		s.latch(err)
		return err
	}
	s.j.add(rec, string(line))
	return nil
}

// Snapshot returns an immutable view; it stays valid and unchanged after later appends.
func (s *Store) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.j.snapshot()
}

// Err returns why the store is unavailable, or nil.
func (s *Store) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Close closes the file. It is safe to call twice; snapshots keep working.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.f == nil {
		return nil
	}
	err := s.f.Close()
	s.f = nil
	return err
}

// Load reads a journal without touching the file. A missing file is not an error: the
// snapshot reports ReasonNoJournal (a legacy run). Everything else wrong with the file
// is reported through Health.
func Load(dir string) (Snapshot, error) {
	data, err := os.ReadFile(filepath.Join(dir, FileName))
	if errors.Is(err, fs.ErrNotExist) {
		j := newJournal()
		j.reasons[ReasonNoJournal] = true
		return j.snapshot(), nil
	}
	if err != nil {
		return Snapshot{}, err
	}
	j, _ := replay(data)
	return j.snapshot(), nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return err
	}
	return d.Close()
}
