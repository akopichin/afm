package sideagent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// пустой/отсутствующий разговор → пустая история, стор доступен (агент не стартует —
// это уже забота вызывающего, но стор не должен быть unavailable).
func TestOpen_EmptyConversation(t *testing.T) {
	base := t.TempDir()
	s, err := Open(base, "run-1")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if len(s.History()) != 0 {
		t.Fatalf("want empty history, got %d", len(s.History()))
	}
	if s.Unavailable() {
		t.Fatal("fresh store must not be unavailable")
	}
	if s.ConversationID() != "run-1" {
		t.Fatalf("conversation id: want run-1, got %q", s.ConversationID())
	}
}

// append монотонно увеличивает seq и replay воспроизводит ту же последовательность.
func TestAppend_MonotonicSeqAndReplay(t *testing.T) {
	base := t.TempDir()
	s, err := Open(base, "run-1")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	e1, err := s.Append(EventTurnAccepted, "t1", TurnAcceptedData{Message: "hi", ClientMessageID: "c1"})
	if err != nil {
		t.Fatalf("append accepted: %v", err)
	}
	e2, err := s.Append(EventTurnStarted, "t1", TurnStartedData{Command: "claude"})
	if err != nil {
		t.Fatalf("append started: %v", err)
	}
	e3, err := s.Append(EventTurnCompleted, "t1", TurnCompletedData{})
	if err != nil {
		t.Fatalf("append completed: %v", err)
	}
	if e1.Seq != 1 || e2.Seq != 2 || e3.Seq != 3 {
		t.Fatalf("seq not monotonic: %d %d %d", e1.Seq, e2.Seq, e3.Seq)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// replay
	s2, err := Open(base, "run-1")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	h := s2.History()
	if len(h) != 3 {
		t.Fatalf("replay history len: want 3, got %d", len(h))
	}
	if h[0].Type != EventTurnAccepted || h[1].Type != EventTurnStarted || h[2].Type != EventTurnCompleted {
		t.Fatalf("replay order wrong: %v %v %v", h[0].Type, h[1].Type, h[2].Type)
	}
	if h[0].Seq != 1 || h[2].Seq != 3 {
		t.Fatalf("replay seq wrong: %d..%d", h[0].Seq, h[2].Seq)
	}
}

// два подряд сообщения делят общую историю после replay.
func TestAppend_TwoMessagesShareHistory(t *testing.T) {
	base := t.TempDir()
	s, _ := Open(base, "run-1")
	// первый ход
	_, _ = s.Append(EventTurnAccepted, "t1", TurnAcceptedData{Message: "first"})
	_, _ = s.Append(EventTurnStarted, "t1", TurnStartedData{Command: "claude"})
	_, _ = s.Append(EventTurnCompleted, "t1", TurnCompletedData{})
	// второй ход
	_, _ = s.Append(EventTurnAccepted, "t2", TurnAcceptedData{Message: "second"})
	_, _ = s.Append(EventTurnStarted, "t2", TurnStartedData{Command: "claude"})
	_, _ = s.Append(EventTurnCompleted, "t2", TurnCompletedData{})
	s.Close()

	s2, _ := Open(base, "run-1")
	defer s2.Close()
	h := s2.History()
	if len(h) != 6 {
		t.Fatalf("want 6 events, got %d", len(h))
	}
	var d1, d2 TurnAcceptedData
	_ = h[0].DecodeData(&d1)
	_ = h[3].DecodeData(&d2)
	if d1.Message != "first" || d2.Message != "second" {
		t.Fatalf("messages not shared after replay: %q %q", d1.Message, d2.Message)
	}
}

// resume того же run_id восстанавливает разговор, другой run_id → пусто.
func TestOpen_ResumeByRunID(t *testing.T) {
	base := t.TempDir()
	s, _ := Open(base, "run-1")
	_, _ = s.Append(EventTurnAccepted, "t1", TurnAcceptedData{Message: "hi"})
	_, _ = s.Append(EventTurnStarted, "t1", TurnStartedData{Command: "claude"})
	_, _ = s.Append(EventTurnCompleted, "t1", TurnCompletedData{})
	s.Close()

	same, _ := Open(base, "run-1")
	if len(same.History()) != 3 {
		t.Fatalf("resume run-1: want 3, got %d", len(same.History()))
	}
	same.Close()

	other, _ := Open(base, "run-2")
	defer other.Close()
	if len(other.History()) != 0 {
		t.Fatalf("different run_id must be empty, got %d", len(other.History()))
	}
}

// crash после turn_accepted: при повторном открытии ход без терминального события
// помечается interrupted (durable), без авто-реплея агента.
func TestOpen_RecoversOrphanTurnAsInterrupted(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "side-agent", "run-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// один accepted без терминала, строка завершена \n (закоммичена)
	acc := Event{SchemaVersion: SchemaVersion, Seq: 1, ConversationID: "run-1", TurnID: "t1", Type: EventTurnAccepted}
	_ = acc.SetData(TurnAcceptedData{Message: "hi"})
	raw, _ := acc.Encode()
	if err := os.WriteFile(filepath.Join(dir, "chat.jsonl"), append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := Open(base, "run-1")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	h := s.History()
	if len(h) != 2 {
		t.Fatalf("want accepted + interrupted = 2, got %d", len(h))
	}
	last := h[len(h)-1]
	if last.Type != EventTurnInterrupted || last.TurnID != "t1" {
		t.Fatalf("orphan turn not marked interrupted: %+v", last)
	}
	// durable: при повторном открытии interrupt уже на диске, нового не добавляется
	s.Close()
	s2, _ := Open(base, "run-1")
	defer s2.Close()
	if n := len(s2.History()); n != 2 {
		t.Fatalf("recovery must be durable & idempotent: want 2, got %d", n)
	}
}

// оборванный хвост (последняя строка без \n) безопасно усекается, разговор читаем.
func TestOpen_TornTailTruncated(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "side-agent", "run-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	good := Event{SchemaVersion: SchemaVersion, Seq: 1, ConversationID: "run-1", TurnID: "t1", Type: EventTurnAccepted}
	_ = good.SetData(TurnAcceptedData{Message: "hi"})
	gr, _ := good.Encode()
	committed := append(gr, '\n')
	// terminal для того же хода, чтобы не триггерить recovery
	term := Event{SchemaVersion: SchemaVersion, Seq: 2, ConversationID: "run-1", TurnID: "t1", Type: EventTurnInterrupted}
	tr, _ := term.Encode()
	committed = append(committed, tr...)
	committed = append(committed, '\n')
	// оборванный хвост без \n
	torn := []byte(`{"schema_version":1,"seq":3,"conversation_id":"run-1`)
	p := filepath.Join(dir, "chat.jsonl")
	if err := os.WriteFile(p, append(committed, torn...), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := Open(base, "run-1")
	if err != nil {
		t.Fatalf("Open torn tail: %v", err)
	}
	defer s.Close()
	if s.Unavailable() {
		t.Fatal("torn tail must not mark unavailable")
	}
	if len(s.History()) != 2 {
		t.Fatalf("want 2 committed events, got %d", len(s.History()))
	}
	// файл усечён до закоммиченного
	after, _ := os.ReadFile(p)
	if len(after) != len(committed) {
		t.Fatalf("file not truncated to committed: want %d got %d", len(committed), len(after))
	}
}

// битая ПОЛНАЯ строка в середине → оригинал цел, стор unavailable, новая работа блокируется.
func TestOpen_CorruptMidJournalUnavailable(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "side-agent", "run-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	line1 := Event{SchemaVersion: SchemaVersion, Seq: 1, ConversationID: "run-1", TurnID: "t1", Type: EventTurnAccepted}
	l1, _ := line1.Encode()
	line3 := Event{SchemaVersion: SchemaVersion, Seq: 2, ConversationID: "run-1", TurnID: "t1", Type: EventTurnInterrupted}
	l3, _ := line3.Encode()
	orig := append(l1, '\n')
	orig = append(orig, []byte("THIS IS NOT JSON\n")...)
	orig = append(orig, l3...)
	orig = append(orig, '\n')
	p := filepath.Join(dir, "chat.jsonl")
	if err := os.WriteFile(p, orig, 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := Open(base, "run-1")
	// Open возвращает стор (чтобы flow-стадии не вставали), но с ошибкой и unavailable.
	if !errors.Is(err, ErrStorageUnavailable) {
		t.Fatalf("want ErrStorageUnavailable, got %v", err)
	}
	if s == nil {
		t.Fatal("store must be returned even when corrupt")
	}
	defer s.Close()
	if !s.Unavailable() {
		t.Fatal("corrupt journal must mark store unavailable")
	}
	// новая работа блокируется, не проглатывается молча
	if _, aerr := s.Append(EventTurnAccepted, "t2", TurnAcceptedData{Message: "x"}); !errors.Is(aerr, ErrStorageUnavailable) {
		t.Fatalf("append on unavailable: want ErrStorageUnavailable, got %v", aerr)
	}
	// оригинал не тронут
	after, _ := os.ReadFile(p)
	if string(after) != string(orig) {
		t.Fatal("original chat.jsonl was modified")
	}
}

// ошибка записи во время работы останавливает только side-запрос; история сохранена.
func TestAppend_WriteErrorStopsOnlySideRequest(t *testing.T) {
	base := t.TempDir()
	s, _ := Open(base, "run-1")
	_, _ = s.Append(EventTurnAccepted, "t1", TurnAcceptedData{Message: "hi"})
	before := len(s.History())

	// симулируем сбой записи: закрываем нижележащий дескриптор
	s.chatLog.Close()
	_, err := s.Append(EventTurnStarted, "t1", TurnStartedData{Command: "claude"})
	if err == nil {
		t.Fatal("append after fd closed must fail")
	}
	if !s.Unavailable() {
		t.Fatal("write error must mark store unavailable")
	}
	// история в памяти не изменилась (durable-first: state не трогаем до fsync)
	if len(s.History()) != before {
		t.Fatalf("history mutated on write error: want %d got %d", before, len(s.History()))
	}
}

// невалидный переход хода отвергается.
func TestAppend_RejectsInvalidTransition(t *testing.T) {
	base := t.TempDir()
	s, _ := Open(base, "run-1")
	defer s.Close()
	// completed без started
	_, _ = s.Append(EventTurnAccepted, "t1", TurnAcceptedData{Message: "hi"})
	if _, err := s.Append(EventTurnCompleted, "t1", TurnCompletedData{}); err == nil {
		t.Fatal("accepted->completed must be rejected")
	}
}

// Dir/TurnDir дают ожидаемую раскладку; usage.jsonl живёт в том же namespace.
func TestStore_Layout(t *testing.T) {
	base := t.TempDir()
	s, _ := Open(base, "run-1")
	defer s.Close()

	wantDir := filepath.Join(base, "side-agent", "run-1")
	if s.Dir() != wantDir {
		t.Fatalf("Dir: want %q got %q", wantDir, s.Dir())
	}
	td, err := s.TurnDir("t1")
	if err != nil {
		t.Fatalf("TurnDir: %v", err)
	}
	if td != filepath.Join(wantDir, "turns", "t1") {
		t.Fatalf("TurnDir path: %q", td)
	}
	if fi, err := os.Stat(td); err != nil || !fi.IsDir() {
		t.Fatalf("TurnDir must create directory: %v", err)
	}
	// usage.jsonl в том же каталоге
	if filepath.Dir(s.UsagePath()) != wantDir {
		t.Fatalf("usage.jsonl not in conversation namespace: %q", s.UsagePath())
	}
}

// единственный писатель: второй Open того же разговора получает ErrLocked.
func TestOpen_SingleWriter(t *testing.T) {
	base := t.TempDir()
	s, err := Open(base, "run-1")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if _, err := Open(base, "run-1"); !errors.Is(err, ErrLocked) {
		t.Fatalf("second Open: want ErrLocked, got %v", err)
	}
}
