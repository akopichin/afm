package main

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/sideagent"
	"github.com/akopichin/afm/pkg/state"
)

// sideAgentMeshBridge has one worker. It derives all progress from the two
// journals on every pass; a missed notification or process restart loses none.
type sideAgentMeshBridge struct {
	orch *orchestrator.Orchestrator
	mgr  *sideagent.Manager

	mu               sync.Mutex
	closing          bool
	wake             chan struct{}
	abort            chan struct{}
	done             chan struct{}
	once             sync.Once
	cursor           uint64 // worker-owned; full replay starts at zero after process restart
	noticed          map[agentcomm.MessageID]bool
	failedRunnerDone chan struct{} // worker-owned; join before unavailable reconciliation
}

func startSideAgentMesh(orch *orchestrator.Orchestrator, mgr *sideagent.Manager) *sideAgentMeshBridge {
	b := &sideAgentMeshBridge{orch: orch, mgr: mgr, wake: make(chan struct{}, 1), abort: make(chan struct{}), done: make(chan struct{}), noticed: make(map[agentcomm.MessageID]bool)}
	if mgr != nil && !mgr.Unavailable() {
		orch.SetSideAgentAvailable(true)
	}
	go b.run()
	return b
}

// A fresh capability-off run has no side participant to reconcile. A resumed
// run retains its historical roster row and inbox even without a usable store.
func startHistoricalSideAgentMesh(ctx context.Context, orch *orchestrator.Orchestrator) (*sideAgentMeshBridge, error) {
	page, err := orch.ListParticipants(ctx, orchestrator.ListParticipantsRequest{Actor: state.Actor{Kind: "system"}})
	if err != nil {
		return nil, err
	}
	for _, p := range page.Participants {
		if p.ID == orch.SideAgentParticipantID() {
			return startSideAgentMesh(orch, nil), nil
		}
	}
	return nil, nil
}

func (b *sideAgentMeshBridge) CloseInput() {
	// SetSideAgentAvailable waits for in-flight Mesh send commits before the
	// bridge decides the final inbox is empty.
	b.mu.Lock()
	if b.closing {
		b.mu.Unlock()
		return
	}
	b.orch.SetSideAgentAvailable(false)
	b.closing = true
	b.mu.Unlock()
	b.signal()
}

func (b *sideAgentMeshBridge) Abort() {
	b.CloseInput()
	b.once.Do(func() { close(b.abort) })
}

func (b *sideAgentMeshBridge) Drain(ctx context.Context) bool {
	select {
	case <-b.done:
		return true
	case <-ctx.Done():
		return false
	}
}

func (b *sideAgentMeshBridge) signal() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

func (b *sideAgentMeshBridge) run() {
	defer close(b.done)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-b.abort:
			return
		default:
		}
		idle := b.reconcile()
		b.mu.Lock()
		closing := b.closing
		b.mu.Unlock()
		if closing && idle {
			return
		}
		select {
		case <-b.abort:
			return
		case <-b.wake:
		case <-ticker.C:
		}
	}
}

// reconcile scans the FULL inbox in journal order. Receipts are deliberately
// ignored: delivery may have been persisted just before the process died.
// At most one new side turn is admitted per pass.
func (b *sideAgentMeshBridge) reconcile() bool {
	if b.mgr == nil || b.mgr.Unavailable() {
		b.CloseInput()
		// A failed journal can no longer report the runner's outcome. Revoke its
		// Mesh descriptor by stopping and joining it before releasing sources.
		if b.mgr != nil {
			if b.failedRunnerDone == nil {
				b.mgr.Shutdown()
				b.failedRunnerDone = make(chan struct{})
				go func() {
					b.mgr.Drain(context.Background())
					close(b.failedRunnerDone)
				}()
			}
			select {
			case <-b.failedRunnerDone:
			default:
				return false
			}
		}
		return b.reconcileUnavailable()
	}
	ctx := context.Background()
	cursor := b.cursor
	page, err := b.orch.ReadInbox(ctx, orchestrator.ReadInboxRequest{Participant: b.orch.SideAgentParticipantID(), Cursor: cursor, Limit: 100, Actor: state.Actor{Kind: "system"}})
	if err != nil {
		log.Printf("WARN: side Mesh inbox: %v", err)
		return false
	}
	if len(page.Messages) == 0 {
		return true
	}
	accepted, terminal := sideMeshTurns(b.mgr.History())
	name := map[agentcomm.ParticipantID]string{}
	if page, err := b.orch.ListParticipants(ctx, orchestrator.ListParticipantsRequest{Actor: state.Actor{Kind: "system"}}); err == nil {
		for _, p := range page.Participants {
			name[p.ID] = p.DisplayName
		}
	}
	for {
		for _, msg := range page.Messages {
			if msg.Sender == b.orch.SideAgentParticipantID() {
				b.cursor = msg.Seq
				continue
			}
			if b.orch.SideAgentMeshResponseFailedAlready(msg.ID) {
				if !msg.AutoResult && !b.noticed[msg.ID] && !b.hasSideTerminal(msg) {
					b.orch.RepublishSideAgentMeshFailure(msg.ID)
					b.noticed[msg.ID] = true
				}
				b.cursor = msg.Seq
				continue
			}
			turn, exists := accepted[string(msg.ID)]
			if exists {
				out, done := terminal[turn]
				if !done {
					return false
				}
				if msg.AutoResult {
					if b.orch.SideAgentMeshSourceAccepted(msg.ID) {
						if err := b.orch.SideAgentMeshMarkUnprocessed(msg.ID); err != nil {
							return false
						}
					}
					b.cursor = msg.Seq
					continue
				} // passive input cannot trigger a reflex result
				if !b.publishTerminal(msg, out) {
					return false
				}
				if b.orch.SideAgentMeshSourceAccepted(msg.ID) {
					if err := b.orch.SideAgentMeshMarkUnprocessed(msg.ID); err != nil {
						return false
					}
				}
				b.cursor = msg.Seq
				continue
			}
			// A failed admission has no side-journal row; its deterministic Mesh
			// terminal response (or undeliverable marker) is checked first.
			if b.hasSideTerminal(msg) {
				if err := b.orch.SideAgentMeshMarkUnprocessed(msg.ID); err != nil {
					return false
				}
				b.cursor = msg.Seq
				continue
			}
			senderName := name[msg.Sender]
			if senderName == "" {
				senderName = string(msg.Sender)
			}
			_, err := b.mgr.Send("mesh:"+string(msg.ID), msg.Body, sideagent.TurnTarget{
				SourceMessageID: string(msg.ID), SourceThreadID: string(msg.ThreadID),
				SourceSenderID: string(msg.Sender), SourceSenderName: senderName,
			})
			if err == nil {
				return false
			}
			var se *sideagent.Error
			if errors.As(err, &se) && se.Code == sideagent.ErrCodeBusy {
				return false
			}
			if msg.AutoResult {
				if err := b.orch.SideAgentMeshResponseFailed(msg.ID, "Could not process side message"); err != nil && !errors.Is(err, orchestrator.ErrSideAgentExplicitReply) {
					return false
				}
				b.noticed[msg.ID] = true
				b.cursor = msg.Seq
				continue
			}
			if !b.publishFailure(msg, "Could not answer this message") {
				return false
			}
			if err := b.orch.SideAgentMeshMarkUnprocessed(msg.ID); err != nil {
				return false
			}
			b.cursor = msg.Seq
		}
		if len(page.Messages) < 100 {
			return true
		}
		cursor = page.NextCursor
		page, err = b.orch.ReadInbox(ctx, orchestrator.ReadInboxRequest{Participant: b.orch.SideAgentParticipantID(), Cursor: cursor, Limit: 100, Actor: state.Actor{Kind: "system"}})
		if err != nil {
			log.Printf("WARN: side Mesh inbox: %v", err)
			return false
		}
	}
}

// An unavailable side journal cannot supply terminal turn events. The Mesh
// inbox and receipts still describe every source and pending reply wait, so
// release them without consulting Manager. This also handles capability-off
// resumes, failed side-store startup and a write failure after turn_accepted.
func (b *sideAgentMeshBridge) reconcileUnavailable() bool {
	ctx := context.Background()
	cursor := uint64(0)
	for {
		page, err := b.orch.ReadInbox(ctx, orchestrator.ReadInboxRequest{
			Participant: b.orch.SideAgentParticipantID(), Cursor: cursor, Limit: 100,
			Actor: state.Actor{Kind: "system"},
		})
		if err != nil {
			log.Printf("WARN: unavailable side Mesh inbox: %v", err)
			return false
		}
		for _, msg := range page.Messages {
			if msg.Sender == b.orch.SideAgentParticipantID() {
				continue
			}
			if b.orch.SideAgentMeshResponseFailedAlready(msg.ID) {
				if !msg.AutoResult && !b.noticed[msg.ID] && !b.hasSideTerminal(msg) {
					b.orch.RepublishSideAgentMeshFailure(msg.ID)
					b.noticed[msg.ID] = true
				}
				continue
			}
			if msg.AutoResult || b.hasSideTerminal(msg) {
				if b.orch.SideAgentMeshSourceAccepted(msg.ID) {
					if err := b.orch.SideAgentMeshMarkUnprocessed(msg.ID); err != nil {
						log.Printf("WARN: release unavailable side Mesh source %s: %v", msg.ID, err)
						return false
					}
				}
				continue
			}
			if !b.publishFailure(msg, "Could not answer: side agent unavailable") {
				return false
			}
			if b.orch.SideAgentMeshSourceAccepted(msg.ID) {
				if err := b.orch.SideAgentMeshMarkUnprocessed(msg.ID); err != nil {
					log.Printf("WARN: release unavailable side Mesh source %s: %v", msg.ID, err)
					return false
				}
			}
		}
		if len(page.Messages) < 100 {
			return true
		}
		cursor = page.NextCursor
	}
}

type sideMeshOutcome struct {
	completed bool
	text      string
}

func sideMeshTurns(events []sideagent.Event) (map[string]string, map[string]sideMeshOutcome) {
	accepted := map[string]string{}
	terminal := map[string]sideMeshOutcome{}
	lastText := map[string]string{}
	for _, e := range events {
		switch e.Type {
		case sideagent.EventTurnAccepted:
			var d sideagent.TurnAcceptedData
			if e.DecodeData(&d) == nil && d.SourceMessageID != "" {
				accepted[d.SourceMessageID] = e.TurnID
			}
		case sideagent.EventAssistantText:
			var d sideagent.AssistantTextData
			if e.DecodeData(&d) == nil {
				lastText[e.TurnID] = d.Text
			}
		case sideagent.EventTurnCompleted:
			terminal[e.TurnID] = sideMeshOutcome{completed: true, text: lastText[e.TurnID]}
		case sideagent.EventTurnFailed, sideagent.EventTurnInterrupted:
			terminal[e.TurnID] = sideMeshOutcome{}
		default:
			continue
		}
	}
	return accepted, terminal
}

func (b *sideAgentMeshBridge) hasSideTerminal(msg agentcomm.Message) bool {
	cursor := uint64(0)
	for {
		page, err := b.orch.ReadThread(context.Background(), orchestrator.ReadThreadRequest{ThreadID: msg.ThreadID, Reader: b.orch.SideAgentParticipantID(), Cursor: cursor, Limit: 100, Actor: state.Actor{Kind: "system"}})
		if err != nil {
			return false
		}
		for _, reply := range page.Messages {
			if reply.Sender == b.orch.SideAgentParticipantID() && reply.InReplyTo != nil && *reply.InReplyTo == msg.ID {
				return true
			}
		}
		if len(page.Messages) < 100 {
			return false
		}
		cursor = page.NextCursor
	}
}

func (b *sideAgentMeshBridge) publishTerminal(msg agentcomm.Message, outcome sideMeshOutcome) bool {
	if b.hasSideTerminal(msg) {
		return true
	} // explicit afm agent reply wins
	if outcome.completed && strings.TrimSpace(outcome.text) != "" {
		return b.sendTerminal(msg, agentcomm.MessageKindReply, boundedSideBody(outcome.text))
	}
	return b.publishFailure(msg, "Could not answer this message")
}

func (b *sideAgentMeshBridge) publishFailure(msg agentcomm.Message, reason string) bool {
	return b.sendTerminal(msg, agentcomm.MessageKindResult, reason)
}

func (b *sideAgentMeshBridge) sendTerminal(msg agentcomm.Message, kind agentcomm.MessageKind, body string) bool {
	_, err := b.orch.Send(context.Background(), orchestrator.SendRequest{
		OperationID: "side-mesh-reply:" + string(msg.ID), ThreadID: msg.ThreadID,
		Sender: b.orch.SideAgentParticipantID(), Recipients: []agentcomm.ParticipantID{msg.Sender},
		Kind: kind, InReplyTo: &msg.ID, Body: body, AutoResult: true,
		Actor: state.Actor{Kind: "system"},
	})
	if err == nil {
		return true
	}
	if errors.Is(err, orchestrator.ErrSideAgentExplicitReply) {
		return true
	}
	var rejected *state.OperationRejectedError
	if !errors.As(err, &rejected) {
		log.Printf("WARN: side Mesh reply: %v", err)
		return false
	}
	if failErr := b.orch.SideAgentMeshResponseFailed(msg.ID, "Could not answer: conversation unavailable"); failErr != nil {
		if errors.Is(failErr, orchestrator.ErrSideAgentExplicitReply) {
			return true
		}
		log.Printf("WARN: side Mesh terminal failure: %v", failErr)
		return false
	}
	b.noticed[msg.ID] = true
	return true
}

func boundedSideBody(body string) string {
	const marker = "\n[Response truncated]"
	body = strings.ToValidUTF8(body, "�")
	if len(body) <= agentcomm.MaxBodyBytes {
		return body
	}
	limit := agentcomm.MaxBodyBytes - len(marker)
	body = body[:limit]
	for !utf8.ValidString(body) {
		body = body[:len(body)-1]
	}
	return body + marker
}
