package sideagent

import (
	"errors"
	"strings"
	"testing"
)

// helper: собрать историю из типизированных событий в порядке отправки.
func ev(t *testing.T, typ EventType, turnID string, data any) Event {
	t.Helper()
	e := Event{SchemaVersion: SchemaVersion, Type: typ, TurnID: turnID, ConversationID: "run-1"}
	if err := e.SetData(data); err != nil {
		t.Fatal(err)
	}
	return e
}

// Первый промпт: короткая инструкция обычного разговора + текущее сообщение.
func TestBuild_FirstPrompt(t *testing.T) {
	b := ContextBuilder{ByteLimit: 100000}
	p, err := b.Build(nil, "первый вопрос")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(p, "первый вопрос") {
		t.Fatal("prompt must contain current message")
	}
	// должна быть инструкция обычного разговора
	if !strings.Contains(p, DefaultInstruction) {
		t.Fatal("prompt must contain the normal-conversation instruction")
	}
}

// Второй промпт содержит сообщения первой отправки (история), а текущее
// сообщение подано отдельно как новое.
func TestBuild_SecondPromptContainsFirstSend(t *testing.T) {
	history := []Event{
		ev(t, EventTurnAccepted, "t1", TurnAcceptedData{Message: "первый вопрос"}),
		ev(t, EventTurnStarted, "t1", TurnStartedData{Command: "claude"}),
		ev(t, EventAssistantText, "t1", AssistantTextData{Text: "первый ответ"}),
		ev(t, EventTurnCompleted, "t1", TurnCompletedData{}),
	}
	b := ContextBuilder{ByteLimit: 100000}
	p, err := b.Build(history, "второй вопрос")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(p, "первый вопрос") || !strings.Contains(p, "первый ответ") {
		t.Fatal("second prompt must contain first send's messages")
	}
	if !strings.Contains(p, "второй вопрос") {
		t.Fatal("second prompt must contain current message")
	}
}

// Историческая инструкция (прошлое сообщение пользователя) подаётся как история,
// а не как новая инструкция: прошлый вопрос — под history-заголовком и ДО текущего.
func TestBuild_HistoricalInstructionMarkedAsHistory(t *testing.T) {
	history := []Event{
		ev(t, EventTurnAccepted, "t1", TurnAcceptedData{Message: "старая инструкция"}),
		ev(t, EventAssistantText, "t1", AssistantTextData{Text: "ответ"}),
	}
	b := ContextBuilder{ByteLimit: 100000}
	p, err := b.Build(history, "новая инструкция")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	hi := strings.Index(p, HistoryHeading)
	ci := strings.Index(p, CurrentHeading)
	if hi < 0 || ci < 0 || hi >= ci {
		t.Fatalf("history heading must precede current heading: hi=%d ci=%d", hi, ci)
	}
	// старая инструкция — в секции истории (до current-заголовка)
	oldIdx := strings.Index(p, "старая инструкция")
	if oldIdx < 0 || oldIdx > ci {
		t.Fatal("old instruction must be in the history section, not re-fed as new")
	}
	// новая инструкция — после current-заголовка
	newIdx := strings.LastIndex(p, "новая инструкция")
	if newIdx < ci {
		t.Fatal("current message must be under the current heading")
	}
}

// Сообщения пользователя и видимые ответы — без UI-усечения (полный текст).
func TestBuild_NoUITruncation(t *testing.T) {
	long := strings.Repeat("длинный текст ", 500)
	history := []Event{
		ev(t, EventTurnAccepted, "t1", TurnAcceptedData{Message: long}),
		ev(t, EventAssistantText, "t1", AssistantTextData{Text: long}),
	}
	b := ContextBuilder{ByteLimit: 10_000_000}
	p, err := b.Build(history, "ok")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if strings.Count(p, long) < 2 {
		t.Fatal("full user message and answer must appear untruncated")
	}
}

// Действия инструментов — компактно, со ссылкой на подробный лог.
func TestBuild_ToolActionsCompactWithLogPointer(t *testing.T) {
	history := []Event{
		ev(t, EventTurnAccepted, "t1", TurnAcceptedData{Message: "сделай"}),
		ev(t, EventToolAction, "t1", ToolActionData{Tool: "Bash", Detail: "ls -la", LogRef: "turns/t1/agent.log"}),
	}
	b := ContextBuilder{ByteLimit: 100000}
	p, err := b.Build(history, "ok")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(p, "Bash") || !strings.Contains(p, "turns/t1/agent.log") {
		t.Fatal("tool action must appear compactly with a pointer to its detailed log")
	}
}

// Превышение байтового лимита → context_limit; история и черновик сохранены
// (старые сообщения НЕ усекаются молча).
func TestBuild_ByteLimitExceeded(t *testing.T) {
	big := strings.Repeat("x", 5000)
	history := []Event{
		ev(t, EventTurnAccepted, "t1", TurnAcceptedData{Message: big}),
		ev(t, EventAssistantText, "t1", AssistantTextData{Text: big}),
	}
	b := ContextBuilder{ByteLimit: 1000}
	_, err := b.Build(history, "ещё")
	if !errors.Is(err, ErrContextLimit) {
		t.Fatalf("want ErrContextLimit, got %v", err)
	}
	// история не тронута вызовом (Build чистый)
	if len(history) != 2 {
		t.Fatal("history must be preserved")
	}
	// с большим лимитом те же старые сообщения на месте — ничего не усекли
	b.ByteLimit = 10_000_000
	p, err := b.Build(history, "ещё")
	if err != nil {
		t.Fatalf("Build with large limit: %v", err)
	}
	if strings.Count(p, big) < 2 {
		t.Fatal("old messages must not have been silently trimmed")
	}
}

// Контракт для downstream: версия формата контекста зафиксирована.
func TestContextFormatVersion(t *testing.T) {
	if ContextFormatVersion == "" {
		t.Fatal("ContextFormatVersion must be set")
	}
}
