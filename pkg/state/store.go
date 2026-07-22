package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/akopichin/afm/pkg/progress"
)

type Transition struct {
	Seq     uint64      `json:"seq"`
	Time    time.Time   `json:"time"`
	StageID string      `json:"stage_id"`
	From    StageStatus `json:"from"`
	To      StageStatus `json:"to"`
	Event   string      `json:"event"`
	Reason  string      `json:"reason,omitempty"`
}

// ErrRunLocked означает, что run-директория уже открыта другим процессом afm.
// flock освобождается ОС при завершении процесса, поэтому упавший ранее run
// не оставляет «залипшей» блокировки.
var ErrRunLocked = errors.New("run directory is locked by another afm process")

// ErrCorruptLog означает битую строку в середине events.jsonl (есть валидные
// записи после неё). Оригинал НЕ усекается — копируется в .corrupt-<ts> для разбора.
var ErrCorruptLog = errors.New("events.jsonl is corrupted mid-log")

// ErrConcurrentChange — статус стадии изменился между чтением и Apply (CAS-mismatch).
// Доброкачественно: ожидаемо при конкурентных переходах, НЕ storage-ошибка.
var ErrConcurrentChange = errors.New("concurrent change")

type Store struct {
	runDir    string
	eventsLog *os.File
	snapshot  *RunState
	lastSeq   uint64
	history   []Transition
	lock      *progress.Lock
	mu        sync.Mutex
}

func Open(runDir string, stageIDs []string) (*Store, error) {
	if err := os.MkdirAll(runDir, 0755); err != nil {
		return nil, fmt.Errorf("mkdir runDir: %w", err)
	}

	lock, _ := progress.NewLock(filepath.Join(runDir, ".lock"))
	if err := lock.TryLock(); err != nil {
		return nil, ErrRunLocked
	}
	locked := true
	defer func() {
		if locked {
			lock.Unlock()
		}
	}()

	rs := NewRunState(stageIDs)

	eventsPath := filepath.Join(runDir, "events.jsonl")

	// legacy fallback: state.json exists, events.jsonl does not
	if _, err := os.Stat(eventsPath); os.IsNotExist(err) {
		if legacy, lerr := loadLegacyState(filepath.Join(runDir, "state.json")); lerr == nil {
			for id, st := range legacy.Stages {
				if _, known := rs.Stages[id]; !known {
					continue
				}
				rs.SetStageStatus(id, st.Status)
			}
		}
	}

	history, lastSeq, lastGoodOffset, corrupted, err := replayEvents(eventsPath, rs)
	if err != nil {
		return nil, fmt.Errorf("replay: %w", err)
	}
	if corrupted {
		quarantine := fmt.Sprintf("%s.corrupt-%d", eventsPath, time.Now().UnixNano())
		if data, rerr := os.ReadFile(eventsPath); rerr == nil {
			_ = os.WriteFile(quarantine, data, 0644)
		}
		return nil, fmt.Errorf("%w: quarantined to %s", ErrCorruptLog, quarantine)
	}

	f, err := os.OpenFile(eventsPath, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("open events.jsonl: %w", err)
	}
	if err := f.Truncate(lastGoodOffset); err != nil {
		return nil, fmt.Errorf("truncate events.jsonl: %w", err)
	}
	if _, err := f.Seek(lastGoodOffset, 0); err != nil {
		return nil, fmt.Errorf("seek events.jsonl: %w", err)
	}

	s := &Store{
		runDir:    runDir,
		eventsLog: f,
		snapshot:  rs,
		lastSeq:   lastSeq,
		history:   history,
		lock:      lock,
	}

	// if started from legacy fallback — write synthetic events
	if lastSeq == 0 {
		for _, id := range stageIDs {
			st := rs.Stages[id].Status
			if st == StatusPending {
				continue
			}
			s.lastSeq++
			tr := Transition{
				Seq:     s.lastSeq,
				Time:    time.Now(),
				StageID: id,
				From:    StatusPending,
				To:      st,
				Event:   "legacy_load",
			}
			data, _ := json.Marshal(tr)
			data = append(data, '\n')
			if _, werr := f.Write(data); werr != nil {
				return nil, fmt.Errorf("write legacy event: %w", werr)
			}
			s.history = append(s.history, tr)
		}
		if s.lastSeq > 0 {
			if err := f.Sync(); err != nil {
				return nil, fmt.Errorf("fsync after legacy events: %w", err)
			}
		}
	}

	locked = false
	return s, nil
}

func loadLegacyState(path string) (*RunState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rs RunState
	if err := json.Unmarshal(data, &rs); err != nil {
		return nil, err
	}
	return &rs, nil
}

func replayEvents(path string, rs *RunState) (history []Transition, lastSeq uint64, lastGoodOffset int64, corrupted bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, 0, false, nil
		}
		return nil, 0, 0, false, err
	}
	res := parseEventLog(data, rs)
	return res.history, res.lastSeq, res.goodOffset, res.corrupted, nil
}

func (s *Store) Get(stageID string) StageStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.snapshot.Stages[stageID]; ok {
		return st.Status
	}
	return StatusPending
}

func (s *Store) Snapshot() RunState {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := RunState{
		FlowName:   s.snapshot.FlowName,
		StartedAt:  s.snapshot.StartedAt,
		StageOrder: append([]string(nil), s.snapshot.StageOrder...),
		Stages:     make(map[string]StageState, len(s.snapshot.Stages)),
	}
	if s.snapshot.StageNames != nil {
		out.StageNames = maps.Clone(s.snapshot.StageNames)
	}
	for k, v := range s.snapshot.Stages {
		out.Stages[k] = v
	}
	return out
}

// History returns the full transition history accumulated during Open's replay
// of events.jsonl and extended by every subsequent Apply. Read-only — it reads
// the already-replayed in-memory log, it never re-opens events.jsonl. The slice
// is ordered by ascending Seq (the append order of the event log, which also
// guarantees non-decreasing Time). A defensive copy is returned so callers
// cannot mutate the store's internal log. A run with zero transitions returns
// an empty slice, never an error — the error is reserved for a future failure
// mode (currently always nil).
func (s *Store) History() ([]Transition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Transition, len(s.history))
	copy(out, s.history)
	return out, nil
}

// SetStageNames stores the display name for each stage. Names are flow metadata
// (sourced from the flow file), not runtime state — they are not part of the
// event log. Empty values are stored as-is; the UI handles them gracefully.
// The map is copied so later mutations by the caller cannot corrupt the store.
func (s *Store) SetStageNames(names map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshot.StageNames = maps.Clone(names)
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	if s.eventsLog != nil {
		err = s.eventsLog.Close()
		s.eventsLog = nil
	}
	if s.lock != nil {
		s.lock.Unlock()
		s.lock = nil
	}
	return err
}

// applyHook is for tests only. Called after fsync but before snapshot rewrite.
var applyHookMu sync.Mutex
var applyHook func(Transition)

// SetApplyHook installs a test hook called inside Apply between fsync and snapshot rewrite.
// Production code MUST NOT call this.
func SetApplyHook(h func(Transition)) {
	applyHookMu.Lock()
	applyHook = h
	applyHookMu.Unlock()
}

func (s *Store) Apply(t Transition) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	current := s.snapshot.Stages[t.StageID].Status
	if current != t.From {
		return fmt.Errorf("%w: stage %q is in %q, expected %q",
			ErrConcurrentChange, t.StageID, current, t.From)
	}

	s.lastSeq++
	t.Seq = s.lastSeq
	t.Time = time.Now()

	data, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("marshal transition: %w", err)
	}
	data = append(data, '\n')

	if _, err := s.eventsLog.Write(data); err != nil {
		return fmt.Errorf("write events.jsonl: %w", err)
	}
	if err := s.eventsLog.Sync(); err != nil {
		return fmt.Errorf("fsync events.jsonl: %w", err)
	}

	s.history = append(s.history, t)

	applyHookMu.Lock()
	hook := applyHook
	applyHookMu.Unlock()
	if hook != nil {
		hook(t)
	}

	s.snapshot.SetStageStatus(t.StageID, t.To)
	s.snapshot.LastSeq = s.lastSeq

	if err := s.writeSnapshot(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: snapshot write failed: %v\n", err)
	}
	return nil
}

func (s *Store) writeSnapshot() error {
	data, err := json.MarshalIndent(s.snapshot, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(s.runDir, "state.json")
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	// fsync директории — иначе rename может быть недолговечен при потере питания.
	dir, err := os.Open(s.runDir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
