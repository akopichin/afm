package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
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

// ErrRunLocked означает, что run-директория уже открыта другим процессом afm. flock освобождается ОС при
// завершении процесса, поэтому упавший ранее run не оставляет «залипшей» блокировки.
var ErrRunLocked = errors.New("run directory is locked by another afm process")

// ErrCorruptLog означает битую строку в середине events.jsonl (есть валидные
// записи после неё). Оригинал НЕ усекается — копируется в .corrupt-<ts> для разбора.
var ErrCorruptLog = errors.New("events.jsonl is corrupted mid-log")

// ErrConcurrentChange — статус стадии изменился между чтением и Apply (CAS-mismatch).
// Доброкачественно: ожидаемо при конкурентных переходах, НЕ storage-ошибка.
var ErrConcurrentChange = errors.New("concurrent change")

// ErrStoreClosed — попытка записи (Apply/ApplyRunOperation/AddStageToGraph) после Store.Close() уже обнулил
// eventsLog (Этап 10, T10.5: "действие бриджа не пишет в закрытый store"). Доброкачественно, как
// ErrConcurrentChange: ОДИН и тот же "store is closed" sentinel для ВСЕХ трёх путей записи — до этого каждый
// возвращал свой голый errors.New("run store is closed"), неразличимый от реальной storage-ошибки для
// вызывающего (errors.Is не сработал бы). FSM.Apply (pkg/orchestrator/bus/fsm.go) классифицирует его так же
// доброкачественно, как ErrConcurrentChange, а НЕ как fatal StorageError — стадия, которую scheduler пытается
// запустить ровно в момент штатного shutdown рана, не должна выглядеть как повреждение журнала.
var ErrStoreClosed = errors.New("state: run store is closed")

type Store struct {
	runDir    string
	eventsLog *os.File
	snapshot  *RunState
	lastSeq   uint64
	history   []Transition
	// controlHistory accumulates every control record committed during this run (replay + live), in
	// increasing Seq order — mirrors history above but for Control records rather than Transitions. Backs
	// TaskEvents' durable per-task event page (pkg/orchestrator's read side for GET
	// /api/assistants/{id}/events); unlike rs.Assistants (the CURRENT projected task-tree state), this is the
	// append-only record of every effect ever committed.
	controlHistory []Control
	lock           *progress.Lock
	mu             sync.Mutex

	// writeOffset is the byte offset in events.jsonl where the NEXT write will land — always the current
	// end of file, since every write is a sequential append. Seeded from replay's lastGoodOffset in Open.
	writeOffset int64
	// seqOffsets maps a control record's seq -> the byte offset where its line STARTS. Populated by both
	// replay (Open) and live writes (appendControlLocked), so T2.5's "direct single read of the journal at
	// the known seq, no scan" can seek straight to it instead of scanning the file. Transitions are not
	// indexed here — only control records need this (idempotent-operation replay).
	seqOffsets map[uint64]int64
	// runFinalizing gates ApplyRunOperation's admission check (T2.4 step 1). Sti set by the orchestrator
	// layer when the run begins shutting down; pkg/state itself has no opinion on WHEN that happens.
	runFinalizing bool
	// opCache is the bounded, evicting fast-path cache of recent operation results (T2.5). The unbounded,
	// never-evicted half of the idempotency boundary is rs.Assistants.OperationIndex (restored by replay).
	opCache *opResultCache
}

func Open(runDir string, stageIDs []string) (*Store, error) {
	if err := os.MkdirAll(runDir, 0755); err != nil {
		return nil, fmt.Errorf("mkdir runDir: %w", err)
	}

	lock, err := acquireRunLock(runDir)
	if err != nil {
		return nil, err
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

	history, controls, lastSeq, lastGoodOffset, corrupted, unsupported, seqOffsets, err := replayEvents(eventsPath, rs)
	if err != nil {
		return nil, fmt.Errorf("replay: %w", err)
	}
	if unsupported != nil {
		// Fail-closed (T2.1): a syntactically valid but unrecognized control record must not be silently
		// reinterpreted, and the run must not continue with a partial task graph. Unlike corrupted below,
		// the original events.jsonl is left untouched — this is not data corruption to quarantine, it is a
		// forward-compatibility hazard (an older afm build opening a log written by a newer one).
		return nil, unsupported
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

	if seqOffsets == nil {
		seqOffsets = map[uint64]int64{}
	}
	s := &Store{
		runDir:         runDir,
		eventsLog:      f,
		snapshot:       rs,
		lastSeq:        lastSeq,
		history:        history,
		controlHistory: controls,
		lock:           lock,
		writeOffset:    lastGoodOffset,
		seqOffsets:     seqOffsets,
		opCache:        newOpResultCache(opResultCacheCapacity()),
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
				Time:    clk().Now(),
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
			s.writeOffset += int64(len(data))
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

func replayEvents(path string, rs *RunState) (history []Transition, controls []Control, lastSeq uint64, lastGoodOffset int64, corrupted bool, unsupported error, seqOffsets map[uint64]int64, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, 0, 0, false, nil, nil, nil
		}
		return nil, nil, 0, 0, false, nil, nil, err
	}
	res := parseEventLog(data, rs)
	return res.history, res.controls, res.lastSeq, res.goodOffset, res.corrupted, res.unsupported, res.seqOffsets, nil
}

func (s *Store) Get(stageID string) StageStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.snapshot.Stages[stageID]; ok {
		return st.Status
	}
	return StatusPending
}

// PausedFrom returns the status a paused stage was paused from (empty if the stage has never been paused).
// See StageState.PausedFrom for why this value survives after the stage leaves paused.
func (s *Store) PausedFrom(stageID string) StageStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot.Stages[stageID].PausedFrom
}

func (s *Store) Snapshot() RunState {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := RunState{
		FlowName:             s.snapshot.FlowName,
		StartedAt:            s.snapshot.StartedAt,
		RunStatus:            s.snapshot.RunStatus,
		RunStartedAt:         s.snapshot.RunStartedAt,
		ElapsedAccumulatedMs: s.snapshot.ElapsedAccumulatedMs,
		LastSeq:              s.snapshot.LastSeq,
		StageOrder:           append([]string(nil), s.snapshot.StageOrder...),
		Stages:               make(map[string]StageState, len(s.snapshot.Stages)),
		IdleAccumulatedMs:    s.snapshot.IdleAccumulatedMs,
		BackoffAccumulatedMs: s.snapshot.BackoffAccumulatedMs,
		Assistants:           s.snapshot.Assistants.Clone(),
		DynamicStages:        append([]DynamicStageAdded(nil), s.snapshot.DynamicStages...),
	}
	if s.snapshot.EndedAt != nil {
		ended := *s.snapshot.EndedAt
		out.EndedAt = &ended
	}
	if s.snapshot.StageNames != nil {
		out.StageNames = maps.Clone(s.snapshot.StageNames)
	}
	for k, v := range s.snapshot.Stages {
		out.Stages[k] = v
	}
	return out
}

// History returns stage transitions accumulated during Open's replay and extended by Apply; run boundary
// records are excluded.
func (s *Store) History() ([]Transition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Transition, len(s.history))
	copy(out, s.history)
	return out, nil
}

// stageGenerationEvent is the FSM event whose Transition.Seq this build treats as "a fresh attempt of the
// stage's main agent began" — a plain string literal (not bus.FSMEvent) to avoid an import cycle (pkg/state
// cannot import pkg/orchestrator/bus), but it is the exact wire value of bus.EvStartRun.
const stageGenerationEvent = "start_run"

// StageGeneration returns stageID's current generation (T6.4/T6.5): the Seq of the LATEST transition that
// started a fresh run of the stage's main agent (bus.EvStartRun — ready/revising -> running), or 0 if the
// stage has never actually started running yet. A Proposal/FeedbackIntent captures this value when it is
// computed; a later mismatch means the stage has since moved on to a different attempt (accepted a different
// Revise, was retried, ...) and the proposal/intent is stale — TestProposal_StaleWhenGenerationChanged,
// TestDeferredIntent_RevalidatedBeforeExecution.
func (s *Store) StageGeneration(stageID string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stageGenerationLocked(stageID)
}

// stageGenerationLocked is StageGeneration's body, callable by code that ALREADY holds s.mu (e.g.
// applyRunOperationLocked's precondition check) without re-acquiring the (non-reentrant) lock.
func (s *Store) stageGenerationLocked(stageID string) uint64 {
	var gen uint64
	for _, tr := range s.history {
		if tr.StageID == stageID && tr.Event == stageGenerationEvent && tr.Seq > gen {
			gen = tr.Seq
		}
	}
	return gen
}

// stageReviseEvent is the FSM event (bus.EvRevise's wire value) LastReviseReason scans for.
const stageReviseEvent = "revise"

// LastReviseReason returns the Reason recorded on stageID's MOST RECENT EvRevise transition, or "" if none
// ever happened. Used by pkg/orchestrator's FeedbackIntent delivery (T6.5/T6.6) to tell apart "this stage's
// generation moved on because OF THIS VERY intent's own (crash-interrupted) delivery attempt resuming" from
// "a genuinely different revise/attempt happened in the meantime" — reviseRunning/reviseAwaitingApproval stamp
// the intent id into the reason, so a resumed stage whose last revise reason still names this intent is not
// treated as stale even though Store.StageGeneration moved on.
func (s *Store) LastReviseReason(stageID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var reason string
	var lastSeq uint64
	for _, tr := range s.history {
		if tr.StageID == stageID && tr.Event == stageReviseEvent && tr.Seq >= lastSeq {
			lastSeq = tr.Seq
			reason = tr.Reason
		}
	}
	return reason
}

// TaskEvent is one durable effect ever committed against a single task — the per-task projection of the
// "operation_committed" control stream. ApplyRunOperation (T2.4) bundles every Effect of one caller-intent
// operation (Attach/PauseTask/ContinueTask/CancelTask/the scheduler's own transitions) into ONE Control
// record, so several TaskEvents can share one Seq when more than one Effect of the same operation targets the
// same task (e.g. Attach's task_created plus an immediate task_status_changed to "queued"). This is the
// durable, authoritative source behind the dashboard's per-task event-history page (GET
// /api/assistants/{id}/events) — distinct from the live, best-effort notices.jsonl (AGENTS.md's "забыть
// durable-копию" gotcha does not apply here: these ARE the durable journal records, not a UI notice).
type TaskEvent struct {
	Seq     uint64          `json:"seq"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// TaskEvents returns every TaskEvent ever committed against taskID, across both replay and live writes, in
// increasing Seq order.
func (s *Store) TaskEvents(taskID assistants.TaskID) []TaskEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []TaskEvent
	for _, c := range s.controlHistory {
		if c.Kind != kindOperationCommitted {
			continue
		}
		var p operationCommittedPayload
		if decodePayload(c.Payload, &p) != nil {
			continue
		}
		for _, e := range p.Effects {
			if e.Target.TaskID == string(taskID) {
				out = append(out, TaskEvent{Seq: c.Seq, Kind: e.Kind, Payload: e.Payload})
			}
		}
	}
	return out
}

// SetStageNames stores the display name for each stage. Empty values are stored as-is; the UI handles them
// gracefully.
func (s *Store) SetStageNames(names map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshot.StageNames = maps.Clone(names)
}

// SetFlowName stores the flow's display name.
func (s *Store) SetFlowName(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshot.FlowName = name
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

// CloseEventsLogForTest closes the underlying journal file descriptor directly, WITHOUT nil-ing s.eventsLog
// or releasing the run lock (unlike Close) — simulates a real OS-level write/fsync failure on the NEXT
// Apply/AppendControl/ApplyRunOperation call (a closed *os.File still looks non-nil to those methods, so they
// attempt the write, which fails and is wrapped as *StorageError — never the "run store is closed" plain
// error Close() itself would produce). Modeled on record_test.go/journal_regression_test.go's in-package
// `store.eventsLog.Close()` trick; exported only so pkg/orchestrator (a different package) can inject the
// same failure from its own tests. Production code MUST NOT call this.
func (s *Store) CloseEventsLogForTest() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.eventsLog != nil {
		_ = s.eventsLog.Close()
	}
}

// BeginRun and EndRun persist process-lifetime boundaries in events.jsonl.
// These records share its sequence but are excluded from stage History().
func (s *Store) BeginRun() error {
	return s.appendRunEvent(runEventStarted)
}

func (s *Store) EndRun(status RunStatus) error {
	var event string
	switch status {
	case RunStatusFinished:
		event = runEventFinished
	case RunStatusFailed:
		event = runEventFailed
	case RunStatusInterrupted:
		event = runEventInterrupted
	default:
		return fmt.Errorf("invalid terminal run status %q", status)
	}
	return s.appendRunEvent(event)
}

func (s *Store) appendRunEvent(event string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.eventsLog == nil {
		return ErrStoreClosed
	}
	if event != runEventStarted && s.snapshot.EndedAt != nil {
		return nil
	}
	t := Transition{Seq: s.lastSeq + 1, Time: clk().Now(), Event: event}
	data, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("marshal run event: %w", err)
	}
	line := append(data, '\n')
	if _, err := s.eventsLog.Write(line); err != nil {
		return fmt.Errorf("write run event: %w", err)
	}
	if err := s.eventsLog.Sync(); err != nil {
		return fmt.Errorf("fsync run event: %w", err)
	}
	s.writeOffset += int64(len(line))
	if s.lastSeq == 0 && event == runEventStarted {
		s.snapshot.StartedAt = t.Time
	}
	s.lastSeq = t.Seq
	applyRunEvent(s.snapshot, event, t.Time)
	s.snapshot.LastSeq = t.Seq
	if err := s.writeSnapshot(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: snapshot write failed: %v\n", err)
	}
	return nil
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

// Apply делает durable CAS-переход и заполняет t.Seq/t.Time до возврата; отдельное чтение LastSeq
// подвержено гонке.
func (s *Store) Apply(t *Transition) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Этап 10 (T10.5): without this check, a FSM transition racing a real Store.Close() would reach
	// s.eventsLog.Write on a nil *os.File below — nil-safe (returns fs.ErrInvalid, "invalid argument"), so it
	// would NOT panic, but FSM.Apply has no way to tell that apart from a genuine disk failure and would wrap
	// it as a fatal StorageError, ending the run with a misleading "storage failure" for what is actually a
	// benign "the run is already shutting down" race (see AGENTS.md's own write-up of this exact symptom
	// class). ApplyRunOperation/AddStageToGraph already guard this the same way; Apply never did.
	if s.eventsLog == nil {
		return ErrStoreClosed
	}

	current := s.snapshot.Stages[t.StageID].Status
	if current != t.From {
		return fmt.Errorf("%w: stage %q is in %q, expected %q",
			ErrConcurrentChange, t.StageID, current, t.From)
	}

	s.lastSeq++
	t.Seq = s.lastSeq
	// Round(0) drops the monotonic reading: durations computed live (accountIdleAndBackoff) then match the ones
	// computed from the journal on replay, which only has wall-clock time.
	t.Time = clk().Now().Round(0)

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
	s.writeOffset += int64(len(data))

	s.history = append(s.history, *t)

	applyHookMu.Lock()
	hook := applyHook
	applyHookMu.Unlock()
	if hook != nil {
		hook(*t)
	}

	// SetStageStatusAt, не SetStageStatus: t.Time уже зафиксирован выше и это именно то время, которое
	// попадёт в events.jsonl.
	s.snapshot.applyTransition(t)
	s.snapshot.LastSeq = s.lastSeq

	if err := s.writeSnapshot(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: snapshot write failed: %v\n", err)
	}
	return nil
}

// StorageError signals that AppendControl/ApplyRunOperation failed because the underlying log write (or
// fsync) itself failed — as opposed to a benign precondition rejection (stale revision, run finalizing).
// Distinct from bus.StorageError (pkg/orchestrator/bus, which wraps Store.Apply failures for the stage FSM):
// pkg/state cannot import pkg/orchestrator/bus (that package imports pkg/state), and AppendControl does not
// go through Store.Apply at all, so it needs its own typed error for the same purpose.
type StorageError struct{ Inner error }

func (e *StorageError) Error() string { return "storage failure: " + e.Inner.Error() }
func (e *StorageError) Unwrap() error { return e.Inner }

// appendControlHookMu/appendControlHook — test seam for AppendControl, modeled on applyHook/SetApplyHook
// above but kept as its own named variable rather than overloading the Transition-typed one: AppendControl
// writes Control records, not Transitions, so a shared hook would need a lossy union type for no benefit.
var appendControlHookMu sync.Mutex
var appendControlHook func(Control)

// SetAppendControlHook installs a test hook called inside AppendControl between fsync and snapshot rewrite.
// Production code MUST NOT call this.
func SetAppendControlHook(h func(Control)) {
	appendControlHookMu.Lock()
	appendControlHook = h
	appendControlHookMu.Unlock()
}

// AppendControl durably appends a new control record, assigning Seq/Time from the SAME monotonic counter
// used by Apply (stage transitions) and the same events.jsonl file/mutex — seq is strictly increasing across
// both kinds of records. AppendControl never calls Apply and never changes stage status: it is a new,
// parallel write path for assistant/task state, not a backdoor around the stage FSM (setstatuslinter only
// polices Store.Apply call sites, which remain unchanged).
//
// A write/fsync failure returns a *StorageError so callers can tell it apart from a benign rejection, the
// same way FSM.Apply distinguishes state.ErrConcurrentChange from bus.StorageError for stage transitions.
// Calling AppendControl after Close returns a plain error without touching s.eventsLog — s.eventsLog is nil
// after Close, and (*os.File)(nil).Write silently returns fs.ErrInvalid ("invalid argument"), which is
// exactly the historical flake this guards against (see AGENTS.md).
func (s *Store) AppendControl(c *Control) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendControlLocked(c)
}

// appendControlLocked is AppendControl's body, factored out so ApplyRunOperation (T2.4) can reuse the exact
// same write/fsync/hook/projection/offset-tracking sequence for its single bundled "operation_committed"
// record instead of duplicating it — one code path for "durably write one control record", two callers.
// Caller MUST hold s.mu.
func (s *Store) appendControlLocked(c *Control) error {
	if s.eventsLog == nil {
		return ErrStoreClosed
	}

	nextSeq := s.lastSeq + 1
	c.Seq = nextSeq
	c.Time = clk().Now().Round(0) // wall clock only, as on replay (see Apply)

	// Validate BEFORE ever touching the journal: a record that fails projection must never be durably
	// written, or every future replay of this run would fail the exact same way forever (unlike a torn tail
	// or a corrupted line, there is no quarantine-and-move-on for "this run's journal asserts an impossible
	// task tree"). validateControlForRunState mutates nothing — see its doc comment for why this is safe to
	// call speculatively against the live snapshot.
	if verr := validateControlForRunState(s.snapshot, c); verr != nil {
		return fmt.Errorf("reject control record: %w", verr)
	}

	data, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal control record: %w", err)
	}
	data = append(data, '\n')

	offset := s.writeOffset
	if _, werr := s.eventsLog.Write(data); werr != nil {
		return &StorageError{Inner: fmt.Errorf("write events.jsonl: %w", werr)}
	}
	if serr := s.eventsLog.Sync(); serr != nil {
		return &StorageError{Inner: fmt.Errorf("fsync events.jsonl: %w", serr)}
	}
	s.writeOffset += int64(len(data))
	s.seqOffsets[c.Seq] = offset
	s.controlHistory = append(s.controlHistory, *c)

	appendControlHookMu.Lock()
	hook := appendControlHook
	appendControlHookMu.Unlock()
	if hook != nil {
		hook(*c)
	}

	if perr := applyControlToRunState(s.snapshot, c); perr != nil {
		// Unreachable in practice: validateControlForRunState just proved this exact record is admissible
		// against this exact snapshot, under the same lock, with nothing else able to run concurrently. Fail
		// loudly rather than let the live snapshot silently diverge from what replay would compute — the
		// record IS already durably written at this point, so this is a genuine storage-fatal situation
		// (the live cache and the authoritative log have diverged), not a benign rejection.
		return &StorageError{Inner: fmt.Errorf("apply control to projection after a successful write (should be unreachable): %w", perr)}
	}
	s.lastSeq = nextSeq
	s.snapshot.LastSeq = s.lastSeq

	// Snapshot write failure is best-effort, same as Apply: events.jsonl is already durable, state.json is
	// only a derived cache.
	if err := s.writeSnapshot(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: snapshot write failed: %v\n", err)
	}
	return nil
}

// SetRunFinalizing marks whether the run is winding down — ApplyRunOperation rejects new operations with
// CodeRunFinalizing while true (T2.4 step 1). pkg/state has no opinion on WHEN a run starts finalizing; the
// orchestrator layer calls this as part of its own shutdown sequence.
func (s *Store) SetRunFinalizing(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runFinalizing = v
}

// FinalizeIf marks the run finalizing when canFinish holds for the current projection, deciding both under
// the SAME lock that admits every operation: an operation committed before the check is part of what
// canFinish sees, and one arriving after is rejected run_finalizing. canFinish must only read rs.
func (s *Store) FinalizeIf(canFinish func(rs *RunState) bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !canFinish(s.snapshot) {
		return false
	}
	s.runFinalizing = true
	return true
}

// View runs read on the live projection under the store lock, without the full copy Snapshot makes — for a
// frequent read of a small part of a large state (a dashboard poll). read must only read rs, must copy out
// whatever it returns (nothing of rs may outlive the call), and must not call back into the Store.
func (s *Store) View(read func(rs *RunState)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	read(s.snapshot)
}

// readControlAtSeq reads exactly the ONE control record at seq directly via its recorded byte offset
// (T2.5) — no scan of the rest of the journal. Caller MUST hold s.mu (same lock as the operation deciding
// whether to admit a repeat).
func (s *Store) readControlAtSeq(seq uint64) (Control, error) {
	controlReadHookMu.Lock()
	hook := controlReadHook
	controlReadHookMu.Unlock()
	if hook != nil {
		hook()
	}

	offset, ok := s.seqOffsets[seq]
	if !ok {
		return Control{}, fmt.Errorf("no recorded journal offset for seq %d", seq)
	}
	const maxLineBytes = 1 << 20 // 1 MiB — generous for a human-scale operation's bundled effects
	buf := make([]byte, maxLineBytes)
	n, err := s.eventsLog.ReadAt(buf, offset)
	if err != nil && err != io.EOF {
		return Control{}, fmt.Errorf("read journal at offset %d: %w", offset, err)
	}
	idx := bytes.IndexByte(buf[:n], '\n')
	if idx < 0 {
		return Control{}, fmt.Errorf("control record at seq %d exceeds %d bytes or is missing its terminating newline", seq, maxLineBytes)
	}
	var c Control
	if err := json.Unmarshal(buf[:idx], &c); err != nil {
		return Control{}, fmt.Errorf("decode control record at seq %d: %w", seq, err)
	}
	return c, nil
}

// controlReadHookMu/controlReadHook — test seam counting/observing calls to readControlAtSeq, so T2.5
// can assert a brand-new operation_id's hot path never touches the journal (TestIdempotency_
// NewOperationDoesNotReadJournal) while a cache-miss-but-index-hit repeat reads it exactly once.
var controlReadHookMu sync.Mutex
var controlReadHook func()

// SetControlReadHook installs a test hook called every time readControlAtSeq runs. Production code
// MUST NOT call this.
func SetControlReadHook(h func()) {
	controlReadHookMu.Lock()
	controlReadHook = h
	controlReadHookMu.Unlock()
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
