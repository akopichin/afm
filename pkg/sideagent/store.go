package sideagent

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/akopichin/afm/pkg/progress"
)

const (
	chatFileName  = "chat.jsonl"
	usageFileName = "usage.jsonl"
	lockFileName  = ".lock"
)

// Ошибки стора.
var (
	// ErrLocked — разговор уже открыт другим писателем (единственный писатель).
	ErrLocked = errors.New("sideagent: conversation locked by another writer")
	// ErrStorageUnavailable — журнал повреждён или запись в него невозможна;
	// новая работа блокируется, но flow-стадии не затрагиваются.
	ErrStorageUnavailable = errors.New("sideagent: storage unavailable")
	// ErrInvalidTransition — попытка недопустимого перехода состояния хода.
	ErrInvalidTransition = errors.New("sideagent: invalid turn transition")
)

// Store — единственный живой писатель журнала одного разговора. Раскладка (от
// переданной базы, путь .afm НЕ хардкодится):
//
//	<base>/side-agent/<run_id>/
//	  chat.jsonl                 # авторитетный журнал
//	  usage.jsonl                # учёт side-запросов
//	  turns/<turn_id>/...        # логи агента по ходу
//
// Журнал отдельный от pkg/state — те же принципы durability (fsync до возврата,
// безопасный разбор, усечение оборванного хвоста, карантин битой середины), но
// независимая реализация. seq принадлежит стору.
type Store struct {
	mu             sync.Mutex
	dir            string
	conversationID string
	chatLog        *os.File
	lock           *progress.Lock

	history    []Event
	lastSeq    uint64
	turnStates map[string]TurnState
	turnOrder  []string

	unavailable bool
	lastErr     error
}

// Open открывает (создавая при необходимости) разговор для run_id под base.
// Возвращает стор даже при повреждённом журнале (вместе с ErrStorageUnavailable)
// — side-agent не должен ронять flow. Второй открыватель того же разговора
// получает ErrLocked.
func Open(base, runID string) (*Store, error) {
	dir := filepath.Join(base, "side-agent", runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("sideagent: mkdir: %w", err)
	}

	lock, err := acquireLock(dir)
	if err != nil {
		return nil, err
	}
	locked := true
	defer func() {
		if locked {
			lock.Unlock()
		}
	}()

	path := filepath.Join(dir, chatFileName)
	data, rerr := os.ReadFile(path)
	if rerr != nil && !os.IsNotExist(rerr) {
		return nil, fmt.Errorf("sideagent: read chat.jsonl: %w", rerr)
	}
	events, lastSeq, goodOffset, corrupt := parseChatLog(data)

	s := &Store{
		dir:            dir,
		conversationID: runID,
		lock:           lock,
		history:        events,
		lastSeq:        lastSeq,
		turnStates:     make(map[string]TurnState),
	}
	for _, e := range events {
		s.applyTurnState(e)
	}

	if corrupt {
		// Оригинал НЕ трогаем: открываем дескриптор на дозапись, но стор сразу
		// unavailable — любая новая работа блокируется, а не проглатывается.
		f, oerr := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if oerr != nil {
			return nil, fmt.Errorf("sideagent: open chat.jsonl: %w", oerr)
		}
		s.chatLog = f
		s.unavailable = true
		s.lastErr = fmt.Errorf("%w: chat.jsonl corrupt mid-journal", ErrStorageUnavailable)
		locked = false
		return s, s.lastErr
	}

	f, oerr := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o644)
	if oerr != nil {
		return nil, fmt.Errorf("sideagent: open chat.jsonl: %w", oerr)
	}
	// Усекаем оборванный незакоммиченный хвост до последней целой записи.
	if err := f.Truncate(goodOffset); err != nil {
		f.Close()
		return nil, fmt.Errorf("sideagent: truncate chat.jsonl: %w", err)
	}
	if _, err := f.Seek(goodOffset, 0); err != nil {
		f.Close()
		return nil, fmt.Errorf("sideagent: seek chat.jsonl: %w", err)
	}
	s.chatLog = f

	// Recovery: ход, принятый/стартовавший, но без терминала (крах) помечается
	// interrupted — durable, без авто-реплея агента.
	if err := s.recoverOrphans(); err != nil {
		f.Close()
		return nil, err
	}

	locked = false
	return s, nil
}

// acquireLock захватывает эксклюзивный flock каталога разговора. Contention →
// ErrLocked; прочие ошибки (нет каталога, нехватка дескрипторов) пробрасываются.
func acquireLock(dir string) (*progress.Lock, error) {
	l, err := progress.NewLock(filepath.Join(dir, lockFileName))
	if err != nil {
		return nil, err
	}
	if err := l.TryLock(); err != nil {
		if errors.Is(err, progress.ErrLockBusy) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return l, nil
}

// parseChatLog — единственный разборщик chat.jsonl. Оборванный хвост (последняя
// строка без '\n') усекается и НЕ считается порчей. Битая ПОЛНАЯ строка в
// середине → corrupt=true (goodOffset указывает на конец последней целой записи).
func parseChatLog(data []byte) (events []Event, lastSeq uint64, goodOffset int64, corrupt bool) {
	lines := splitLines(data)
	endsWithNewline := len(data) > 0 && data[len(data)-1] == '\n'
	var offset int64
	for i, line := range lines {
		isLast := i == len(lines)-1
		if isLast && !endsWithNewline {
			break // незакоммиченный хвост
		}
		offset += int64(len(line)) + 1 // +1 на '\n'
		if len(bytes.TrimSpace(line)) == 0 {
			goodOffset = offset
			continue
		}
		e, err := DecodeEvent(line)
		if err != nil {
			return events, lastSeq, goodOffset, true
		}
		events = append(events, e)
		lastSeq = e.Seq
		goodOffset = offset
	}
	return events, lastSeq, goodOffset, false
}

func splitLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			out = append(out, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		out = append(out, data[start:])
	}
	return out
}

// recoverOrphans помечает незавершённые ходы как interrupted в порядке их
// появления. Вызывается только из Open (без конкуренции) с удерживаемым flock.
func (s *Store) recoverOrphans() error {
	for _, turnID := range s.turnOrder {
		if s.turnStates[turnID].IsTerminal() {
			continue
		}
		if _, err := s.appendLocked(EventTurnInterrupted, turnID, TurnInterruptedData{Reason: "recovered: no terminal event"}); err != nil {
			return err
		}
	}
	return nil
}

// applyTurnState обновляет in-memory FSM-карту ходов по событию.
func (s *Store) applyTurnState(e Event) {
	st, ok := e.Type.TurnState()
	if !ok {
		return // content-событие состояние хода не меняет
	}
	if _, seen := s.turnStates[e.TurnID]; !seen {
		s.turnOrder = append(s.turnOrder, e.TurnID)
	}
	s.turnStates[e.TurnID] = st
}

// Append добавляет событие в журнал: валидация перехода → durable-запись с fsync
// → и только потом мутация in-memory состояния (durable-first). Один активный
// ход за раз обеспечивается валидацией переходов.
func (s *Store) Append(typ EventType, turnID string, data any) (Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendLocked(typ, turnID, data)
}

func (s *Store) appendLocked(typ EventType, turnID string, data any) (Event, error) {
	if s.unavailable {
		return Event{}, s.lastErr
	}
	if err := s.validateTransition(typ, turnID); err != nil {
		return Event{}, err
	}

	e := Event{
		SchemaVersion:  SchemaVersion,
		Seq:            s.lastSeq + 1,
		ConversationID: s.conversationID,
		TurnID:         turnID,
		Type:           typ,
		Timestamp:      time.Now().UTC(),
	}
	if data != nil {
		if err := e.SetData(data); err != nil {
			return Event{}, err
		}
	}
	raw, err := e.Encode()
	if err != nil {
		return Event{}, fmt.Errorf("sideagent: encode event: %w", err)
	}
	raw = append(raw, '\n')

	if _, err := s.chatLog.Write(raw); err != nil {
		s.markUnavailable(err)
		return Event{}, s.lastErr
	}
	if err := s.chatLog.Sync(); err != nil {
		s.markUnavailable(err)
		return Event{}, s.lastErr
	}

	// durable-first: журнал на диске, теперь обновляем runtime-состояние.
	s.lastSeq = e.Seq
	s.history = append(s.history, e)
	s.applyTurnState(e)
	return e, nil
}

// validateTransition проверяет допустимость события для текущего состояния хода.
func (s *Store) validateTransition(typ EventType, turnID string) error {
	newState, isFSM := typ.TurnState()
	cur, exists := s.turnStates[turnID]

	if !isFSM {
		// content-событие допустимо только для активного (started) хода
		if !exists || cur != TurnStateStarted {
			return fmt.Errorf("%w: %s on turn %q in state %q", ErrInvalidTransition, typ, turnID, cur)
		}
		return nil
	}
	if newState == TurnStateAccepted {
		if exists {
			return fmt.Errorf("%w: turn %q already exists", ErrInvalidTransition, turnID)
		}
		return nil
	}
	if !exists {
		return fmt.Errorf("%w: turn %q not found for %s", ErrInvalidTransition, turnID, typ)
	}
	if !cur.CanTransitionTo(newState) {
		return fmt.Errorf("%w: %s->%s on turn %q", ErrInvalidTransition, cur, newState, turnID)
	}
	return nil
}

// markUnavailable должен вызываться с удерживаемым mu.
func (s *Store) markUnavailable(err error) {
	s.unavailable = true
	s.lastErr = fmt.Errorf("%w: %v", ErrStorageUnavailable, err)
}

// History возвращает защитную копию журнала в порядке возрастания seq.
func (s *Store) History() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Event, len(s.history))
	copy(out, s.history)
	return out
}

// ConversationID возвращает id разговора (== run_id).
func (s *Store) ConversationID() string { return s.conversationID }

// Unavailable сообщает, заблокирована ли дальнейшая работа (порча/сбой записи).
func (s *Store) Unavailable() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.unavailable
}

// Dir возвращает каталог разговора (namespace для usage.jsonl и turns/).
func (s *Store) Dir() string { return s.dir }

// UsagePath возвращает путь к usage.jsonl в том же namespace.
func (s *Store) UsagePath() string { return filepath.Join(s.dir, usageFileName) }

// TurnDir возвращает (создавая) каталог логов агента для хода.
func (s *Store) TurnDir(turnID string) (string, error) {
	p := filepath.Join(s.dir, "turns", turnID)
	if err := os.MkdirAll(p, 0o755); err != nil {
		return "", fmt.Errorf("sideagent: mkdir turn dir: %w", err)
	}
	return p, nil
}

// Close закрывает журнал и снимает flock. Безопасно повторно.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	if s.chatLog != nil {
		err = s.chatLog.Close()
		s.chatLog = nil
	}
	if s.lock != nil {
		s.lock.Unlock()
		s.lock = nil
	}
	return err
}
