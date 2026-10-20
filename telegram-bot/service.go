package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	afmsdk "github.com/akopichin/afm/sdk"
)

type Service struct {
	Registry *Registry
	Store    *Store
	Telegram *TelegramTransport
	Allowed  map[int64]bool
}

type textUpdateIDKey struct{}

func markTextProcessed(st *State, ctx context.Context) {
	if id, ok := ctx.Value(textUpdateIDKey{}).(int64); ok {
		st.ProcessedText[id] = true
	}
}

func (s *Service) setUserForText(ctx context.Context, id int64, fn func(*UserSession)) error {
	return s.Store.Update(func(st *State) error {
		u := st.Users[id]
		fn(&u)
		st.Users[id] = u
		markTextProcessed(st, ctx)
		return nil
	})
}

func NewService(reg *Registry, tg *TelegramTransport, allowed map[int64]bool) *Service {
	return &Service{Registry: reg, Store: reg.Store, Telegram: tg, Allowed: allowed}
}
func (s *Service) allowedMessage(m *TelegramMessage) bool {
	return m != nil && m.Chat.Type == "private" && s.Allowed[m.From.ID] && m.Chat.ID == m.From.ID
}
func (s *Service) HandleUpdate(ctx context.Context, u TelegramUpdate) error {
	if u.Callback != nil {
		c := u.Callback
		if c.Message == nil || !s.allowedMessage(&TelegramMessage{From: c.From, Chat: c.Message.Chat}) {
			_ = s.Telegram.AnswerCallback(ctx, c.ID, "Доступ запрещён")
			return nil
		}
		err := s.handleCallback(ctx, c)
		if err != nil {
			return err
		}
		return s.Telegram.AnswerCallback(ctx, c.ID, "")
	}
	if !s.allowedMessage(u.Message) {
		return nil
	}
	if s.Store.Snapshot().ProcessedText[u.UpdateID] {
		return nil
	}
	ctx = context.WithValue(ctx, textUpdateIDKey{}, u.UpdateID)
	m := u.Message
	text := strings.TrimSpace(m.Text)
	var err error
	if strings.HasPrefix(text, "/") {
		err = s.command(ctx, m.From.ID, text)
	} else {
		err = s.handleText(ctx, m.From.ID, m.Text)
	}
	if err != nil {
		return err
	}
	return s.Store.Update(func(st *State) error { markTextProcessed(st, ctx); return nil })
}
func (s *Service) sendText(ctx context.Context, user int64, message string) error {
	if message == "" {
		message = " "
	}
	for message != "" {
		part, rest := textPrefix(message, telegramTextLimit)
		if err := s.Telegram.SendMessage(ctx, user, part, nil); err != nil {
			return err
		}
		message = rest
	}
	return nil
}
func (s *Service) sendView(ctx context.Context, user int64, v View) error {
	for _, part := range v.Parts {
		rows := make([][]TelegramButton, 0, len(part.Buttons))
		for _, row := range part.Buttons {
			buttons := make([]TelegramButton, 0, len(row))
			for _, b := range row {
				id, err := randomID()
				if err != nil {
					return err
				}
				err = s.Store.Update(func(st *State) error {
					st.Callbacks[id] = CallbackRecord{UserID: user, Intent: b.Intent, CreatedAt: time.Now().UTC()}
					return nil
				})
				if err != nil {
					return err
				}
				buttons = append(buttons, TelegramButton{Text: b.Label, CallbackData: id})
			}
			rows = append(rows, buttons)
		}
		if err := s.Telegram.SendMessage(ctx, user, part.Text, rows); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) user(id int64) UserSession { return s.Store.Snapshot().Users[id] }
func (s *Service) setUser(id int64, fn func(*UserSession)) error {
	return s.Store.Update(func(st *State) error { u := st.Users[id]; fn(&u); st.Users[id] = u; return nil })
}
func composerDiscardNotice(comp *ComposerContext) string {
	message := "Контекст ввода сброшен при смене запуска/действия"
	if comp == nil {
		return message
	}
	if comp.Draft != "" {
		message += ". Черновик для копирования: " + comp.Draft
	} else if comp.Instruction != "" {
		message += ". Задача для копирования: " + comp.Instruction
	}
	if comp.FirstMessage != "" {
		message += ". Первое сообщение для копирования: " + comp.FirstMessage
	}
	return message
}
func (s *Service) replaceComposer(ctx context.Context, user int64, comp *ComposerContext) error {
	old := s.user(user).Composer
	if err := s.setUser(user, func(u *UserSession) { u.Composer = comp }); err != nil {
		return err
	}
	if old != nil {
		return s.sendText(ctx, user, composerDiscardNotice(old))
	}
	return nil
}
func (s *Service) selected(id int64) RunKey {
	u := s.user(id)
	if u.Selected != "" {
		return u.Selected
	}
	st := s.Store.Snapshot()
	keys := make([]string, 0, len(st.Runs))
	for k, b := range st.Runs {
		if !b.Invalidated {
			keys = append(keys, string(k))
		}
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		return RunKey(keys[0])
	}
	return ""
}
func (s *Service) command(ctx context.Context, user int64, raw string) error {
	parts := strings.Fields(raw)
	command := strings.SplitN(parts[0], "@", 2)[0]
	switch command {
	case "/start", "/status":
		return s.showHome(ctx, user, s.selected(user))
	case "/runs":
		return s.showRuns(ctx, user, "")
	case "/connect":
		if len(parts) != 2 {
			return s.sendText(ctx, user, "Укажите порт dashboard: /connect 9876")
		}
		port, err := parsePort(parts[1])
		if err != nil {
			return s.sendText(ctx, user, "Неверный порт dashboard")
		}
		b, err := s.Registry.Register(ctx, port)
		if err != nil {
			return s.sendText(ctx, user, fmt.Sprintf("Не удалось подключить AFM на порту %d. Проверьте dashboard и повторите /connect %d", port, port))
		}
		old := s.user(user)
		if err := s.setUser(user, func(u *UserSession) {
			if u.Selected != b.Key {
				u.Selected = b.Key
				u.Composer = nil
				u.Recipients = nil
			}
		}); err != nil {
			return err
		}
		if old.Selected != b.Key && old.Composer != nil {
			if err := s.sendText(ctx, user, composerDiscardNotice(old.Composer)); err != nil {
				return err
			}
		}
		return s.sendText(ctx, user, fmt.Sprintf("Подключён запуск %s · dashboard :%d", b.Label, b.DashboardPort))
	case "/agents":
		return s.showRoster(ctx, user, s.selected(user))
	case "/inbox":
		return s.showInbox(ctx, user)
	case "/cancel":
		if err := s.setUser(user, func(u *UserSession) { u.Composer = nil }); err != nil {
			return err
		}
		return s.sendText(ctx, user, "Действие отменено")
	case "/settings":
		return s.sendText(ctx, user, "Настройки: /runs — выбрать запуск; /inbox — входящие; /cancel — отменить ввод")
	case "/help":
		return s.sendText(ctx, user, "Команды: /start /connect PORT /runs /agents /inbox /status /settings /help /cancel")
	default:
		return s.sendText(ctx, user, "Неизвестная команда. /help")
	}
}
func (s *Service) showRuns(ctx context.Context, user int64, problem string) error {
	st := s.Store.Snapshot()
	keys := make([]string, 0, len(st.Runs))
	for k := range st.Runs {
		keys = append(keys, string(k))
	}
	sort.Strings(keys)
	card := RunCard{State: DataReady, RunKey: s.selected(user), Error: problem}
	for _, key := range keys {
		b := st.Runs[RunKey(key)]
		state := DataReady
		status := ""
		if b.Invalidated {
			status = "Запуск заменён · подключите новый: /connect " + strconv.Itoa(b.DashboardPort)
		} else if b.Offline {
			state = DataOffline
		}
		card.Runs = append(card.Runs, RunSummary{Key: b.Key, Label: b.Label, DashboardPort: b.DashboardPort, State: state, Status: status, LastSeenAt: b.LastSeenAt, Completed: b.Completed})
	}
	if len(card.Runs) == 0 {
		card.State = DataEmpty
	}
	return s.sendView(ctx, user, RenderRuns(card))
}
func firstStage(status afmsdk.RunStatus, id string) (afmsdk.StageView, bool) {
	if id != "" {
		for _, v := range status.StageViews {
			if v.ID == id {
				return v, true
			}
		}
		return afmsdk.StageView{}, false
	}
	if status.RunState == "failed" {
		for _, v := range status.StageViews {
			if v.Status == afmsdk.StageFailed || v.Status == afmsdk.StageHookFailed {
				return v, true
			}
		}
	}
	for _, v := range status.StageViews {
		if v.Status == afmsdk.StageAwaitingApproval || v.Status == afmsdk.StageAwaitingUserInput || v.Status == afmsdk.StagePaused {
			return v, true
		}
	}
	for _, v := range status.StageViews {
		switch v.Status {
		case afmsdk.StageRunning, afmsdk.StagePlanning, afmsdk.StageRevising, afmsdk.StageRetrying, afmsdk.StageAwaitingAssistants, afmsdk.StageAwaitingPeer:
			return v, true
		}
	}
	for _, v := range status.StageViews {
		if v.Status == afmsdk.StageFailed || v.Status == afmsdk.StageHookFailed {
			return v, true
		}
	}
	for _, v := range status.StageViews {
		if v.Status != afmsdk.StageDone {
			return v, true
		}
	}
	if len(status.StageViews) > 0 {
		return status.StageViews[len(status.StageViews)-1], true
	}
	return afmsdk.StageView{}, false
}
func stageStatusLabel(status afmsdk.StageStatus) string {
	switch status {
	case afmsdk.StagePending:
		return "Ожидает запуска"
	case afmsdk.StagePlanning:
		return "Готовит план"
	case afmsdk.StageAwaitingApproval:
		return "Ждёт одобрения"
	case afmsdk.StageRevising:
		return "Дорабатывает план"
	case afmsdk.StageReady:
		return "Готова к запуску"
	case afmsdk.StageRunning:
		return "Работает"
	case afmsdk.StageRetrying:
		return "Повторяет попытку"
	case afmsdk.StageAwaitingUserInput:
		return "Ждёт ответа"
	case afmsdk.StageDone:
		return "Завершена"
	case afmsdk.StageFailed, afmsdk.StageHookFailed:
		return "Ошибка"
	case afmsdk.StageAwaitingAssistants:
		return "Ждёт помощников"
	case afmsdk.StageAwaitingPeer:
		return "Ждёт коллегу"
	case afmsdk.StagePaused:
		return "На паузе"
	default:
		return string(status)
	}
}
func (s *Service) showHome(ctx context.Context, user int64, key RunKey) error {
	if key == "" {
		return s.sendView(ctx, user, RenderHome(HomeCard{State: DataEmpty}))
	}
	b, ok := s.Store.Run(key)
	if !ok {
		return s.showRuns(ctx, user, "")
	}
	card := HomeCard{State: DataReady, RunKey: key, RunLabel: b.Label, LastSeenAt: b.LastSeenAt, Completed: b.Completed}
	_, status, err := s.Registry.Resolve(ctx, key, false)
	if err != nil {
		if errors.Is(err, ErrStaleRun) {
			card.State = DataError
			card.Error = fmt.Sprintf("Запуск заменён. Подключите новый: /connect %d", b.DashboardPort)
		} else {
			card.State = DataOffline
			card.Error = err.Error()
		}
	} else {
		card.Completed = terminal(status)
		if v, ok := firstStage(status, ""); ok {
			card.StageID = v.ID
			card.StageName = v.Name
			card.Status = stageStatusLabel(v.Status)
			card.HasPlan = v.ShowPlan
			card.HasQuestion = v.ShowDialog
		}
		card.CanMessage = status.Capabilities.Collaboration && !card.Completed
	}
	return s.sendView(ctx, user, RenderHome(card))
}
func (s *Service) showRoster(ctx context.Context, user int64, key RunKey) error {
	b, ok := s.Store.Run(key)
	if !ok {
		return s.showRuns(ctx, user, "")
	}
	card := RosterCard{State: DataReady, RunKey: key, RunLabel: b.Label, SelectedRecipientIDs: s.user(user).Recipients, Completed: b.Completed}
	run, status, err := s.Registry.Resolve(ctx, key, false)
	if err != nil {
		card.State = DataOffline
		card.Agents = s.Store.Snapshot().RosterCache[key]
		card.Error = err.Error()
		return s.sendView(ctx, user, RenderRoster(card))
	}
	card.Completed = terminal(status)
	for _, stage := range status.StageViews {
		if stage.Status != afmsdk.StageDone && stage.Status != afmsdk.StageFailed && stage.Status != afmsdk.StageHookFailed {
			card.StageID = stage.ID
			break
		}
	}
	if !status.Capabilities.Collaboration {
		card.State = DataError
		card.Error = "Agent Mesh недоступен"
		return s.sendView(ctx, user, RenderRoster(card))
	}
	people, err := run.Participants(ctx)
	if err != nil {
		card.State = DataError
		card.Error = err.Error()
	} else {
		for _, p := range people {
			if p.ID == "user" {
				continue
			}
			running := p.Activity == "running"
			card.Agents = append(card.Agents, AgentCard{ID: p.ID, Alias: p.Alias, Name: p.DisplayName, Scope: p.Scope, StageID: p.StageID, Status: p.Activity, Reachable: p.Reachable, Running: running, CanInterrupt: p.Kind == "stage_agent" && running && p.Reachable && !terminal(status)})
		}
	}
	if card.State == DataReady {
		if err := s.Store.Update(func(st *State) error { st.RosterCache[key] = append([]AgentCard(nil), card.Agents...); return nil }); err != nil {
			return err
		}
	}
	return s.sendView(ctx, user, RenderRoster(card))
}
func (s *Service) showThread(ctx context.Context, user int64, key RunKey, threadID string, cursor uint64) error {
	b, ok := s.Store.Run(key)
	if !ok {
		return s.showRuns(ctx, user, "")
	}
	card := ThreadCard{State: DataReady, RunKey: key, RunLabel: b.Label, ThreadID: threadID, Filter: s.user(user).Filter, Completed: b.Completed}
	run, status, err := s.Registry.Resolve(ctx, key, false)
	if err != nil {
		card.State = DataOffline
		card.Messages = s.Store.Snapshot().ThreadCache[threadCacheKey(key, threadID)]
		card.Error = err.Error()
		return s.sendView(ctx, user, RenderThread(card))
	}
	card.Completed = terminal(status)
	page, err := run.Thread(ctx, threadID, cursor, 50)
	if err != nil {
		card.State = DataError
		card.Error = err.Error()
	} else {
		card.NextCursor = page.NextCursor
		card.HasMore = len(page.Messages) == 50
		viewKey := fmt.Sprintf("view:%d:%s:%s", user, key, threadID)
		for _, m := range page.Messages {
			msg := ThreadMessage{ID: m.ID, SenderID: m.SenderID, SenderName: m.SenderID, Text: m.Body, InReplyTo: m.InReplyTo, MessageKind: m.Kind, SentAt: m.CreatedAt, IsResult: m.AutoResult}
			card.Messages = append(card.Messages, msg)
			if err := s.cacheThreadMessage(key, threadID, msg); err != nil {
				return err
			}
		}
		if err := s.sendView(ctx, user, RenderThread(card)); err != nil {
			return err
		}
		return s.Store.Update(func(st *State) error { st.Cursors[viewKey] = page.NextCursor; return nil })
	}
	return s.sendView(ctx, user, RenderThread(card))
}
func (s *Service) showComposer(ctx context.Context, user int64, comp ComposerContext) error {
	card := ComposerCard{State: DataReady, RunKey: comp.RunKey, Mode: comp.Mode, StageID: comp.StageID, ThreadID: comp.ThreadID, InReplyTo: comp.InReplyTo, MessageKind: comp.MessageKind, Interrupt: comp.Interrupt, RecipientIDs: comp.RecipientIDs, Draft: comp.Draft, CanSend: true, CanInterrupt: comp.Mode == "message" && s.canInterrupt(ctx, comp.RunKey, comp.StageID)}
	if b, ok := s.Store.Run(comp.RunKey); ok {
		card.RunLabel = b.Label
	}
	if comp.Mode == "question" {
		cached := s.Store.Snapshot().QuestionCache[questionEntryCacheKey(comp.RunKey, comp.StageID, comp.QuestionPhase, comp.QuestionID)]
		if cached.QuestionID == comp.QuestionID && cached.QuestionPhase == comp.QuestionPhase && cached.QuestionDigest == comp.QuestionDigest {
			card.QuestionID = cached.QuestionID
			card.QuestionPhase = cached.QuestionPhase
			card.QuestionDigest = cached.QuestionDigest
			card.QuestionPrompt = cached.Prompt
			card.StageName = cached.StageName
		}
	}
	if comp.InReplyTo != "" {
		for _, m := range s.Store.Snapshot().ThreadCache[threadCacheKey(comp.RunKey, comp.ThreadID)] {
			if m.ID == comp.InReplyTo {
				card.ReplyAuthor = m.SenderName
				if card.ReplyAuthor == "" {
					card.ReplyAuthor = m.SenderID
				}
				card.ReplyText = m.Text
				break
			}
		}
	}
	return s.sendView(ctx, user, RenderComposer(card))
}
func (s *Service) canInterrupt(ctx context.Context, key RunKey, stageID string) bool {
	if stageID == "" {
		return false
	}
	run, status, err := s.Registry.Resolve(ctx, key, false)
	if err != nil || terminal(status) || !status.Capabilities.Collaboration {
		return false
	}
	stage, ok := firstStage(status, stageID)
	if !ok || stage.Status != afmsdk.StageRunning {
		return false
	}
	people, err := run.Participants(ctx)
	if err != nil {
		return false
	}
	for _, p := range people {
		if p.Kind == "stage_agent" && p.StageID == stageID && p.Reachable && p.Activity == "running" {
			return true
		}
	}
	return false
}
func questionDigest(q afmsdk.DialogEntry) string {
	b, _ := json.Marshal(struct {
		Question    string   `json:"question"`
		Options     []string `json:"options"`
		AllowCustom bool     `json:"allow_custom"`
	}{q.Question, q.Options, q.AllowCustom})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func composerDigest(instruction, firstMessage string) string {
	b, _ := json.Marshal([]string{instruction, firstMessage})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func planDigest(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func pendingQuestions(entries []afmsdk.DialogEntry) []afmsdk.DialogEntry {
	var pending []afmsdk.DialogEntry
	for _, entry := range entries {
		if entry.Question != "" && entry.Answer == nil {
			pending = append(pending, entry)
		}
	}
	return pending
}
func currentQuestion(entries []afmsdk.DialogEntry) (afmsdk.DialogEntry, bool) {
	pending := pendingQuestions(entries)
	if len(pending) > 0 {
		return pending[len(pending)-1], true
	}
	return afmsdk.DialogEntry{}, false
}
func (s *Service) showQuestion(ctx context.Context, user int64, key RunKey, stageID string) error {
	return s.showQuestionTarget(ctx, user, key, stageID, "", "")
}
func (s *Service) showQuestionTarget(ctx context.Context, user int64, key RunKey, stageID, questionID, phase string) error {
	b, ok := s.Store.Run(key)
	if !ok {
		return s.showRuns(ctx, user, "")
	}
	card := QuestionCard{State: DataReady, RunKey: key, RunLabel: b.Label, StageID: stageID}
	run, status, err := s.Registry.Resolve(ctx, key, false)
	if err != nil {
		cacheKey := questionCacheKey(key, stageID)
		if questionID != "" {
			cacheKey = questionEntryCacheKey(key, stageID, phase, questionID)
		}
		card = s.Store.Snapshot().QuestionCache[cacheKey]
		card.State = DataOffline
		card.RunKey = key
		card.RunLabel = b.Label
		card.StageID = stageID
		card.CanAnswer = false
		card.Error = err.Error()
		return s.sendView(ctx, user, RenderQuestion(card))
	}
	v, ok := firstStage(status, stageID)
	if !ok {
		card.State = DataError
		card.Error = "Стадия не найдена"
		return s.sendView(ctx, user, RenderQuestion(card))
	}
	card.StageID = v.ID
	card.StageName = v.Name
	card.StageUpdatedAt = v.UpdatedAt
	entries, err := run.Dialog(ctx, v.ID)
	if err != nil {
		card.State = DataError
		card.Error = err.Error()
	} else {
		var q afmsdk.DialogEntry
		var found bool
		for _, entry := range pendingQuestions(entries) {
			if questionID == "" || entry.ID == questionID && entry.Phase == phase {
				q, found = entry, true
			}
		}
		if found {
			card.Prompt = q.Question
			card.QuestionID = q.ID
			card.QuestionPhase = q.Phase
			card.QuestionDigest = questionDigest(q)
			card.AllowCustom = q.AllowCustom
			card.CanAnswer = !terminal(status)
			for _, o := range q.Options {
				card.Options = append(card.Options, QuestionOption{Label: o, Value: o})
			}
		} else {
			card.State = DataEmpty
		}
	}
	if card.State == DataReady {
		if err := s.Store.Update(func(st *State) error {
			st.QuestionCache[questionCacheKey(key, card.StageID)] = card
			st.QuestionCache[questionEntryCacheKey(key, card.StageID, card.QuestionPhase, card.QuestionID)] = card
			return nil
		}); err != nil {
			return err
		}
	}
	return s.sendView(ctx, user, RenderQuestion(card))
}
func (s *Service) showPlan(ctx context.Context, user int64, key RunKey, stageID string, draft string, confirm bool) error {
	b, ok := s.Store.Run(key)
	if !ok {
		return s.showRuns(ctx, user, "")
	}
	card := PlanCard{State: DataReady, RunKey: key, RunLabel: b.Label, StageID: stageID, RevisionDraft: draft, RevisionNeedsConfirmation: confirm}
	run, status, err := s.Registry.Resolve(ctx, key, false)
	if err != nil {
		card.State = DataOffline
		card.Plan = s.Store.Snapshot().PlanCache[planCacheKey(key, stageID)]
		card.CanApprove = false
		card.CanRevise = false
		card.CanPause = false
		card.CanContinue = false
		card.Error = err.Error()
		return s.sendView(ctx, user, RenderPlan(card))
	}
	v, ok := firstStage(status, stageID)
	if !ok {
		card.State = DataError
		card.Error = "Стадия не найдена"
		return s.sendView(ctx, user, RenderPlan(card))
	}
	card.StageID = v.ID
	card.StageName = v.Name
	card.ExpectedStatus = string(v.Status)
	card.StageUpdatedAt = v.UpdatedAt
	source, err := run.Plan(ctx, v.ID)
	if err != nil {
		var h *afmsdk.HTTPError
		if !errors.As(err, &h) || h.StatusCode != 404 {
			card.State = DataError
			card.Error = err.Error()
		}
	} else {
		card.Plan = string(source)
		card.PlanDigest = planDigest(source)
		if err := s.Store.Update(func(st *State) error {
			k := planCacheKey(key, card.StageID)
			st.PlanCache[k] = card.Plan
			st.PlanUpdated[k] = card.StageUpdatedAt
			return nil
		}); err != nil {
			return err
		}
	}
	if card.State == DataReady && !terminal(status) {
		card.CanApprove = v.Status == afmsdk.StageAwaitingApproval && card.PlanDigest != ""
		card.CanRevise = card.CanApprove
		card.CanPause = (v.Status == afmsdk.StageRunning && !v.IsScript) || v.Status == afmsdk.StagePlanning || v.Status == afmsdk.StageRevising || v.Status == afmsdk.StageRetrying || v.Status == afmsdk.StageAwaitingAssistants || v.Status == afmsdk.StageAwaitingPeer
		card.CanContinue = v.Status == afmsdk.StagePaused
	}
	return s.sendView(ctx, user, RenderPlan(card))
}
func (s *Service) showInbox(ctx context.Context, user int64) error {
	st := s.Store.Snapshot()
	filter := s.user(user).Filter
	if filter == "" {
		filter = "important"
	}
	card := InboxCard{State: DataReady, Filter: filter}
	readFailed := false
	for key, binding := range st.Runs {
		if binding.Invalidated {
			continue
		}
		run, status, err := s.Registry.Resolve(ctx, key, false)
		if err != nil {
			card.Items = append(card.Items, InboxItem{RunKey: key, RunLabel: binding.Label, Kind: "run", Summary: "Нет связи с AFM · состояние запуска не проверено", Actionable: true})
			continue
		}
		for _, stage := range status.StageViews {
			if stage.Status == afmsdk.StageFailed || stage.Status == afmsdk.StageHookFailed {
				card.Items = append(card.Items, InboxItem{RunKey: key, RunLabel: binding.Label, Kind: "run", Summary: "Ошибка стадии: " + stage.Name + " · откройте статус", UpdatedAt: stage.UpdatedAt, Actionable: true})
			}
			if stage.ShowDialog && !terminal(status) {
				entries, err := run.Dialog(ctx, stage.ID)
				if err == nil {
					for _, q := range pendingQuestions(entries) {
						card.Items = append(card.Items, InboxItem{RunKey: key, RunLabel: binding.Label, Kind: "question", StageID: stage.ID, StageName: stage.Name, QuestionID: q.ID, QuestionPhase: q.Phase, QuestionDigest: questionDigest(q), Summary: q.Question, UpdatedAt: stage.UpdatedAt, Actionable: true})
					}
				} else {
					readFailed = true
				}
			}
			if stage.Status == afmsdk.StageAwaitingApproval && !terminal(status) {
				card.Items = append(card.Items, InboxItem{RunKey: key, RunLabel: binding.Label, Kind: "plan", StageID: stage.ID, StageName: stage.Name, Summary: "План ждёт решения", UpdatedAt: stage.UpdatedAt, Actionable: true})
			}
		}
		if filter == "all" && status.Capabilities.Collaboration {
			threads, err := run.Threads(ctx)
			if err == nil {
				for _, thread := range threads {
					summary := thread.Title
					if summary == "" {
						summary = thread.ID
					}
					card.Items = append(card.Items, InboxItem{RunKey: key, RunLabel: binding.Label, Kind: "thread", ThreadID: thread.ID, Summary: summary})
				}
			} else {
				readFailed = true
			}
		}
	}
	for _, out := range st.Outbox {
		if out.Status != ReceiptFailed {
			continue
		}
		binding := st.Runs[out.RunKey]
		card.Items = append(card.Items, InboxItem{RunKey: out.RunKey, RunLabel: binding.Label, Kind: "run", Summary: "Не доставлено: " + out.FailureReason + " · текст: " + out.Text, Actionable: true})
	}
	if readFailed {
		card.State = DataError
		card.Error = "Не удалось проверить вопросы или беседы · повторите /inbox"
	} else if len(card.Items) == 0 {
		card.State = DataEmpty
	}
	sort.Slice(card.Items, func(i, j int) bool { return card.Items[i].UpdatedAt.After(card.Items[j].UpdatedAt) })
	return s.sendView(ctx, user, RenderInbox(card))
}
func (s *Service) handleCallback(ctx context.Context, c *TelegramCallback) error {
	if len(c.Data) > CallbackDataLimit {
		return s.sendText(ctx, c.From.ID, "Кнопка недействительна")
	}
	st := s.Store.Snapshot()
	record, ok := st.Callbacks[c.Data]
	if !ok || record.UserID != c.From.ID || record.Consumed {
		return s.sendText(ctx, c.From.ID, "Кнопка устарела. Обновите карточку")
	}
	if err := s.Store.Update(func(st *State) error {
		r := st.Callbacks[c.Data]
		if r.Consumed {
			return errors.New("consumed callback")
		}
		r.Consumed = true
		r.Result = "in_flight"
		st.Callbacks[c.Data] = r
		return nil
	}); err != nil {
		return err
	}
	err := s.HandleIntent(ctx, c.From.ID, record.Intent)
	outcome := "handled"
	if err != nil {
		outcome = "error"
	}
	_ = s.Store.Update(func(st *State) error {
		r := st.Callbacks[c.Data]
		r.Result = outcome
		st.Callbacks[c.Data] = r
		return nil
	})
	return err
}
func (s *Service) switchRun(ctx context.Context, user int64, key RunKey) error {
	if key == "" || key == s.selected(user) {
		return nil
	}
	binding, ok := s.Store.Run(key)
	if !ok || binding.Invalidated {
		return ErrStaleRun
	}
	old := s.user(user).Composer
	if err := s.setUser(user, func(u *UserSession) { u.Selected = key; u.Composer = nil; u.Recipients = nil }); err != nil {
		return err
	}
	if old != nil {
		return s.sendText(ctx, user, composerDiscardNotice(old))
	}
	return nil
}
func (s *Service) HandleIntent(ctx context.Context, user int64, in Intent) error {
	switch in.Kind {
	case IntentHome:
		return s.showHome(ctx, user, s.selected(user))
	case IntentRuns:
		return s.showRuns(ctx, user, "")
	case IntentSelectRun:
		if b, ok := s.Store.Run(in.RunKey); !ok || b.Invalidated {
			return s.sendText(ctx, user, "Запуск заменён или не найден. Подключите новый запуск через /connect PORT")
		}
		old := s.user(user)
		if err := s.setUser(user, func(u *UserSession) { u.Selected = in.RunKey; u.Recipients = nil; u.Composer = nil }); err != nil {
			return err
		}
		if old.Composer != nil {
			if err := s.sendText(ctx, user, composerDiscardNotice(old.Composer)); err != nil {
				return err
			}
		}
		return s.showHome(ctx, user, in.RunKey)
	case IntentInbox, IntentFilter:
		if in.Filter != "" {
			if err := s.setUser(user, func(u *UserSession) { u.Filter = in.Filter }); err != nil {
				return err
			}
		}
		return s.showInbox(ctx, user)
	case IntentRoster:
		if err := s.switchRun(ctx, user, in.RunKey); err != nil {
			return err
		}
		return s.showRoster(ctx, user, in.RunKey)
	case IntentThread:
		if err := s.switchRun(ctx, user, in.RunKey); err != nil {
			return err
		}
		return s.showThread(ctx, user, in.RunKey, in.ThreadID, 0)
	case IntentLoadMore:
		viewKey := fmt.Sprintf("view:%d:%s:%s", user, in.RunKey, in.ThreadID)
		return s.showThread(ctx, user, in.RunKey, in.ThreadID, s.Store.Snapshot().Cursors[viewKey])
	case IntentPlan:
		if err := s.switchRun(ctx, user, in.RunKey); err != nil {
			return err
		}
		return s.showPlan(ctx, user, in.RunKey, in.StageID, "", false)
	case IntentRefresh:
		if in.RunKey != "" {
			if err := s.switchRun(ctx, user, in.RunKey); err != nil {
				return err
			}
		}
		if in.StageID != "" {
			return s.showQuestionTarget(ctx, user, in.RunKey, in.StageID, in.QuestionID, in.QuestionPhase)
		}
		return s.showHome(ctx, user, s.selected(user))
	case IntentToggleRecipient:
		if in.RunKey != s.selected(user) {
			return s.sendText(ctx, user, "Выберите запуск заново")
		}
		if err := s.setUser(user, func(u *UserSession) {
			if len(in.RecipientIDs) == 0 {
				return
			}
			id := in.RecipientIDs[0]
			next := []string{}
			found := false
			for _, v := range u.Recipients {
				if v == id {
					found = true
				} else {
					next = append(next, v)
				}
			}
			if !found {
				next = append(next, id)
			}
			u.Recipients = next
		}); err != nil {
			return err
		}
		return s.showRoster(ctx, user, in.RunKey)
	case IntentCompose, IntentReply, IntentInterrupt:
		if in.RunKey != s.selected(user) {
			return s.sendText(ctx, user, "Контекст запуска изменился. Выберите действие заново")
		}
		if in.Kind == IntentInterrupt && in.Mode != "off" && !s.canInterrupt(ctx, in.RunKey, in.StageID) {
			return s.sendText(ctx, user, "Прерывание недоступно: агент стадии сейчас не работает")
		}
		previous := s.user(user).Composer
		if in.Kind == IntentInterrupt && previous != nil && previous.Mode == "message" && previous.RunKey == in.RunKey && previous.StageID == in.StageID && previous.ThreadID == in.ThreadID && previous.InReplyTo == in.InReplyTo {
			copy := *previous
			copy.Interrupt = in.Mode != "off"
			if err := s.setUser(user, func(u *UserSession) { u.Composer = &copy }); err != nil {
				return err
			}
			return s.showComposer(ctx, user, copy)
		}
		recipients := in.RecipientIDs
		if len(recipients) == 0 {
			recipients = s.user(user).Recipients
		}
		mode := "message"
		kind := "message"
		if in.Kind == IntentReply {
			kind = "reply"
		}
		comp := &ComposerContext{Mode: mode, RunKey: in.RunKey, StageID: in.StageID, ThreadID: in.ThreadID, InReplyTo: in.InReplyTo, MessageKind: kind, Interrupt: in.Kind == IntentInterrupt, RecipientIDs: recipients}
		if err := s.replaceComposer(ctx, user, comp); err != nil {
			return err
		}
		return s.showComposer(ctx, user, *comp)
	case IntentCancel:
		if err := s.setUser(user, func(u *UserSession) { u.Composer = nil }); err != nil {
			return err
		}
		return s.sendText(ctx, user, "Действие отменено")
	case IntentAnswerQuestion, IntentCustomAnswer, IntentApprovePlan, IntentRevisePlan, IntentConfirmRevision, IntentPause, IntentContinue, IntentAddAgent, IntentSelectProfile, IntentSelectScope, IntentSelectMode, IntentConfirmAddAgent:
		return s.handleActionIntent(ctx, user, in)
	case IntentSettings:
		return s.command(ctx, user, "/settings")
	default:
		return s.sendText(ctx, user, "Неизвестное действие")
	}
}
func (s *Service) handleText(ctx context.Context, user int64, text string) error {
	comp := s.user(user).Composer
	if comp == nil {
		return s.sendText(ctx, user, "Выберите действие в меню или отправьте /help")
	}
	if comp.RunKey != s.selected(user) {
		if err := s.setUser(user, func(u *UserSession) { u.Composer = nil }); err != nil {
			return err
		}
		return s.sendText(ctx, user, "Контекст запуска изменился. Черновик сохраните для копирования: "+text)
	}
	return s.handleComposerText(ctx, user, *comp, text)
}
func (s *Service) PollOnce(ctx context.Context) error {
	offset := s.Store.Snapshot().UpdateOffset
	updates, err := s.Telegram.GetUpdates(ctx, offset)
	if err != nil {
		return err
	}
	for _, u := range updates {
		if u.UpdateID < offset {
			continue
		}
		if err := s.HandleUpdate(ctx, u); err != nil {
			return err
		}
		next := u.UpdateID + 1
		if err := s.Store.Update(func(st *State) error {
			if next > st.UpdateOffset {
				st.UpdateOffset = next
			}
			for id := range st.ProcessedText {
				if id < st.UpdateOffset {
					delete(st.ProcessedText, id)
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}
func parseUserIDs(raw string) (map[int64]bool, error) { return ParseAllowedUsers(raw) }
func userIDString(id int64) string                    { return strconv.FormatInt(id, 10) }

func threadCacheKey(key RunKey, threadID string) string  { return string(key) + ":" + threadID }
func planCacheKey(key RunKey, stageID string) string     { return string(key) + ":" + stageID }
func questionCacheKey(key RunKey, stageID string) string { return string(key) + ":" + stageID }
func questionEntryCacheKey(key RunKey, stageID, phase, id string) string {
	return string(key) + ":" + stageID + ":" + phase + ":" + id
}
func (s *Service) cacheThreadMessage(key RunKey, threadID string, msg ThreadMessage) error {
	return s.Store.Update(func(st *State) error {
		k := threadCacheKey(key, threadID)
		for _, existing := range st.ThreadCache[k] {
			if existing.ID == msg.ID {
				return nil
			}
		}
		messages := append(st.ThreadCache[k], msg)
		if len(messages) > 500 {
			messages = messages[len(messages)-500:]
		}
		st.ThreadCache[k] = messages
		return nil
	})
}
