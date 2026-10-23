package main

import (
	"context"
	"slices"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/server"
	"github.com/akopichin/afm/pkg/state"
)

// Agent Mesh Phase 7 (T7.1): adapts *orchestrator.Orchestrator to server.MeshActions, the UI-facing
// /api/mesh/* surface. Unlike agentMeshActions (run_agent_mesh.go), whose actor comes from a per-invocation
// bearer token, every call here is the human dashboard operator — meshUIActor/meshUIParticipant are fixed
// constants, never a value derived from the HTTP request, mirroring collaborationActions'
// state.Actor{Kind: "user"} convention (run_collaboration.go).
type meshActions struct{ orch *orchestrator.Orchestrator }

// meshServerActions returns the UI-facing adapter. Wired into server.Config unconditionally from
// startDashboard, the SAME way agentMeshServerActions is: the underlying Service methods (not this adapter)
// are what reject a request when collaboration is disabled (e.g. AttachAndSend's own
// assistants.CodeFeatureDisabled check) — there is no separate gate to mirror here.
func meshServerActions(orch *orchestrator.Orchestrator) server.MeshActions {
	return meshActions{orch}
}

// meshUIActor/meshUIParticipant are the fixed identity every /api/mesh/* call uses — see the package doc
// comment above.
var meshUIActor = state.Actor{Kind: "user"}

const meshUIParticipant = agentcomm.UserParticipantID

func (a meshActions) ListParticipants(ctx context.Context) ([]agentcomm.Participant, error) {
	page, err := a.orch.ListParticipants(ctx, orchestrator.ListParticipantsRequest{Actor: meshUIActor})
	if err != nil {
		return nil, err
	}
	return page.Participants, nil
}

// Threads implements the T7.3 dependency's HTTP side: every thread the human dashboard operator belongs to.
// orchestrator.ListThreads already treats meshUIParticipant ("user") as the run owner who sees every thread,
// not just the ones naming it as a Member (the same carve-out ReadThread/Inbox use elsewhere on this
// adapter) — exactly what the Team view's "Conversations" grouping needs.
func (a meshActions) Threads(ctx context.Context) (orchestrator.ThreadPage, error) {
	return a.orch.ListThreads(ctx, orchestrator.ListThreadsRequest{Participant: meshUIParticipant, Actor: meshUIActor})
}

// OpenThread ensures the human is a member of every thread it opens through this endpoint — a thread the
// operator cannot send into would make the UI's own composer useless, and ensureParticipantEffects
// (pkg/orchestrator/agentcomm_roster.go) already treats "user" as always resolvable.
func (a meshActions) OpenThread(ctx context.Context, requestID string, req server.MeshOpenThreadRequest) (agentcomm.Thread, error) {
	members := toParticipantIDs(req.Members)
	if !slices.Contains(members, meshUIParticipant) {
		members = append([]agentcomm.ParticipantID{meshUIParticipant}, members...)
	}
	return a.orch.OpenThread(ctx, orchestrator.OpenThreadRequest{
		OperationID: requestID, Title: req.Title, Scope: agentcomm.Scope(req.Scope),
		Members: members, MaxMessages: req.MaxMessages, OriginStage: req.OriginStage, Actor: meshUIActor,
	})
}

func (a meshActions) CloseThread(ctx context.Context, requestID, threadID string) error {
	return a.orch.CloseThread(ctx, orchestrator.CloseThreadRequest{
		OperationID: requestID, ThreadID: agentcomm.ThreadID(threadID), Actor: meshUIActor,
	})
}

func (a meshActions) UpdateThreadTitle(ctx context.Context, requestID, threadID string, req server.MeshTopicRequest) (orchestrator.UpdateThreadTitleReceipt, error) {
	return a.orch.UpdateThreadTitle(ctx, orchestrator.UpdateThreadTitleRequest{
		OperationID: requestID, ThreadID: agentcomm.ThreadID(threadID), Title: req.Title,
		ExpectedTitleSeq: req.ExpectedTitleSeq, Actor: meshUIActor,
	})
}

// Send is the human's message, through the same name-resolving send agents use (orchestrator.SendFromAgent):
// recipients may be aliases; without a thread it goes into the open conversation of exactly those people that
// the origin stage's feed shows, or opens one in the same operation. A retry under the same request id
// replays the committed message.
func (a meshActions) Send(ctx context.Context, requestID string, req server.MeshUserSendRequest) (server.MeshSendReceipt, error) {
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
		OperationID: requestID, ThreadID: agentcomm.ThreadID(req.ThreadID), Sender: meshUIParticipant,
		Recipients: toParticipantIDs(req.Recipients), Kind: kind, InReplyTo: inReplyTo,
		Body: req.Body, ArtifactRefs: req.ArtifactRefs, OriginStage: req.OriginStage, Actor: meshUIActor,
	})
	if err != nil {
		return server.MeshSendReceipt{}, err
	}
	return server.MeshSendReceipt{MessageID: string(receipt.MessageID), Seq: receipt.Seq, ThreadID: string(receipt.ThreadID)}, nil
}

func (a meshActions) Thread(ctx context.Context, threadID string, cursor uint64, limit int) (server.MeshMessagePage, error) {
	page, err := a.orch.ReadThread(ctx, orchestrator.ReadThreadRequest{
		ThreadID: agentcomm.ThreadID(threadID), Reader: meshUIParticipant, Cursor: cursor, Limit: limit, Actor: meshUIActor,
	})
	if err != nil {
		return server.MeshMessagePage{}, err
	}
	view := toMeshMessagePage(page)
	view.SideAgentFailedIDs = a.sideAgentFailures(page.Messages)
	return view, nil
}

func (a meshActions) Inbox(ctx context.Context, cursor uint64, limit int) (server.MeshMessagePage, error) {
	page, err := a.orch.ReadInbox(ctx, orchestrator.ReadInboxRequest{
		Participant: meshUIParticipant, Cursor: cursor, Limit: limit, Actor: meshUIActor,
	})
	if err != nil {
		return server.MeshMessagePage{}, err
	}
	return toMeshMessagePage(page), nil
}

// StageMessages is the stage feed's conversation page; empty lists, never null, for the dashboard.
func (a meshActions) StageMessages(ctx context.Context, stageID string, cursor uint64, limit int) (server.MeshStageMessagePage, error) {
	view := toMeshStagePage(a.orch.StageMessages(stageID, cursor, limit))
	view.SideAgentFailedIDs = a.sideAgentFailures(view.Messages)
	return view, nil
}

func (a meshActions) sideAgentFailures(messages []agentcomm.Message) map[agentcomm.MessageID]bool {
	var failed map[agentcomm.MessageID]bool
	for _, msg := range messages {
		if !msg.AutoResult && a.orch.SideAgentMeshResponseFailedAlready(msg.ID) {
			if failed == nil {
				failed = make(map[agentcomm.MessageID]bool)
			}
			failed[msg.ID] = true
		}
	}
	return failed
}

func toMeshStagePage(p orchestrator.StageMessagePage) server.MeshStageMessagePage {
	page := server.MeshStageMessagePage{Messages: p.Messages, Threads: p.Threads, NextCursor: p.NextCursor, HasMore: p.HasMore}
	if page.Messages == nil {
		page.Messages = []agentcomm.Message{}
	}
	if page.Threads == nil {
		page.Threads = []agentcomm.Thread{}
	}
	return page
}

func (a meshActions) Board(ctx context.Context, key string) ([]agentcomm.BoardEntry, error) {
	page, err := a.orch.ReadBoard(ctx, orchestrator.ReadBoardRequest{Key: key})
	if err != nil {
		return nil, err
	}
	return page.Entries, nil
}

// AttachAndSend delegates to the T6.2 composite verbatim — this adapter composes nothing beyond translating
// ids/strings, per dev-plan.md's T7.1 note ("the handler itself composes nothing"). Scope/Mode pass through
// as given: AttachAndSendRequest's own zero values already mean ScopeStage/ModeAdvisory, so there is no
// default to duplicate here.
// PublishBoard posts a board entry as the human operator (meshUIActor) — e.g. a task the agents pick up — and
// notifies req.Notify about it.
func (a meshActions) PublishBoard(ctx context.Context, requestID string, req server.MeshBoardPublishRequest) (agentcomm.BoardEntry, error) {
	return a.orch.Publish(ctx, orchestrator.PublishRequest{
		OperationID: requestID, Author: meshUIParticipant, Key: req.Key, Status: req.Status,
		ArtifactRefs: req.ArtifactRefs, ExpectedVersion: req.ExpectedVersion, Notify: toParticipantIDs(req.Notify),
		Actor: meshUIActor,
	})
}

func (a meshActions) BoardTasksEnabled() bool { return a.orch.BoardTasksEnabled() }

func (a meshActions) BoardTasks(ctx context.Context, key string) ([]agentcomm.BoardTaskView, error) {
	return a.orch.BoardTasks(ctx, key)
}

// BoardTask makes a board task change as the human operator (meshUIParticipant).
func (a meshActions) BoardTask(ctx context.Context, requestID string, req server.MeshBoardTaskRequest) (orchestrator.BoardTaskReceipt, error) {
	return a.orch.BoardTask(ctx, boardTaskRequest(requestID, meshUIParticipant, req))
}

func (a meshActions) AttachAndSend(ctx context.Context, requestID string, req server.MeshAttachAndSendRequest) (server.MeshAttachAndSendResult, error) {
	result, err := a.orch.AttachAndSend(ctx, orchestrator.AttachAndSendRequest{
		OperationID: requestID, ProfileID: assistants.ProfileID(req.ProfileID), Scope: assistants.Scope(req.Scope),
		StageID: req.StageID, Mode: assistants.Mode(req.Mode), Title: req.Title, Instruction: req.Instruction,
		FirstMessage: req.FirstMessage, Recipients: toParticipantIDs(req.Recipients), OriginStage: req.OriginStage, Actor: meshUIActor,
	})
	if err != nil {
		return server.MeshAttachAndSendResult{}, err
	}
	var msgID *string
	if result.MessageID != nil {
		id := string(*result.MessageID)
		msgID = &id
	}
	return server.MeshAttachAndSendResult{
		TaskID: string(result.TaskID), ParticipantID: string(result.ParticipantID),
		ThreadID: string(result.ThreadID), MessageID: msgID, Participant: a.participant(ctx, result.ParticipantID),
	}, nil
}

// participant is id's current roster entry (with its alias), or nil when the roster does not list it.
func (a meshActions) participant(ctx context.Context, id agentcomm.ParticipantID) *agentcomm.Participant {
	roster, err := a.ListParticipants(ctx)
	if err != nil {
		return nil
	}
	i := slices.IndexFunc(roster, func(p agentcomm.Participant) bool { return p.ID == id })
	if i < 0 {
		return nil
	}
	return &roster[i]
}

// Loops lists every loop of the run: the human operator sees the agents' loops too.
func (a meshActions) Loops(ctx context.Context) (orchestrator.LoopPage, error) {
	return a.orch.ListLoops(ctx, orchestrator.ListLoopsRequest{Actor: meshUIActor})
}

// Loop adds or stops a loop as the human operator; origin_stage is the Feed it is created from.
func (a meshActions) Loop(ctx context.Context, requestID string, cmd server.MeshLoopCommand) (orchestrator.LoopView, error) {
	return applyLoopCommand(ctx, a.orch, requestID, meshUIActor, cmd)
}
