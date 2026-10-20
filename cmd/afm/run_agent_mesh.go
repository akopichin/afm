package main

import (
	"context"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/server"
	"github.com/akopichin/afm/pkg/state"
)

// Agent Mesh Phase 3 (T3.1): adapts *orchestrator.Orchestrator to server.AgentMeshActions, translating the
// HTTP layer's server-local request/response shapes into orchestrator.Service's own DTOs — mirrors
// collaborationActions (run_collaboration.go) exactly. Every call's Actor is the TOKEN-RESOLVED participant
// (server.go's authenticateAgentMesh), never a value read from the JSON body.
type agentMeshActions struct{ orch *orchestrator.Orchestrator }

// agentMeshServerActions returns the server-facing adapter. Called from startDashboard only once
// cfg.Server.GetPort() > 0 has already been confirmed (the dashboard is about to start listening) — unlike
// orch.MeshBaseURL(), which also requires SetDashboardURL to have run, this adapter's methods need no base
// URL of their own (only the HTTP route needs to exist); SetDashboardURL always completes before any stage
// spawns an agent that could actually call this API.
func agentMeshServerActions(orch *orchestrator.Orchestrator) server.AgentMeshActions {
	return agentMeshActions{orch}
}

func (a agentMeshActions) ResolveToken(token string) (string, bool) {
	p, ok := a.orch.ResolveMeshToken(token)
	return string(p), ok
}

func meshActor(participant string) state.Actor {
	return state.Actor{Kind: "agent", ID: participant}
}

func (a agentMeshActions) ListParticipants(ctx context.Context, actor string) ([]agentcomm.Participant, error) {
	page, err := a.orch.ListParticipants(ctx, orchestrator.ListParticipantsRequest{Actor: meshActor(actor)})
	if err != nil {
		return nil, err
	}
	return page.Participants, nil
}

func toParticipantIDs(ids []string) []agentcomm.ParticipantID {
	out := make([]agentcomm.ParticipantID, len(ids))
	for i, id := range ids {
		out[i] = agentcomm.ParticipantID(id)
	}
	return out
}

func (a agentMeshActions) Send(ctx context.Context, requestID, actor string, req server.MeshSendRequest) (server.MeshSendReceipt, error) {
	var inReplyTo *agentcomm.MessageID
	if req.InReplyTo != "" {
		id := agentcomm.MessageID(req.InReplyTo)
		inReplyTo = &id
	}
	kind := agentcomm.MessageKind(req.Kind)
	if kind == "" {
		kind = agentcomm.MessageKindMessage
	}
	receipt, err := a.orch.SendFromAgent(ctx, orchestrator.SendRequest{
		OperationID: requestID, ThreadID: agentcomm.ThreadID(req.ThreadID), Sender: agentcomm.ParticipantID(actor),
		Recipients: toParticipantIDs(req.Recipients), Kind: kind, InReplyTo: inReplyTo,
		Body: req.Body, ArtifactRefs: req.ArtifactRefs, Actor: meshActor(actor),
	})
	if err != nil {
		return server.MeshSendReceipt{}, err
	}
	return server.MeshSendReceipt{MessageID: string(receipt.MessageID), Seq: receipt.Seq, ThreadID: string(receipt.ThreadID)}, nil
}

func toMeshMessagePage(p orchestrator.MessagePage) server.MeshMessagePage {
	return server.MeshMessagePage{Messages: p.Messages, NextCursor: p.NextCursor}
}

// Inbox returns only messages the agent has not been handed yet unless all is set: a run agent gets a fresh
// session every turn, and showing it messages it already handled (in an earlier turn's <inbox> or inbox read)
// makes it answer them again.
func (a agentMeshActions) Inbox(ctx context.Context, actor string, cursor uint64, limit int, all bool) (server.MeshMessagePage, error) {
	reader := agentcomm.ParticipantID(actor)
	page, err := a.orch.ReadInbox(ctx, orchestrator.ReadInboxRequest{Participant: reader, Cursor: cursor, Limit: limit, OnlyNew: !all, Actor: meshActor(actor)})
	if err != nil {
		return server.MeshMessagePage{}, err
	}
	// Handing messages to the agent is what "delivered" means: a later thread-less addressed_message wait must
	// not wake it again for a message it has already read. Idempotent per (message, recipient).
	if reader != a.orch.SideAgentParticipantID() {
		for _, m := range page.Messages {
			if err := a.orch.MarkMessageDelivered(ctx, m.ID, reader); err != nil {
				return server.MeshMessagePage{}, err
			}
		}
	}
	return toMeshMessagePage(page), nil
}

func (a agentMeshActions) Thread(ctx context.Context, actor, threadID string, cursor uint64, limit int) (server.MeshMessagePage, error) {
	page, err := a.orch.ReadThread(ctx, orchestrator.ReadThreadRequest{
		ThreadID: agentcomm.ThreadID(threadID), Reader: agentcomm.ParticipantID(actor), Cursor: cursor, Limit: limit, Actor: meshActor(actor),
	})
	if err != nil {
		return server.MeshMessagePage{}, err
	}
	return toMeshMessagePage(page), nil
}

func (a agentMeshActions) Ack(ctx context.Context, requestID, actor, messageID string) error {
	return a.orch.Acknowledge(ctx, orchestrator.AcknowledgeRequest{
		OperationID: requestID, MessageID: agentcomm.MessageID(messageID), Participant: agentcomm.ParticipantID(actor), Actor: meshActor(actor),
	})
}

func (a agentMeshActions) Wait(ctx context.Context, requestID, actor string, req server.MeshWaitRequest) (server.MeshWaitReceipt, error) {
	var deadline *time.Time
	if req.DeadlineUnix != nil {
		t := time.Unix(*req.DeadlineUnix, 0).UTC()
		deadline = &t
	}
	receipt, err := a.orch.Wait(ctx, orchestrator.WaitRequest{
		OperationID: requestID, Waiter: agentcomm.ParticipantID(actor), Condition: req.Condition,
		Deadline: deadline, ContinuationNote: req.ContinuationNote, Actor: meshActor(actor),
	})
	if err != nil {
		return server.MeshWaitReceipt{}, err
	}
	return server.MeshWaitReceipt{WaitID: string(receipt.WaitID), Status: string(receipt.Status)}, nil
}

func (a agentMeshActions) CancelWait(ctx context.Context, requestID, actor, waitID, reason string) error {
	return a.orch.CancelWait(ctx, orchestrator.CancelWaitRequest{
		OperationID: requestID, WaitID: agentcomm.WaitID(waitID), Reason: reason, Actor: meshActor(actor),
	})
}

func (a agentMeshActions) PublishBoard(ctx context.Context, requestID, actor string, req server.MeshPublishRequest) (agentcomm.BoardEntry, error) {
	return a.orch.Publish(ctx, orchestrator.PublishRequest{
		OperationID: requestID, Author: agentcomm.ParticipantID(actor), Key: req.Key, Status: req.Status,
		ArtifactRefs: req.ArtifactRefs, ExpectedVersion: req.ExpectedVersion,
		Actor: meshActor(actor),
	})
}

func (a agentMeshActions) Board(ctx context.Context, key string) ([]agentcomm.BoardEntry, error) {
	page, err := a.orch.ReadBoard(ctx, orchestrator.ReadBoardRequest{Key: key})
	if err != nil {
		return nil, err
	}
	return page.Entries, nil
}
