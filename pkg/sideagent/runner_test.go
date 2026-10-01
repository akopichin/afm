package sideagent

import (
	"context"
	"testing"

	"github.com/akopichin/afm/pkg/executor"
)

// mapAction — чистая функция; тестируем маппинг действие→событие, редакцию
// секретов и проброс log_ref без запуска подпроцесса.

func TestMapAction_TextIsAssistantProse(t *testing.T) {
	typ, data := mapAction("text", "привет из агента", "turns/t-1/agent.log", nil)
	if typ != EventAssistantText {
		t.Fatalf("type = %q, want %q", typ, EventAssistantText)
	}
	d, ok := data.(AssistantTextData)
	if !ok {
		t.Fatalf("data type = %T, want AssistantTextData", data)
	}
	if d.Text != "привет из агента" {
		t.Fatalf("text = %q", d.Text)
	}
}

func TestMapAction_ToolCarriesLogRef(t *testing.T) {
	typ, data := mapAction("Bash", "ls -la", "turns/t-1/agent.log", nil)
	if typ != EventToolAction {
		t.Fatalf("type = %q, want %q", typ, EventToolAction)
	}
	d, ok := data.(ToolActionData)
	if !ok {
		t.Fatalf("data type = %T, want ToolActionData", data)
	}
	if d.Tool != "Bash" || d.Detail != "ls -la" {
		t.Fatalf("tool/detail = %q/%q", d.Tool, d.Detail)
	}
	if d.LogRef != "turns/t-1/agent.log" {
		t.Fatalf("log_ref = %q", d.LogRef)
	}
}

func TestMapAction_RedactsAssistantText(t *testing.T) {
	typ, data := mapAction("text", "токен это TOPSECRET ок", "", []string{"TOPSECRET"})
	if typ != EventAssistantText {
		t.Fatalf("type = %q", typ)
	}
	d := data.(AssistantTextData)
	if d.Text == "токен это TOPSECRET ок" {
		t.Fatalf("секрет не отредактирован: %q", d.Text)
	}
	if want := "токен это [REDACTED] ок"; d.Text != want {
		t.Fatalf("text = %q, want %q", d.Text, want)
	}
}

func TestMapAction_RedactsToolDetail(t *testing.T) {
	_, data := mapAction("Bash", "curl -H 'Authorization: TOPSECRET'", "", []string{"TOPSECRET"})
	d := data.(ToolActionData)
	if want := "curl -H 'Authorization: [REDACTED]'"; d.Detail != want {
		t.Fatalf("detail = %q, want %q", d.Detail, want)
	}
}

func TestMapAction_NoSecretsIsNoop(t *testing.T) {
	_, data := mapAction("text", "обычный текст", "", nil)
	if data.(AssistantTextData).Text != "обычный текст" {
		t.Fatalf("текст изменён без секретов")
	}
}

// Реальная фабрика строит AgentRunner поверх executor.New — проверяем только,
// что тип удовлетворяет интерфейсу (без запуска подпроцесса).
func TestNewExecutorRunner_SatisfiesInterface(t *testing.T) {
	var r AgentRunner = NewExecutorRunner(executor.Config{Command: "true"})
	if r == nil {
		t.Fatal("NewExecutorRunner вернул nil")
	}
	// Вызов RunAgent здесь не делаем — он запустил бы подпроцесс.
	_ = context.Background()
}
