package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	afmsdk "github.com/akopichin/afm/sdk"
)

func receiptState(o Outbound) string {
	data, _ := json.Marshal(struct {
		Status          ReceiptStatus
		RecipientStatus map[string]ReceiptStatus
		FailureReason   string
		AFMMessageID    string
		ThreadID        string
	}{o.Status, o.RecipientStatus, o.FailureReason, o.AFMMessageID, o.ThreadID})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func markOutboundFailed(o *Outbound, reason string) {
	o.Status = ReceiptFailed
	o.FailureReason = reason
	for _, recipient := range o.RecipientIDs {
		if o.RecipientStatus == nil {
			o.RecipientStatus = map[string]ReceiptStatus{}
		}
		o.RecipientStatus[recipient] = ReceiptFailed
	}
}

func (s *Service) notifyReceiptChanges(ctx context.Context) error {
	st := s.Store.Snapshot()
	ids := make([]string, 0, len(st.Outbox))
	for id := range st.Outbox {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var allErr error
	for _, id := range ids {
		o := st.Outbox[id]
		if o.OwnerUserID == 0 || !s.Allowed[o.OwnerUserID] || o.NotifiedState == receiptState(o) {
			continue
		}
		if err := s.sendReceipt(ctx, o.OwnerUserID, o); err != nil {
			allErr = errors.Join(allErr, fmt.Errorf("receipt %s: %w", id, err))
		}
	}
	return allErr
}

// Deliver retries only request-ID operations. The original run key and request ID
// remain fixed across outages, and a changed dashboard fingerprint fails the item.
func (s *Service) Deliver(ctx context.Context, id string) error {
	out, ok := s.Store.Snapshot().Outbox[id]
	if !ok {
		return fmt.Errorf("outbound %s missing", id)
	}
	if out.Status != ReceiptQueued && out.Status != ReceiptUnknown {
		return nil
	}
	run, status, err := s.Registry.Resolve(ctx, out.RunKey, false)
	if err != nil {
		if errors.Is(err, ErrStaleRun) {
			return s.setOutbound(id, func(o *Outbound) { markOutboundFailed(o, err.Error()) })
		}
		return nil
	}
	// A committed request can be replayed by request ID even after its target
	// becomes unreachable or the run finishes. Check eligibility only before
	// the first attempt; never retarget a replay.
	if !out.Attempted && terminal(status) {
		return s.setOutbound(id, func(o *Outbound) { markOutboundFailed(o, ErrCompletedRun.Error()) })
	}
	if !out.Attempted && (out.Kind == "mesh" || out.Kind == "attach") && !status.Capabilities.Collaboration {
		return s.setOutbound(id, func(o *Outbound) { markOutboundFailed(o, "Agent Mesh unavailable") })
	}
	if !out.Attempted && out.Kind == "mesh" && len(out.RecipientIDs) > 0 {
		people, e := run.Participants(ctx)
		if e != nil {
			return nil
		}
		reachable := map[string]bool{}
		for _, p := range people {
			if p.Reachable {
				reachable[p.ID] = true
			}
		}
		for _, recipient := range out.RecipientIDs {
			if !reachable[recipient] {
				return s.setOutbound(id, func(o *Outbound) { markOutboundFailed(o, "recipient unavailable: "+recipient) })
			}
		}
	}
	if !out.Attempted {
		if err := s.setOutbound(id, func(o *Outbound) { o.Attempted = true; o.Status = ReceiptUnknown }); err != nil {
			return err
		}
	}
	var receipt afmsdk.MessageReceipt
	attachRecipient := ""
	switch out.Kind {
	case "mesh":
		kind := out.MessageKind
		if kind == "" {
			kind = "message"
		}
		receipt, err = run.Send(ctx, afmsdk.MeshSendRequest{RequestID: out.RequestID, ThreadID: out.ThreadID, Recipients: out.RecipientIDs, Kind: kind, InReplyTo: out.InReplyTo, Body: out.Text, OriginStage: out.StageID})
	case "discussion":
		receipt, err = run.PostDiscussion(ctx, out.StageID, afmsdk.DiscussionPost{RequestID: out.RequestID, Body: out.Text, Mentions: out.RecipientIDs, Interrupt: out.Interrupt})
	case "attach":
		var first *string
		if out.FirstMessage != "" {
			first = &out.FirstMessage
		}
		stageID := out.StageID
		if out.Scope == "run" {
			stageID = ""
		}
		result, e := run.AttachAndSend(ctx, afmsdk.AttachAndSendRequest{RequestID: out.RequestID, ProfileID: out.ProfileID, Scope: out.Scope, StageID: stageID, Mode: out.Mode, Instruction: out.Instruction, FirstMessage: first, OriginStage: out.StageID})
		err = e
		attachRecipient = result.ParticipantID
		receipt.ThreadID = result.ThreadID
		if result.MessageID != nil {
			receipt.MessageID = *result.MessageID
		}
	default:
		return s.setOutbound(id, func(o *Outbound) { markOutboundFailed(o, "unknown outbound kind") })
	}
	if err != nil {
		if s.permanent(err) {
			return s.setOutbound(id, func(o *Outbound) { markOutboundFailed(o, err.Error()) })
		}
		return nil // unknown outcome; stable request ID permits safe replay
	}
	return s.setOutbound(id, func(o *Outbound) {
		o.Status = ReceiptAccepted
		o.AFMMessageID = receipt.MessageID
		if o.RecipientStatus == nil {
			o.RecipientStatus = map[string]ReceiptStatus{}
		}
		if receipt.ThreadID != "" {
			o.ThreadID = receipt.ThreadID
		}
		for _, p := range o.RecipientIDs {
			o.RecipientStatus[p] = ReceiptAccepted
		}
		if o.Kind == "discussion" && o.StageID != "" {
			o.RecipientStatus["stage:"+o.StageID] = ReceiptAccepted
		}
		if attachRecipient != "" && receipt.MessageID != "" {
			o.RecipientStatus[attachRecipient] = ReceiptAccepted
		}
	})
}
func (s *Service) SyncOnce(ctx context.Context) error {
	st := s.Store.Snapshot()
	ids := make([]string, 0, len(st.Outbox))
	for id, o := range st.Outbox {
		if o.Status == ReceiptQueued || o.Status == ReceiptUnknown {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	var allErr error
	for _, id := range ids {
		if err := s.Deliver(ctx, id); err != nil {
			allErr = errors.Join(allErr, fmt.Errorf("deliver %s: %w", id, err))
		}
	}
	keys := make([]string, 0, len(st.Runs))
	for key, b := range st.Runs {
		if !b.Invalidated {
			keys = append(keys, string(key))
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := s.SyncRun(ctx, RunKey(key)); err != nil {
			allErr = errors.Join(allErr, fmt.Errorf("sync run %s: %w", key, err))
		}
	}
	if err := s.reconcilePendingActions(ctx); err != nil {
		allErr = errors.Join(allErr, err)
	}
	if err := s.notifyReceiptChanges(ctx); err != nil {
		allErr = errors.Join(allErr, err)
	}
	return allErr
}
func (s *Service) reconcilePendingActions(ctx context.Context) error {
	st := s.Store.Snapshot()
	ids := make([]string, 0, len(st.PendingActions))
	for id := range st.PendingActions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var allErr error
	for _, id := range ids {
		a := st.PendingActions[id]
		if a.OwnerUserID == 0 || !s.Allowed[a.OwnerUserID] {
			continue
		}
		run, status, err := s.Registry.Resolve(ctx, a.RunKey, false)
		if err != nil && !errors.Is(err, ErrStaleRun) {
			continue
		}
		message := ""
		confirmedQuestion := false
		if errors.Is(err, ErrStaleRun) {
			message = "Результат действия не удалось проверить: запуск AFM заменён. Проверьте историю AFM"
		} else if a.Kind == IntentAnswerQuestion || a.Kind == IntentCustomAnswer {
			entries, e := run.Dialog(ctx, a.StageID)
			if e != nil {
				continue
			}
			// A reused ID may leave an older answered entry beside a new
			// unanswered question. Never attribute that old answer to this write.
			current, open := currentQuestion(entries)
			for i := len(entries) - 1; i >= 0; i-- {
				q := entries[i]
				if open && current.ID == a.QuestionID && current.Phase == a.QuestionPhase {
					break
				}
				if q.ID == a.QuestionID && q.Phase == a.QuestionPhase && questionDigest(q) == a.QuestionDigest && q.Answer != nil && *q.Answer == a.Answer {
					confirmedQuestion = true
					break
				}
			}
			if confirmedQuestion {
				message = "AFM показывает принятый ответ на вопрос " + a.QuestionID
			} else {
				message = "Не удалось подтвердить ответ на вопрос " + a.QuestionID + ". Проверьте AFM; автоматического повтора нет"
			}
		} else {
			stage, ok := firstStage(status, a.StageID)
			if !ok {
				message = "Не удалось подтвердить действие: стадия недоступна. Проверьте AFM; автоматического повтора нет"
			} else {
				message = "Результат действия уточнён: AFM показывает «" + stageStatusLabel(stage.Status) + "». Проверьте историю AFM; автоматического повтора нет"
			}
		}
		if err := s.sendText(ctx, a.OwnerUserID, message); err != nil {
			allErr = errors.Join(allErr, fmt.Errorf("pending action %s: %w", id, err))
			continue
		}
		if err := s.Store.Update(func(st *State) error {
			delete(st.PendingActions, id)
			if confirmedQuestion {
				u := st.Users[a.OwnerUserID]
				if u.Composer != nil && u.Composer.Mode == "question" && u.Composer.RunKey == a.RunKey && u.Composer.QuestionID == a.QuestionID && u.Composer.QuestionPhase == a.QuestionPhase {
					u.Composer = nil
					st.Users[a.OwnerUserID] = u
				}
			}
			return nil
		}); err != nil {
			allErr = errors.Join(allErr, err)
		}
	}
	return allErr
}
func (s *Service) SyncRun(ctx context.Context, key RunKey) error {
	run, status, err := s.Registry.Resolve(ctx, key, false)
	if err != nil {
		return nil
	}
	for _, stage := range status.StageViews {
		if stage.ShowPlan {
			cacheKey := planCacheKey(key, stage.ID)
			if !s.Store.Snapshot().PlanUpdated[cacheKey].Equal(stage.UpdatedAt) {
				source, e := run.Plan(ctx, stage.ID)
				if e == nil {
					if err := s.Store.Update(func(st *State) error {
						st.PlanCache[cacheKey] = string(source)
						st.PlanUpdated[cacheKey] = stage.UpdatedAt
						return nil
					}); err != nil {
						return err
					}
				}
			}
		}
		if stage.ShowDialog && !terminal(status) {
			entries, e := run.Dialog(ctx, stage.ID)
			if e == nil {
				for _, q := range pendingQuestions(entries) {
					marker := fmt.Sprintf("question:%s:%s:%s:%s:%s:%s", key, stage.ID, q.Phase, q.ID, stage.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), questionDigest(q))
					for user := range s.Allowed {
						seenKey := marker + ":" + userIDString(user)
						if s.Store.Snapshot().Seen[seenKey] {
							continue
						}
						if err := s.showQuestionTarget(ctx, user, key, stage.ID, q.ID, q.Phase); err != nil {
							return err
						}
						if err := s.Store.Update(func(st *State) error { st.Seen[seenKey] = true; return nil }); err != nil {
							return err
						}
					}
				}
			}
		}
		pageKey := fmt.Sprintf("discussion:%s:%s", key, stage.ID)
		if e := s.syncDiscussion(ctx, run, key, stage.ID, pageKey); e != nil {
			return e
		}
	}
	if !status.Capabilities.Collaboration {
		return nil
	}
	if people, err := run.Participants(ctx); err == nil {
		agents := make([]AgentCard, 0, len(people))
		for _, p := range people {
			if p.ID == "user" {
				continue
			}
			running := p.Activity == "running"
			agents = append(agents, AgentCard{ID: p.ID, Alias: p.Alias, Name: p.DisplayName, Scope: p.Scope, StageID: p.StageID, Status: p.Activity, Reachable: p.Reachable, Running: running, CanInterrupt: p.Kind == "stage_agent" && running && p.Reachable && !terminal(status)})
		}
		if err := s.Store.Update(func(st *State) error { st.RosterCache[key] = agents; return nil }); err != nil {
			return err
		}
	}
	if err := s.syncMeshPage(ctx, run, key, "inbox:"+string(key), func(cursor uint64) (afmsdk.MessagePage, error) { return run.Inbox(ctx, cursor, 100) }); err != nil {
		return err
	}
	threads, err := run.Threads(ctx)
	if err != nil {
		return err
	}
	for _, thread := range threads {
		threadID := thread.ID
		cursorKey := "thread:" + string(key) + ":" + threadID
		if err := s.syncMeshPage(ctx, run, key, cursorKey, func(cursor uint64) (afmsdk.MessagePage, error) { return run.Thread(ctx, threadID, cursor, 100) }); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) syncMeshPage(ctx context.Context, _ *afmsdk.Run, key RunKey, cursorKey string, read func(uint64) (afmsdk.MessagePage, error)) error {
	for pageNumber := 0; pageNumber < 100; pageNumber++ {
		cursor := s.Store.Snapshot().Cursors[cursorKey]
		page, err := read(cursor)
		if err != nil {
			return err
		}
		for _, m := range page.Messages {
			if err := s.processMeshMessage(ctx, key, m); err != nil {
				return err
			}
		}
		if page.NextCursor > cursor {
			if err := s.Store.Update(func(st *State) error { st.Cursors[cursorKey] = page.NextCursor; return nil }); err != nil {
				return err
			}
		}
		if len(page.Messages) < 100 || page.NextCursor <= cursor {
			return nil
		}
	}
	return errors.New("mesh page limit reached")
}
func (s *Service) processMeshMessage(ctx context.Context, key RunKey, m afmsdk.Message) error {
	seenKey := "mesh:" + string(key) + ":" + m.ID
	if s.Store.Snapshot().Seen[seenKey] {
		return nil
	}
	if err := s.Store.Update(func(st *State) error {
		for id, o := range st.Outbox {
			if o.RunKey != key || o.AFMMessageID == "" || o.AFMMessageID != m.InReplyTo || o.Status != ReceiptAccepted && o.Status != ReceiptReplied {
				continue
			}
			if _, ok := o.RecipientStatus[m.SenderID]; ok {
				o.RecipientStatus[m.SenderID] = ReceiptReplied
				all := true
				for _, status := range o.RecipientStatus {
					if status != ReceiptReplied {
						all = false
					}
				}
				if all {
					o.Status = ReceiptReplied
				}
				st.Outbox[id] = o
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if m.SenderID != "user" {
		for user := range s.Allowed {
			session := s.user(user)
			if session.Filter == "all" || contains(m.RecipientIDs, "user") || m.Kind == "reply" {
				if err := s.notifyMessage(ctx, key, m.ID, user, fmt.Sprintf("AFM · %s · %s\n%s", key, m.SenderID, m.Body)); err != nil {
					return err
				}
			}
		}
	}
	if err := s.cacheThreadMessage(key, m.ThreadID, ThreadMessage{ID: m.ID, SenderID: m.SenderID, SenderName: m.SenderID, Text: m.Body, InReplyTo: m.InReplyTo, MessageKind: m.Kind, SentAt: m.CreatedAt, IsResult: m.AutoResult}); err != nil {
		return err
	}
	return s.Store.Update(func(st *State) error { st.Seen[seenKey] = true; return nil })
}
func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
func (s *Service) notifyMessage(ctx context.Context, key RunKey, messageID string, user int64, body string) error {
	marker := fmt.Sprintf("notice:%s:%s:%d", key, messageID, user)
	if s.Store.Snapshot().Seen[marker] {
		return nil
	}
	if err := s.sendText(ctx, user, body); err != nil {
		return err
	}
	return s.Store.Update(func(st *State) error { st.Seen[marker] = true; return nil })
}
func (s *Service) syncDiscussion(ctx context.Context, run *afmsdk.Run, key RunKey, stageID, cursorKey string) error {
	for pageNumber := 0; pageNumber < 100; pageNumber++ {
		cursor := s.Store.Snapshot().Cursors[cursorKey]
		page, err := run.Discussion(ctx, stageID, cursor, 100)
		if err != nil {
			var h *afmsdk.HTTPError
			if errors.As(err, &h) && h.StatusCode == http.StatusNotFound {
				return nil
			}
			return err
		}
		for _, m := range page.Messages {
			seenKey := "discussion:" + string(key) + ":" + m.ID
			if s.Store.Snapshot().Seen[seenKey] {
				continue
			}
			if err := s.Store.Update(func(st *State) error {
				for id, o := range st.Outbox {
					if o.RunKey != key || o.Kind != "discussion" || o.AFMMessageID == "" || o.AFMMessageID != m.ParentMessageID {
						continue
					}
					if _, ok := o.RecipientStatus[m.AuthorID]; ok {
						o.RecipientStatus[m.AuthorID] = ReceiptReplied
						all := true
						for _, recipientStatus := range o.RecipientStatus {
							if recipientStatus != ReceiptReplied {
								all = false
							}
						}
						if all {
							o.Status = ReceiptReplied
						}
						st.Outbox[id] = o
					}
				}
				return nil
			}); err != nil {
				return err
			}
			if m.AuthorKind != "user" && strings.TrimSpace(m.Body) != "" {
				for user := range s.Allowed {
					if err := s.notifyMessage(ctx, key, m.ID, user, fmt.Sprintf("AFM · %s · %s\n%s", key, stageID, m.Body)); err != nil {
						return err
					}
				}
			}
			if err := s.Store.Update(func(st *State) error { st.Seen[seenKey] = true; return nil }); err != nil {
				return err
			}
		}
		if page.NextCursor > cursor {
			if err := s.Store.Update(func(st *State) error { st.Cursors[cursorKey] = page.NextCursor; return nil }); err != nil {
				return err
			}
		}
		if !page.HasMore || page.NextCursor <= cursor {
			return nil
		}
	}
	return errors.New("discussion page limit reached")
}
