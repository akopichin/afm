package sideagent

import (
	"errors"
	"fmt"
	"strings"

	"github.com/akopichin/afm/pkg/prompts"
)

// ContextFormatVersion — версия формата сборки промпта. Фиксируется в событии
// turn_started, чтобы восстановление и downstream знали, как был собран контекст.
const ContextFormatVersion = "v2"

// Заголовки секций промпта (экспортированы для тестов и downstream-навигации).
const (
	HistoryHeading = "## Предыдущие сообщения (история)"
	CurrentHeading = "## Текущее сообщение"
)

// DefaultInstruction — короткая инструкция обычного разговора. Прошлые сообщения даются как
// контекст-история: их не нужно переисполнять, отвечать нужно только на текущее сообщение.
const DefaultInstruction = "Ты ведёшь обычный разговор в боковом чате по текущему проекту. " +
	"Блок истории ниже — только контекст прошлых реплик; не переисполняй прошлые просьбы. " +
	"Ответь только на текущее сообщение."

// ErrContextLimit — собранный промпт превысил явный байтовый бюджет.
var ErrContextLimit = errors.New("sideagent: assembled prompt exceeds byte budget")

// ContextBuilder собирает промпт бокового разговора: AFM формирует контекст сам, поэтому каждая отправка —
// свежий вызов агента с полным промптом из истории + текущего сообщения.
type ContextBuilder struct {
	// Instruction переопределяет инструкцию обычного разговора; пусто →
	// DefaultInstruction.
	Instruction string
	// ByteLimit — явный верхний предел размера промпта в байтах. <= 0 означает
	// «без предела». Превышение → ErrContextLimit (без молчаливого усечения).
	ByteLimit int
}

// Build собирает промпт: инструкция + история прошлых сообщений в порядке отправки + текущее сообщение.
// Метод чистый: он не мутирует history и не усекает её.
func (b ContextBuilder) Build(history []Event, current TurnAcceptedData) (string, error) {
	instruction := b.Instruction
	if instruction == "" {
		instruction = DefaultInstruction
	}

	var sb strings.Builder
	sb.WriteString(instruction)
	sb.WriteString("\n\n")

	if lines := renderHistory(history); len(lines) > 0 {
		sb.WriteString(HistoryHeading)
		sb.WriteString("\n")
		for _, l := range lines {
			sb.WriteString(l)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	sb.WriteString(CurrentHeading)
	sb.WriteString("\n")
	if current.SourceMessageID != "" {
		sb.WriteString(prompts.AgentCommInboxBlock([]prompts.AgentCommInboxMessage{{
			MessageID: current.SourceMessageID,
			ThreadID:  current.SourceThreadID,
			From:      peerLabel(current),
			Body:      current.Message,
		}}, 0))
	} else {
		sb.WriteString(current.Message)
		sb.WriteString("\n")
	}

	prompt := sb.String()
	if b.ByteLimit > 0 && len(prompt) > b.ByteLimit {
		return "", fmt.Errorf("%w: %d > %d bytes", ErrContextLimit, len(prompt), b.ByteLimit)
	}
	return prompt, nil
}

// renderHistory превращает события в строки истории в порядке отправки.
func renderHistory(history []Event) []string {
	var lines []string
	for _, e := range history {
		switch e.Type {
		case EventTurnAccepted:
			var d TurnAcceptedData
			if e.DecodeData(&d) == nil && d.Message != "" {
				if d.SourceMessageID != "" {
					lines = append(lines, quoteHistoryEntry("From "+peerLabel(d)+" ["+prompts.EscapeTagsForReprompt(d.SourceMessageID)+"] in thread "+prompts.EscapeTagsForReprompt(d.SourceThreadID)+": "+prompts.EscapeTagsForReprompt(d.Message)))
				} else {
					lines = append(lines, "Пользователь: "+d.Message)
				}
			}
		case EventAssistantText:
			var d AssistantTextData
			if e.DecodeData(&d) == nil && d.Text != "" {
				lines = append(lines, "Агент: "+d.Text)
			}
		case EventToolAction:
			var d ToolActionData
			if e.DecodeData(&d) == nil {
				lines = append(lines, formatToolAction(d))
			}
		default:
			// FSM-маркеры (started/completed/failed/interrupted) в промпт не идут.
		}
	}
	return lines
}

// Each line of a historical peer message stays inside a Markdown quotation,
// including blank lines and lines that resemble prompt headings or role labels.
func quoteHistoryEntry(entry string) string {
	entry = strings.ReplaceAll(entry, "\r\n", "\n")
	entry = strings.ReplaceAll(entry, "\r", "\n")
	return "> " + strings.ReplaceAll(entry, "\n", "\n> ")
}

func peerLabel(d TurnAcceptedData) string {
	name := d.SourceSenderName
	if name == "" {
		name = d.SourceSenderID
	}
	cleanLine := strings.NewReplacer("\r", " ", "\n", " ").Replace
	return prompts.EscapeTagsForReprompt(cleanLine(name)) + " (" + prompts.EscapeTagsForReprompt(cleanLine(d.SourceSenderID)) + ")"
}

// formatToolAction — компактная строка действия со ссылкой на подробный лог.
func formatToolAction(d ToolActionData) string {
	var sb strings.Builder
	sb.WriteString("Действие: ")
	sb.WriteString(d.Tool)
	if d.Detail != "" {
		sb.WriteString(" — ")
		sb.WriteString(d.Detail)
	}
	if d.LogRef != "" {
		sb.WriteString(" (подробности: ")
		sb.WriteString(d.LogRef)
		sb.WriteString(")")
	}
	return sb.String()
}
