package sideagent

import (
	"errors"
	"fmt"
	"strings"

	"github.com/akopichin/afm/pkg/prompts"
)

// ContextFormatVersion — версия формата сборки промпта. Фиксируется в событии
// turn_started, чтобы восстановление и downstream знали, как был собран контекст.
const ContextFormatVersion = "v3"

// Заголовки секций промпта (экспортированы для тестов и downstream-навигации).
const (
	HistoryHeading = "## Предыдущие сообщения (история)"
	CurrentHeading = "## Текущее сообщение"
)

// DefaultInstruction — короткая инструкция обычного разговора. Прошлые сообщения даются как
// контекст-история: их не нужно переисполнять, отвечать нужно только на текущие сообщения.
const DefaultInstruction = "Ты ведёшь обычный разговор в боковом чате по текущему проекту. " +
	"Блок истории ниже — только контекст прошлых реплик; не переисполняй прошлые просьбы. " +
	"Ответь только на текущее сообщение; если их несколько, ответь на все сразу, учитывая поздние уточнения."

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
	// HideSource reports a source message whose turn the history must not show. nil → nothing is hidden.
	HideSource func(sourceMessageID string) bool
}

// head собирает промпт до текущих сообщений: инструкция, история и заголовок текущего блока.
func (b ContextBuilder) head(history []Event) string {
	instruction := b.Instruction
	if instruction == "" {
		instruction = DefaultInstruction
	}

	var sb strings.Builder
	sb.WriteString(instruction)
	sb.WriteString("\n\n")
	if lines := b.renderHistory(history); len(lines) > 0 {
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
	return sb.String()
}

// Build собирает промпт одного сообщения: инструкция + история прошлых сообщений в порядке отправки +
// текущее сообщение (Mesh-запрос оформляется как inbox-блок). Метод чистый: он не мутирует history.
func (b ContextBuilder) Build(history []Event, current TurnAcceptedData) (string, error) {
	var sb strings.Builder
	sb.WriteString(b.head(history))
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

// BuildGroup собирает промпт одного хода для группы сообщений очереди и возвращает, сколько сообщений
// группы поместилось (наибольший префикс). Голова, которая не помещается, — ErrContextLimit. Инструкция и
// история рендерятся один раз; разметка каждого сообщения не зависит от размера группы.
func (b ContextBuilder) BuildGroup(history []Event, group []MessageInfo) (string, int, error) {
	if len(group) == 0 {
		return "", 0, errors.New("sideagent: empty message group")
	}
	var sb strings.Builder
	sb.WriteString(b.head(history))
	used := 0
	for i, msg := range group {
		piece := fmt.Sprintf("[Сообщение %d]\n%s\n[Конец сообщения %d]\n", i+1, msg.Text, i+1)
		if b.ByteLimit > 0 && sb.Len()+len(piece) > b.ByteLimit {
			break
		}
		sb.WriteString(piece)
		used++
	}
	if used == 0 {
		return "", 0, fmt.Errorf("%w: head alone needs more than %d bytes", ErrContextLimit, b.ByteLimit)
	}
	return sb.String(), used, nil
}

// renderHistory превращает события в строки истории в порядке отправки.
func (b ContextBuilder) renderHistory(history []Event) []string {
	hidden := b.hiddenTurns(history)
	queuedText := queuedTexts(history)
	var lines []string
	for _, e := range history {
		if hidden[e.TurnID] {
			continue
		}
		switch e.Type {
		case EventTurnAccepted:
			var d TurnAcceptedData
			if e.DecodeData(&d) != nil {
				continue
			}
			// Групповой ход показывает сообщения в позиции claim, а не приёма: между ними мог идти ответ.
			for _, id := range d.MessageIDs {
				lines = append(lines, "Пользователь: "+queuedText[id])
			}
			if d.Message == "" {
				continue
			}
			if d.SourceMessageID != "" {
				lines = append(lines, quoteHistoryEntry("From "+peerLabel(d)+" ["+prompts.EscapeTagsForReprompt(d.SourceMessageID)+"] in thread "+prompts.EscapeTagsForReprompt(d.SourceThreadID)+": "+prompts.EscapeTagsForReprompt(d.Message)))
			} else {
				lines = append(lines, "Пользователь: "+d.Message)
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

// queuedTexts индексирует тексты сообщений очереди; в историю они попадают только вместе с claim-ходом.
func queuedTexts(history []Event) map[string]string {
	texts := map[string]string{}
	for _, e := range history {
		var d MessageQueuedData
		if e.Type == EventMessageQueued && e.DecodeData(&d) == nil {
			texts[d.MessageID] = d.Message
		}
	}
	return texts
}

// hiddenTurns returns the turns started by a hidden source; the Assistant's answer repeats what the source said.
func (b ContextBuilder) hiddenTurns(history []Event) map[string]bool {
	if b.HideSource == nil {
		return nil
	}
	hidden := map[string]bool{}
	for _, e := range history {
		var d TurnAcceptedData
		if e.Type == EventTurnAccepted && e.DecodeData(&d) == nil && d.SourceMessageID != "" && b.HideSource(d.SourceMessageID) {
			hidden[e.TurnID] = true
		}
	}
	return hidden
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
