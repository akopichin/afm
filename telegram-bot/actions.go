package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	afmsdk "github.com/akopichin/afm/sdk"
)

var errStaleContent = errors.New("AFM content changed")

func (s *Service) beginPendingAction(ctx context.Context, action PendingAction) (string, error) {
	id, err := randomID()
	if err != nil {
		return "", err
	}
	action.ID = id
	err = s.Store.Update(func(st *State) error {
		st.PendingActions[id] = action
		markTextProcessed(st, ctx)
		if action.Kind == IntentAnswerQuestion || action.Kind == IntentCustomAnswer {
			u := st.Users[action.OwnerUserID]
			if u.Composer != nil {
				u.Composer.Draft = action.Answer
				st.Users[action.OwnerUserID] = u
			}
		}
		return nil
	})
	return id, err
}

func (s *Service) finishPendingAction(id string) error {
	return s.Store.Update(func(st *State) error { delete(st.PendingActions, id); return nil })
}

func (s *Service) handleActionIntent(ctx context.Context, user int64, in Intent) error {
	if in.RunKey == "" || in.RunKey != s.selected(user) {
		return s.sendText(ctx, user, "Контекст запуска изменился. Выберите действие заново")
	}
	switch in.Kind {
	case IntentAnswerQuestion:
		return s.answerQuestion(ctx, user, in, in.OptionValue, true)
	case IntentCustomAnswer:
		comp := &ComposerContext{Mode: "question", RunKey: in.RunKey, StageID: in.StageID, QuestionPhase: in.QuestionPhase, QuestionID: in.QuestionID, QuestionDigest: in.QuestionDigest, StageUpdatedAt: in.StageUpdatedAt}
		if err := s.replaceComposer(ctx, user, comp); err != nil {
			return err
		}
		return s.showComposer(ctx, user, *comp)
	case IntentApprovePlan, IntentConfirmRevision, IntentPause, IntentContinue:
		return s.stageAction(ctx, user, in)
	case IntentRevisePlan:
		comp := &ComposerContext{Mode: "revision", RunKey: in.RunKey, StageID: in.StageID, QuestionDigest: in.PlanDigest, StageUpdatedAt: in.StageUpdatedAt, MessageKind: in.ExpectedStatus}
		if err := s.replaceComposer(ctx, user, comp); err != nil {
			return err
		}
		return s.sendText(ctx, user, "Напишите правку плана. После текста потребуется отдельное подтверждение. /cancel — отмена")
	case IntentAddAgent, IntentSelectProfile, IntentSelectScope, IntentSelectMode:
		return s.selectAgent(ctx, user, in)
	case IntentConfirmAddAgent:
		return s.confirmAgent(ctx, user, in)
	default:
		return nil
	}
}
func (s *Service) preflightQuestion(ctx context.Context, in Intent) (*afmsdk.Run, afmsdk.DialogEntry, error) {
	run, status, err := s.Registry.Resolve(ctx, in.RunKey, true)
	if err != nil {
		return nil, afmsdk.DialogEntry{}, err
	}
	stage, ok := firstStage(status, in.StageID)
	if !ok || !stage.UpdatedAt.Equal(in.StageUpdatedAt) {
		return nil, afmsdk.DialogEntry{}, errStaleContent
	}
	entries, err := run.Dialog(ctx, in.StageID)
	if err != nil {
		return nil, afmsdk.DialogEntry{}, err
	}
	for _, q := range pendingQuestions(entries) {
		if q.ID == in.QuestionID && q.Phase == in.QuestionPhase && questionDigest(q) == in.QuestionDigest {
			return run, q, nil
		}
	}
	return nil, afmsdk.DialogEntry{}, errStaleContent
}
func (s *Service) answerQuestion(ctx context.Context, user int64, in Intent, answer string, fromOptions bool) error {
	run, q, err := s.preflightQuestion(ctx, in)
	if err != nil {
		if errors.Is(err, errStaleContent) || errors.Is(err, ErrStaleRun) {
			return s.staleQuestion(ctx, user, in, answer)
		}
		if err := s.setUser(user, func(u *UserSession) {
			if u.Composer != nil {
				u.Composer.Draft = answer
			}
		}); err != nil {
			return err
		}
		return s.sendText(ctx, user, "Нет связи с AFM. Действие не отправлено; черновик сохранён: "+answer)
	}
	if fromOptions {
		matched := false
		for _, option := range q.Options {
			if option == answer {
				matched = true
				break
			}
		}
		if !matched {
			return s.staleQuestion(ctx, user, in, answer)
		}
	} else if !q.AllowCustom {
		return s.staleQuestion(ctx, user, in, answer)
	}
	pendingID, err := s.beginPendingAction(ctx, PendingAction{OwnerUserID: user, RunKey: in.RunKey, StageID: in.StageID, Kind: in.Kind, QuestionID: q.ID, QuestionPhase: q.Phase, QuestionDigest: questionDigest(q), Answer: answer})
	if err != nil {
		return err
	}
	err = run.Answer(ctx, in.StageID, afmsdk.DialogAnswer{ID: q.ID, Phase: q.Phase, Answer: answer, FromOptions: fromOptions})
	if err != nil {
		var h *afmsdk.HTTPError
		if errors.As(err, &h) && h.StatusCode == http.StatusConflict {
			if err := s.finishPendingAction(pendingID); err != nil {
				return err
			}
			return s.staleQuestion(ctx, user, in, answer)
		}
		if errors.As(err, &h) && h.StatusCode >= 400 && h.StatusCode < 500 {
			if err := s.finishPendingAction(pendingID); err != nil {
				return err
			}
			return s.sendText(ctx, user, "AFM отклонил ответ. Черновик сохранён: "+answer)
		}
		if err := s.setUser(user, func(u *UserSession) {
			if u.Composer != nil {
				u.Composer.Draft = answer
			}
		}); err != nil {
			return err
		}
		return s.sendText(ctx, user, "Результат неизвестен · проверяю AFM. Текст сохранён: "+answer)
	}
	if err := s.finishPendingAction(pendingID); err != nil {
		return err
	}
	if err := s.setUser(user, func(u *UserSession) { u.Composer = nil }); err != nil {
		return err
	}
	return s.sendText(ctx, user, "Ответ принят AFM")
}
func (s *Service) staleQuestion(ctx context.Context, user int64, in Intent, text string) error {
	if err := s.setUser(user, func(u *UserSession) {
		if u.Composer != nil {
			u.Composer.Draft = text
		}
	}); err != nil {
		return err
	}
	if err := s.sendText(ctx, user, "Вопрос изменился. Черновик сохранён: "+text); err != nil {
		return err
	}
	return s.showQuestionTarget(ctx, user, in.RunKey, in.StageID, in.QuestionID, in.QuestionPhase)
}
func (s *Service) preflightPlan(ctx context.Context, in Intent) (*afmsdk.Run, afmsdk.StageView, error) {
	run, status, err := s.Registry.Resolve(ctx, in.RunKey, true)
	if err != nil {
		return nil, afmsdk.StageView{}, err
	}
	stage, ok := firstStage(status, in.StageID)
	if !ok || string(stage.Status) != in.ExpectedStatus || !stage.UpdatedAt.Equal(in.StageUpdatedAt) {
		return nil, stage, errStaleContent
	}
	if in.Kind == IntentPause || in.Kind == IntentContinue {
		return run, stage, nil
	}
	source, err := run.Plan(ctx, in.StageID)
	if err != nil {
		return nil, stage, err
	}
	if planDigest(source) != in.PlanDigest {
		return nil, stage, errStaleContent
	}
	return run, stage, nil
}
func (s *Service) stageAction(ctx context.Context, user int64, in Intent) error {
	run, stage, err := s.preflightPlan(ctx, in)
	if err != nil {
		if errors.Is(err, errStaleContent) || errors.Is(err, ErrStaleRun) {
			if err := s.sendText(ctx, user, "Карточка плана устарела. Обновляю план"); err != nil {
				return err
			}
			return s.showPlan(ctx, user, in.RunKey, in.StageID, "", false)
		}
		return s.sendText(ctx, user, "Нет связи с AFM. Действие не отправлено; черновик сохранён")
	}
	var verb string
	var post func() error
	switch in.Kind {
	case IntentApprovePlan:
		if stage.Status != afmsdk.StageAwaitingApproval {
			return s.sendText(ctx, user, "Одобрение сейчас недоступно")
		}
		verb = "Одобрение"
		post = func() error { return run.Approve(ctx, in.StageID) }
	case IntentConfirmRevision:
		comp := s.user(user).Composer
		if comp == nil || comp.Mode != "revision_confirm" || comp.RunKey != in.RunKey || comp.StageID != in.StageID || comp.QuestionDigest != in.PlanDigest || !comp.StageUpdatedAt.Equal(in.StageUpdatedAt) || comp.Draft == "" {
			return s.sendText(ctx, user, "Черновик правки устарел")
		}
		if stage.Status != afmsdk.StageAwaitingApproval {
			return s.sendText(ctx, user, "Правка сейчас недоступна")
		}
		verb = "Правка"
		post = func() error { return run.Revise(ctx, in.StageID, comp.Draft) }
	case IntentPause:
		if stage.IsScript && stage.Status == afmsdk.StageRunning {
			return s.sendText(ctx, user, "Пауза сейчас недоступна")
		}
		switch stage.Status {
		case afmsdk.StageRunning, afmsdk.StagePlanning, afmsdk.StageRevising, afmsdk.StageRetrying, afmsdk.StageAwaitingAssistants, afmsdk.StageAwaitingPeer:
		default:
			return s.sendText(ctx, user, "Пауза сейчас недоступна")
		}
		verb = "Пауза"
		post = func() error { return run.Pause(ctx, in.StageID) }
	case IntentContinue:
		if stage.Status != afmsdk.StagePaused {
			return s.sendText(ctx, user, "Продолжение сейчас недоступно")
		}
		verb = "Продолжение"
		post = func() error { return run.Continue(ctx, in.StageID) }
	}
	pendingID, err := s.beginPendingAction(ctx, PendingAction{OwnerUserID: user, RunKey: in.RunKey, StageID: in.StageID, Kind: in.Kind, ExpectedStatus: in.ExpectedStatus})
	if err != nil {
		return err
	}
	err = post()
	if err != nil {
		var h *afmsdk.HTTPError
		if errors.As(err, &h) && h.StatusCode >= 400 && h.StatusCode < 500 {
			if err := s.finishPendingAction(pendingID); err != nil {
				return err
			}
			return s.sendText(ctx, user, "AFM отклонил действие. Обновите карточку")
		}
		return s.sendText(ctx, user, "Результат неизвестен · проверяю AFM. Не повторяйте действие автоматически")
	}
	if err := s.finishPendingAction(pendingID); err != nil {
		return err
	}
	if err := s.setUser(user, func(u *UserSession) { u.Composer = nil }); err != nil {
		return err
	}
	return s.sendText(ctx, user, verb+" принято AFM")
}
func (s *Service) handleComposerText(ctx context.Context, user int64, comp ComposerContext, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return s.sendText(ctx, user, "Пустой текст не отправлен")
	}
	if err := s.setUserForText(ctx, user, func(u *UserSession) {
		if u.Composer != nil {
			u.Composer.Draft = text
		}
	}); err != nil {
		return err
	}
	switch comp.Mode {
	case "question":
		in := Intent{Kind: IntentCustomAnswer, RunKey: comp.RunKey, StageID: comp.StageID, QuestionID: comp.QuestionID, QuestionPhase: comp.QuestionPhase, QuestionDigest: comp.QuestionDigest, StageUpdatedAt: comp.StageUpdatedAt}
		return s.answerQuestion(ctx, user, in, text, false)
	case "revision":
		if err := s.setUserForText(ctx, user, func(u *UserSession) { u.Composer.Mode = "revision_confirm"; u.Composer.Draft = text }); err != nil {
			return err
		}
		return s.showPlan(ctx, user, comp.RunKey, comp.StageID, text, true)
	case "revision_confirm":
		if err := s.setUserForText(ctx, user, func(u *UserSession) { u.Composer.Draft = text }); err != nil {
			return err
		}
		return s.showPlan(ctx, user, comp.RunKey, comp.StageID, text, true)
	case "agent_instruction":
		if err := s.setUserForText(ctx, user, func(u *UserSession) { u.Composer.Instruction = text; u.Composer.Mode = "agent_ready" }); err != nil {
			return err
		}
		return s.showAddAgent(ctx, user, comp.RunKey, comp.StageID)
	case "agent_ready":
		if err := s.setUserForText(ctx, user, func(u *UserSession) { u.Composer.FirstMessage = text }); err != nil {
			return err
		}
		return s.showAddAgent(ctx, user, comp.RunKey, comp.StageID)
	case "message":
		return s.queueMessage(ctx, user, comp, text)
	default:
		return s.sendText(ctx, user, "Контекст ввода устарел")
	}
}
func (s *Service) selectAgent(ctx context.Context, user int64, in Intent) error {
	comp := s.user(user).Composer
	newComposer := false
	if comp == nil || comp.Mode != "agent_instruction" && comp.Mode != "agent_ready" || comp.RunKey != in.RunKey || comp.StageID != in.StageID {
		comp = &ComposerContext{Mode: "agent_instruction", RunKey: in.RunKey, StageID: in.StageID, Scope: "run"}
		newComposer = true
	}
	if in.ProfileID != "" {
		comp.ProfileID = in.ProfileID
	}
	if in.Scope != "" {
		comp.Scope = in.Scope
	}
	if in.Mode != "" {
		comp.HelperMode = in.Mode
	}
	if comp.Scope == "run" {
		comp.HelperMode = ""
	}
	if newComposer {
		if err := s.replaceComposer(ctx, user, comp); err != nil {
			return err
		}
	} else {
		if err := s.setUser(user, func(u *UserSession) { u.Composer = comp }); err != nil {
			return err
		}
	}
	return s.showAddAgent(ctx, user, in.RunKey, in.StageID)
}
func (s *Service) showAddAgent(ctx context.Context, user int64, key RunKey, stageID string) error {
	b, ok := s.Store.Run(key)
	if !ok {
		return s.showRuns(ctx, user, "")
	}
	comp := s.user(user).Composer
	card := AddAgentCard{State: DataReady, RunKey: key, RunLabel: b.Label, StageID: stageID, CanAdd: !b.Completed}
	if comp != nil {
		card.ProfileID = comp.ProfileID
		card.Scope = comp.Scope
		card.Mode = comp.HelperMode
		card.Instruction = comp.Instruction
		card.FirstMessage = comp.FirstMessage
	}
	run, status, err := s.Registry.Resolve(ctx, key, false)
	if err != nil {
		card.State = DataOffline
		card.Error = err.Error()
		card.CanAdd = false
		return s.sendView(ctx, user, RenderAddAgent(card))
	}
	card.CanAdd = !terminal(status) && status.Capabilities.Collaboration
	if stage, ok := firstStage(status, stageID); ok {
		card.StageName = stage.Name
	}
	profiles, err := run.AssistantProfiles(ctx)
	if err != nil {
		card.State = DataError
		card.Error = err.Error()
		card.CanAdd = false
	} else {
		for _, p := range profiles.Profiles {
			card.Profiles = append(card.Profiles, AgentProfile{ID: p.ID, Name: p.Title, Available: p.Available, Reason: p.UnavailableReason})
		}
	}
	if err := s.sendView(ctx, user, RenderAddAgent(card)); err != nil {
		return err
	}
	if card.CanAdd && card.Instruction == "" {
		return s.sendText(ctx, user, "Напишите задачу агента. После неё можно отправить необязательное первое сообщение отдельным текстом")
	}
	return nil
}
func (s *Service) confirmAgent(ctx context.Context, user int64, in Intent) error {
	comp := s.user(user).Composer
	if comp == nil || comp.Mode != "agent_ready" || comp.RunKey != in.RunKey || comp.StageID != in.StageID || comp.ProfileID != in.ProfileID || comp.Scope != in.Scope || comp.HelperMode != in.Mode || comp.Instruction == "" || composerDigest(comp.Instruction, comp.FirstMessage) != in.ComposerDigest {
		return s.sendText(ctx, user, "Выбор агента устарел")
	}
	run, status, err := s.Registry.Resolve(ctx, in.RunKey, true)
	if err != nil {
		return s.sendText(ctx, user, "AFM недоступен. Задача сохранена")
	}
	if !status.Capabilities.Collaboration {
		return s.sendText(ctx, user, "Добавление агента недоступно")
	}
	profiles, err := run.AssistantProfiles(ctx)
	if err != nil {
		return err
	}
	available := false
	for _, p := range profiles.Profiles {
		if p.ID == comp.ProfileID && p.Available {
			available = true
		}
	}
	if !available {
		return s.sendText(ctx, user, "Профиль больше недоступен")
	}
	id, err := randomID()
	if err != nil {
		return err
	}
	out := Outbound{ID: id, OwnerUserID: user, RunKey: comp.RunKey, Kind: "attach", RequestID: id, Text: comp.Instruction, StageID: comp.StageID, ProfileID: comp.ProfileID, Scope: comp.Scope, Mode: comp.HelperMode, Instruction: comp.Instruction, FirstMessage: comp.FirstMessage, Status: ReceiptQueued}
	if err := s.Store.Update(func(st *State) error {
		st.Outbox[id] = out
		u := st.Users[user]
		u.Composer = nil
		st.Users[user] = u
		markTextProcessed(st, ctx)
		return nil
	}); err != nil {
		return err
	}
	return s.deliverAndShow(ctx, user, id)
}
func (s *Service) queueMessage(ctx context.Context, user int64, comp ComposerContext, text string) error {
	if _, _, err := s.Registry.Resolve(ctx, comp.RunKey, true); err != nil && (errors.Is(err, ErrStaleRun) || errors.Is(err, ErrCompletedRun)) {
		return s.sendText(ctx, user, "Запуск изменился или завершён. Текст сохранён: "+text)
	}
	if comp.Interrupt && !s.canInterrupt(ctx, comp.RunKey, comp.StageID) {
		return s.sendText(ctx, user, "Прерывание недоступно: агент стадии сейчас не работает. Черновик сохранён: "+text)
	}
	recipients := append([]string(nil), comp.RecipientIDs...)
	if strings.Contains(text, "@") {
		run, _, err := s.Registry.Resolve(ctx, comp.RunKey, true)
		if err != nil {
			if err := s.setUser(user, func(u *UserSession) {
				if u.Composer != nil {
					u.Composer.Draft = text
				}
			}); err != nil {
				return err
			}
			return s.sendText(ctx, user, "Не удалось проверить @адресатов. Черновик сохранён: "+text)
		}
		people, err := run.Participants(ctx)
		if err != nil {
			return s.sendText(ctx, user, "Не удалось получить список агентов. Черновик сохранён: "+text)
		}
		aliases := map[string]string{}
		for _, p := range people {
			if p.Reachable && p.Alias != "" {
				aliases[p.Alias] = p.ID
			}
		}
		for _, word := range strings.Fields(text) {
			if !strings.HasPrefix(word, "@") {
				continue
			}
			alias := strings.Trim(word[1:], ",.:;!?()[]")
			if alias == "" {
				continue
			}
			id, ok := aliases[alias]
			if !ok {
				if err := s.setUser(user, func(u *UserSession) {
					if u.Composer != nil {
						u.Composer.Draft = text
					}
				}); err != nil {
					return err
				}
				return s.sendText(ctx, user, "Агент @"+alias+" недоступен. Черновик сохранён: "+text)
			}
			found := false
			for _, r := range recipients {
				if r == id {
					found = true
				}
			}
			if !found {
				recipients = append(recipients, id)
			}
		}
	}
	id, err := randomID()
	if err != nil {
		return err
	}
	kind := "mesh"
	if comp.StageID != "" && (comp.Interrupt || len(recipients) == 0) && comp.ThreadID == "" {
		kind = "discussion"
	}
	out := Outbound{ID: id, OwnerUserID: user, RunKey: comp.RunKey, Kind: kind, RequestID: id, Text: text, StageID: comp.StageID, ThreadID: comp.ThreadID, InReplyTo: comp.InReplyTo, MessageKind: comp.MessageKind, Interrupt: comp.Interrupt, RecipientIDs: recipients, Status: ReceiptQueued, RecipientStatus: map[string]ReceiptStatus{}}
	for _, recipient := range out.RecipientIDs {
		out.RecipientStatus[recipient] = ReceiptQueued
	}
	if err := s.Store.Update(func(st *State) error {
		st.Outbox[id] = out
		u := st.Users[user]
		u.Composer = nil
		st.Users[user] = u
		markTextProcessed(st, ctx)
		return nil
	}); err != nil {
		return err
	}
	return s.deliverAndShow(ctx, user, id)
}
func (s *Service) deliverAndShow(ctx context.Context, user int64, id string) error {
	if err := s.Deliver(ctx, id); err != nil {
		return err
	}
	out := s.Store.Snapshot().Outbox[id]
	return s.sendReceipt(ctx, user, out)
}
func (s *Service) sendReceipt(ctx context.Context, user int64, out Outbound) error {
	b, _ := s.Store.Run(out.RunKey)
	card := ReceiptCard{State: DataReady, RunKey: out.RunKey, RunLabel: b.Label, Text: out.Text, Status: out.Status, ThreadID: out.ThreadID, InReplyTo: out.InReplyTo, AFMMessageID: out.AFMMessageID, FailureReason: out.FailureReason}
	recipients := append([]string(nil), out.RecipientIDs...)
	seen := make(map[string]bool, len(recipients))
	for _, recipient := range recipients {
		seen[recipient] = true
	}
	for recipient := range out.RecipientStatus {
		if !seen[recipient] {
			recipients = append(recipients, recipient)
		}
	}
	sort.Strings(recipients)
	for _, recipient := range recipients {
		card.Recipients = append(card.Recipients, RecipientReceipt{ID: recipient, Name: recipient, Status: out.RecipientStatus[recipient], OutboundMessageID: out.AFMMessageID, FailureReason: out.FailureReason})
	}
	if err := s.sendView(ctx, user, RenderReceipt(card)); err != nil {
		return err
	}
	return s.setOutbound(out.ID, func(o *Outbound) { o.NotifiedState = receiptState(*o) })
}
func (s *Service) permanent(err error) bool {
	var h *afmsdk.HTTPError
	return errors.As(err, &h) && h.StatusCode >= 400 && h.StatusCode < 500 && h.StatusCode != http.StatusTooManyRequests
}
func (s *Service) setOutbound(id string, fn func(*Outbound)) error {
	return s.Store.Update(func(st *State) error {
		o, ok := st.Outbox[id]
		if !ok {
			return fmt.Errorf("outbound %s missing", id)
		}
		fn(&o)
		st.Outbox[id] = o
		return nil
	})
}
