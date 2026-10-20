package main

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf16"
)

const telegramTextLimit = 4096

// Telegram receives these views as plain text. AFM text is therefore literal:
// markup characters cannot turn into Telegram formatting or links.
func textUnits(s string) int { return len(utf16.Encode([]rune(s))) }

func textPrefix(s string, limit int) (string, string) {
	if limit <= 0 {
		return "", s
	}
	used := 0
	for i, r := range s {
		units := 1
		if r > 0xffff {
			units = 2
		}
		if used+units > limit {
			return s[:i], s[i:]
		}
		used += units
	}
	return s, ""
}

func shortText(s string, limit int) string {
	if textUnits(s) <= limit {
		return s
	}
	part, _ := textPrefix(s, limit-textUnits("… [текст сокращён]"))
	return part + "… [текст сокращён]"
}

func onePart(s string, rows ...[]Button) View {
	view := View{Parts: []ViewPart{{}}}
	appendViewText(&view, s)
	view.Parts[len(view.Parts)-1].Buttons = rows
	return view
}

// appendViewText keeps literal source text in order while respecting Telegram's
// UTF-16 message limit. A control is added after its visible item is appended.
func appendViewText(view *View, source string) {
	for source != "" {
		last := &view.Parts[len(view.Parts)-1]
		space := telegramTextLimit - textUnits(last.Text)
		part, rest := textPrefix(source, space)
		if part == "" {
			view.Parts = append(view.Parts, ViewPart{})
			continue
		}
		last.Text += part
		source = rest
	}
}

func appendViewButton(view *View, button Button) {
	last := &view.Parts[len(view.Parts)-1]
	last.Buttons = append(last.Buttons, []Button{button})
}

func btn(label string, in Intent) Button { return Button{Label: label, Intent: in} }

func runName(label string, key RunKey) string {
	if label != "" {
		return label
	}
	if key != "" {
		return string(key)
	}
	return "Запуск"
}

func stateText(state DataState, errText string) string {
	switch state {
	case DataLoading:
		return "Загружаю данные AFM…"
	case DataOffline:
		return "Нет связи с AFM. Показаны последние доступные данные."
	case DataError:
		if errText != "" {
			return "Ошибка: " + errText
		}
		return "Не удалось загрузить данные AFM."
	default:
		return ""
	}
}

func statusTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("02.01.2006 15:04 UTC")
}

// A run key is a random local ID. Show the shortest prefix that distinguishes
// it from the other registrations in this selector.
func runKeyPrefix(key RunKey, runs []RunSummary) string {
	s := string(key)
	for n := min(8, len(s)); n < len(s); n++ {
		prefix := s[:n]
		unique := true
		for _, other := range runs {
			if other.Key != key && strings.HasPrefix(string(other.Key), prefix) {
				unique = false
				break
			}
		}
		if unique {
			return prefix
		}
	}
	return s
}

func runButtonLabel(name, state string, r RunSummary, runs []RunSummary, selected bool) string {
	mark := ""
	if selected {
		mark = "✓ "
	}
	identity := fmt.Sprintf(" · :%d · #%s", r.DashboardPort, runKeyPrefix(r.Key, runs))
	state, _ = textPrefix(state, 16)
	context := " · " + state + identity
	space := 64 - textUnits(mark+context)
	if space <= 0 {
		return mark + strings.TrimPrefix(context, " · ")
	}
	visible, rest := textPrefix(name, space)
	if rest != "" && space > 1 {
		visible, _ = textPrefix(name, space-1)
		visible += "…"
	}
	return mark + visible + context
}

func RenderHome(c HomeCard) View {
	if c.RunKey == "" && c.State == DataEmpty {
		return onePart("Запуски не подключены. Запустите AFM с dashboard и отправьте /connect 9876 (укажите его порт)", []Button{btn("Запуски", Intent{Kind: IntentRuns})})
	}
	var lines []string
	lines = append(lines, "AFM · "+runName(c.RunLabel, c.RunKey))
	if c.StageName != "" {
		lines = append(lines, "Стадия: "+c.StageName)
	}
	if c.Status != "" {
		lines = append(lines, "Состояние: "+c.Status)
	}
	if c.Completed {
		lines = append(lines, "Запуск завершён · история доступна для чтения")
	}
	if c.PendingCount > 0 {
		lines = append(lines, fmt.Sprintf("Требуют внимания: %d", c.PendingCount))
	}
	if s := stateText(c.State, c.Error); s != "" {
		lines = append(lines, s)
	}
	if c.State == DataOffline && !c.LastSeenAt.IsZero() {
		lines = append(lines, "Последнее обновление: "+statusTime(c.LastSeenAt))
	}
	rows := [][]Button{{btn("Запуски", Intent{Kind: IntentRuns}), btn("Входящие", Intent{Kind: IntentInbox})}}
	if c.RunKey != "" {
		rows = append(rows, []Button{btn("Агенты", Intent{Kind: IntentRoster, RunKey: c.RunKey})})
		if c.HasPlan {
			rows = append(rows, []Button{btn("План и действия", Intent{Kind: IntentPlan, RunKey: c.RunKey, StageID: c.StageID})})
		}
		if c.HasQuestion {
			rows = append(rows, []Button{btn("Текущий вопрос", Intent{Kind: IntentRefresh, RunKey: c.RunKey, StageID: c.StageID})})
		}
		if c.CanMessage && c.State == DataReady && !c.Completed {
			rows = append(rows, []Button{btn("Написать агентам", Intent{Kind: IntentCompose, RunKey: c.RunKey, StageID: c.StageID})})
		}
	}
	return onePart(strings.Join(lines, "\n"), rows...)
}

func RenderRuns(c RunCard) View {
	if len(c.Runs) == 0 {
		message := "Не удалось проверить список запусков"
		if c.State == DataReady || c.State == DataEmpty {
			message = "Запуски не подключены. Запустите AFM с dashboard и отправьте /connect 9876 (укажите его порт)"
		}
		if s := stateText(c.State, c.Error); s != "" {
			message += "\n" + s
		}
		return onePart(message, []Button{btn("Обновить", Intent{Kind: IntentRefresh})})
	}
	view := View{Parts: []ViewPart{{}}}
	appendViewText(&view, "Подключённые запуски:")
	for _, r := range c.Runs {
		name := runName(r.Label, r.Key)
		state := r.Status
		if r.State == DataOffline {
			state = "Нет связи"
		} else if state == "" && r.Completed {
			state = "Завершён"
		} else if state == "" {
			state = "Активен"
		}
		line := fmt.Sprintf("%s · %s · dashboard :%d", name, state, r.DashboardPort)
		if r.State == DataOffline && !r.LastSeenAt.IsZero() {
			line += " · обновлено " + statusTime(r.LastSeenAt)
		}
		appendViewText(&view, "\n"+line)
		appendViewButton(&view, btn(runButtonLabel(name, state, r, c.Runs, r.Key == c.RunKey), Intent{Kind: IntentSelectRun, RunKey: r.Key}))
	}
	if s := stateText(c.State, c.Error); s != "" {
		appendViewText(&view, "\n"+s)
	}
	return view
}

func RenderInbox(c InboxCard) View {
	filter := c.Filter
	if filter != "all" {
		filter = "important"
	}
	view := View{Parts: []ViewPart{{}}}
	appendViewText(&view, "Входящие · "+map[bool]string{true: "Все беседы", false: "Важное"}[filter == "all"])
	next := "all"
	label := "Все беседы"
	if filter == "all" {
		next, label = "important", "Важное"
	}
	appendViewButton(&view, btn(label, Intent{Kind: IntentFilter, Filter: next}))
	if len(c.Items) == 0 && (c.State == DataReady || c.State == DataEmpty) {
		if filter == "important" {
			appendViewText(&view, "\nСейчас ничего не требует решения")
		} else {
			appendViewText(&view, "\nПока нет сообщений")
		}
	}
	for _, item := range c.Items {
		appendViewText(&view, "\n"+runName(item.RunLabel, item.RunKey)+" · "+item.Summary)
		in := Intent{RunKey: item.RunKey, StageID: item.StageID, ThreadID: item.ThreadID, QuestionID: item.QuestionID, QuestionPhase: item.QuestionPhase, QuestionDigest: item.QuestionDigest, PlanDigest: item.PlanDigest}
		switch item.Kind {
		case "thread", "message", "mesh":
			in.Kind = IntentThread
		case "plan", "approval":
			in.Kind = IntentPlan
		default:
			in.Kind = IntentRefresh
		}
		appendViewButton(&view, btn(shortText(runName(item.RunLabel, item.RunKey)+" · "+item.Summary, 64), in))
	}
	if s := stateText(c.State, c.Error); s != "" {
		appendViewText(&view, "\n"+s)
	}
	return view
}

func RenderRoster(c RosterCard) View {
	view := View{Parts: []ViewPart{{}}}
	appendViewText(&view, "Агенты · "+runName(c.RunLabel, c.RunKey))
	canMutate := c.State == DataReady && !c.Completed
	if len(c.Agents) == 0 {
		if c.State == DataReady || c.State == DataEmpty {
			appendViewText(&view, "\nАгенты пока не найдены")
		}
	}
	selected := make(map[string]bool, len(c.SelectedRecipientIDs))
	for _, id := range c.SelectedRecipientIDs {
		selected[id] = true
	}
	for _, a := range c.Agents {
		name := a.Name
		if a.Alias != "" {
			name = "@" + a.Alias
		}
		state := "Недоступен"
		if a.Reachable {
			state = "Доступен"
			if a.Running {
				state = "Работает"
			}
		}
		appendViewText(&view, "\n"+name+" · "+state)
		if a.Reachable && canMutate {
			mark := "○ "
			if selected[a.ID] {
				mark = "✓ "
			}
			appendViewButton(&view, btn(shortText(mark+name+" · "+state, 64), Intent{Kind: IntentToggleRecipient, RunKey: c.RunKey, StageID: a.StageID, RecipientIDs: []string{a.ID}}))
			if a.Running && a.CanInterrupt && a.StageID != "" {
				appendViewButton(&view, btn(shortText("Прервать для ответа · "+name, 64), Intent{Kind: IntentInterrupt, RunKey: c.RunKey, StageID: a.StageID, RecipientIDs: []string{a.ID}}))
			}
		}
	}
	if len(c.SelectedRecipientIDs) > 0 && canMutate {
		appendViewButton(&view, btn("Написать выбранным", Intent{Kind: IntentCompose, RunKey: c.RunKey, RecipientIDs: append([]string(nil), c.SelectedRecipientIDs...)}))
	}
	if canMutate && c.RunKey != "" {
		appendViewButton(&view, btn("Добавить агента", Intent{Kind: IntentAddAgent, RunKey: c.RunKey, StageID: c.StageID}))
	}
	if s := stateText(c.State, c.Error); s != "" {
		appendViewText(&view, "\n"+s)
	}
	return view
}

func RenderThread(c ThreadCard) View {
	title := c.Title
	if title == "" {
		title = c.ThreadID
	}
	view := View{Parts: []ViewPart{{}}}
	appendViewText(&view, "Беседа · "+runName(c.RunLabel, c.RunKey)+"\n"+title)
	if len(c.Messages) == 0 {
		if c.State == DataReady || c.State == DataEmpty {
			appendViewText(&view, "\nСообщений на этой странице нет")
			if !c.HasMore {
				appendViewText(&view, "\nКонец доступной истории")
			}
		}
		if c.RunKey != "" && c.ThreadID != "" {
			appendViewButton(&view, btn("Открыть беседу с начала", Intent{Kind: IntentThread, RunKey: c.RunKey, ThreadID: c.ThreadID}))
		}
		appendViewButton(&view, btn("К входящим", Intent{Kind: IntentInbox, Filter: c.Filter}))
	}
	for _, m := range c.Messages {
		name := m.SenderName
		if name == "" {
			name = m.SenderID
		}
		appendViewText(&view, "\n"+name+": "+m.Text)
		if m.SenderID != "" && m.SenderID != "user" && c.State == DataReady && !c.Completed {
			appendViewButton(&view, btn(shortText("Ответить "+name, 64), Intent{Kind: IntentReply, RunKey: c.RunKey, ThreadID: c.ThreadID, InReplyTo: m.ID, RecipientIDs: []string{m.SenderID}}))
		}
	}
	if c.HasMore {
		appendViewText(&view, "\nЕщё сообщения")
		appendViewButton(&view, btn("Ещё сообщения", Intent{Kind: IntentLoadMore, RunKey: c.RunKey, ThreadID: c.ThreadID, Filter: c.Filter}))
	}
	if s := stateText(c.State, c.Error); s != "" {
		appendViewText(&view, "\n"+s)
	}
	return view
}

func receiptLabel(status ReceiptStatus) string {
	switch status {
	case ReceiptQueued:
		return "В очереди · AFM ещё не принял"
	case ReceiptAccepted:
		return "Принято AFM"
	case ReceiptReplied:
		return "Агент ответил"
	case ReceiptFailed:
		return "Не доставлено"
	case ReceiptUnknown:
		return "Результат неизвестен · проверяю AFM"
	default:
		return "Статус уточняется"
	}
}

func RenderReceipt(c ReceiptCard) View {
	lines := []string{"Отправка · " + runName(c.RunLabel, c.RunKey), receiptLabel(c.Status)}
	if c.Text != "" {
		lines = append(lines, "Сообщение: "+c.Text)
	}
	for _, r := range c.Recipients {
		name := r.Name
		if name == "" {
			name = r.ID
		}
		line := name + " · " + receiptLabel(r.Status)
		if r.FailureReason != "" {
			line += " · " + r.FailureReason
		}
		lines = append(lines, line)
	}
	if c.FailureReason != "" {
		lines = append(lines, "Причина: "+c.FailureReason)
	}
	if s := stateText(c.State, c.Error); s != "" {
		lines = append(lines, s)
	}
	var rows [][]Button
	if c.ThreadID != "" {
		rows = append(rows, []Button{btn("Открыть беседу", Intent{Kind: IntentThread, RunKey: c.RunKey, ThreadID: c.ThreadID})})
	}
	return onePart(strings.Join(lines, "\n"), rows...)
}

func RenderComposer(c ComposerCard) View {
	heading := "Сообщение · "
	if c.Mode == "question" {
		heading = "Ответ на вопрос · "
	}
	lines := []string{heading + runName(c.RunLabel, c.RunKey)}
	stage := c.StageName
	if stage == "" {
		stage = c.StageID
	}
	if stage != "" {
		lines = append(lines, "Стадия: "+stage)
	}
	if c.Mode == "question" && c.QuestionID != "" {
		lines = append(lines, "Вопрос: "+c.QuestionID)
	}
	if c.Mode == "question" && c.QuestionPrompt != "" {
		lines = append(lines, "Текст вопроса: "+c.QuestionPrompt)
	}
	if len(c.RecipientNames) > 0 {
		lines = append(lines, "Кому: "+strings.Join(c.RecipientNames, ", "))
	} else if len(c.RecipientIDs) > 0 {
		lines = append(lines, "Кому: "+strings.Join(c.RecipientIDs, ", "))
	}
	if c.InReplyTo != "" {
		lines = append(lines, "Ответ "+c.ReplyAuthor+" в исходной беседе: "+c.ReplyText)
	}
	if c.Interrupt {
		lines = append(lines, "Прервать текущий ход агента для ответа")
	}
	if c.Draft != "" {
		lines = append(lines, "Черновик: "+c.Draft)
	} else {
		lines = append(lines, "Введите текст следующим сообщением")
	}
	if s := stateText(c.State, c.Error); s != "" {
		lines = append(lines, s)
	}
	rows := [][]Button{{btn("Отмена", Intent{Kind: IntentCancel, RunKey: c.RunKey})}}
	if c.CanInterrupt && c.State == DataReady && c.StageID != "" {
		label := "Прервать для ответа"
		mode := ""
		if c.Interrupt {
			label = "Не прерывать"
			mode = "off"
		}
		rows = append(rows, []Button{btn(label, Intent{Kind: IntentInterrupt, RunKey: c.RunKey, StageID: c.StageID, ThreadID: c.ThreadID, InReplyTo: c.InReplyTo, RecipientIDs: append([]string(nil), c.RecipientIDs...), Mode: mode})})
	}
	return onePart(strings.Join(lines, "\n"), rows...)
}
