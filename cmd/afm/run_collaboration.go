package main

import (
	orchestrator "github.com/akopichin/afm/contracts/control"
	"github.com/akopichin/afm/contracts/state"

	"context"

	"github.com/akopichin/afm-engine/engine"
	stagefiles "github.com/akopichin/afm/contracts/activity"
	"github.com/akopichin/afm/contracts/agentcomm"
	"github.com/akopichin/afm/contracts/assistants"
	"github.com/akopichin/afm/pkg/server"
)

// profilesService returns the server-facing view of the profiles. nil receiver, a disabled policy, or a failed
// registry build all return a nil interface — capability off: /api/assistant-profiles is not registered.
// Built from the opened run: stages added during the run live only in its runtime registry.
func profilesService(r *engine.Collaboration, orch engine.CollaborationAPI) server.AssistantProfilesService {
	if r == nil || !r.Policy.Enabled || r.Registry == nil {
		return nil
	}
	return collaborationService{policy: r.Policy, orch: orch}
}

// collaborationService adapts engine.CollaborationAPI to server.AssistantProfilesService.
type collaborationService struct {
	policy engine.CollaborationPolicy
	orch   engine.CollaborationAPI
}

func (s collaborationService) Profiles(stageID string) ([]assistants.ProfileView, error) {
	return s.orch.AssistantProfiles(stageID)
}

func (s collaborationService) MaxDepth() (depth int, source string) {
	return s.policy.Depth.MaxDepth, string(s.policy.Depth.Source)
}

// assistantActions is the write adapter of the collaboration HTTP API, nil when the capability is off.
// It needs the built orchestrator, unlike profilesService, which only needs the registry.
func assistantActions(r *engine.Collaboration, orch engine.CollaborationAPI) server.AssistantActions {
	if r == nil || !r.Policy.Enabled || r.Registry == nil {
		return nil
	}
	return collaborationActions{orch}
}

// assistantsReader is the read adapter (task tree, event history), nil under the gate of assistantActions.
func assistantsReader(r *engine.Collaboration, orch engine.CollaborationAPI) server.AssistantsReader {
	if r == nil || !r.Policy.Enabled || r.Registry == nil {
		return nil
	}
	return collaborationReader{orch}
}

// collaborationActions adapts engine.CollaborationAPI to server.AssistantActions, translating HTTP request
// types and string ids to the orchestrator's. Every mutation comes from the dashboard, so Actor is "user".
type collaborationActions struct{ orch engine.CollaborationAPI }

func (a collaborationActions) Attach(ctx context.Context, stageID string, req server.AttachRequest) (string, error) {
	taskID, err := a.orch.Attach(ctx, orchestrator.AttachRequest{
		OperationID: req.RequestID,
		StageID:     stageID,
		ProfileID:   assistants.ProfileID(req.ProfileID),
		Title:       req.Title,
		Instruction: req.Instruction,
		Mode:        assistants.Mode(req.Mode),
		Trigger:     assistants.Trigger(req.Trigger),
		Actor:       state.Actor{Kind: "user"},
	})
	return string(taskID), err
}

func (a collaborationActions) PolicyRevision() (string, int) {
	return a.orch.AssistantsPolicyRevision()
}

func (a collaborationActions) PauseTask(ctx context.Context, taskID string) error {
	return a.orch.PauseTask(ctx, assistants.TaskID(taskID))
}

func (a collaborationActions) ContinueTask(ctx context.Context, taskID string) error {
	return a.orch.ContinueTask(ctx, assistants.TaskID(taskID))
}

// WaiveTask (Этап 7, T7.6) stamps the actor as the authenticated dashboard operator — never taken from the
// request body, so a crafted payload can never impersonate a human waiver (§19.5: only a human may waive).
func (a collaborationActions) WaiveTask(ctx context.Context, taskID string, reason string) error {
	return a.orch.WaiveTask(ctx, assistants.TaskID(taskID), state.Actor{Kind: string(assistants.OriginUser)}, reason)
}

func (a collaborationActions) CancelTask(ctx context.Context, taskID string) error {
	return a.orch.CancelTask(ctx, assistants.TaskID(taskID))
}

// collaborationReader adapts engine.CollaborationAPI to server.AssistantsReader: converts the
// orchestrator/assistants domain types (assistants.Task, state.TaskEvent) into the server package's own
// dashboard-facing view shapes.
type collaborationReader struct{ orch engine.CollaborationAPI }

func (r collaborationReader) TaskTree(stageID string) ([]server.AssistantTaskView, error) {
	tasks, err := r.orch.TaskTree(stageID)
	if err != nil {
		return nil, err
	}
	// Этап 7 (T7.5): a local-only lookup over this SAME flat tree backs BlocksRootCompletion below — no store
	// access needed beyond what TaskTree already returned.
	byID := make(map[assistants.TaskID]assistants.Task, len(tasks))
	for _, t := range tasks {
		byID[t.ID] = t
	}
	lookup := func(id assistants.TaskID) (assistants.Task, bool) { t, ok := byID[id]; return t, ok }

	out := make([]server.AssistantTaskView, 0, len(tasks))
	for _, t := range tasks {
		view := server.AssistantTaskView{
			ID:                   string(t.ID),
			ProfileID:            string(t.ProfileID),
			Title:                t.Title,
			Mode:                 string(t.Mode),
			Trigger:              string(t.Trigger),
			Status:               string(t.Status),
			StatusReason:         t.StatusReason,
			ReadonlyEnforcement:  string(t.ProfileSnapshot.ReadonlyEnforcement),
			Depth:                t.Depth,
			BlocksRootCompletion: assistants.BlocksRootCompletion(t, lookup),
		}
		if t.ParentTaskID != nil {
			view.ParentTaskID = string(*t.ParentTaskID)
		}
		// T5.6: surface a watcher's own progress (trigger kind is already view.Trigger above) — ok is false for
		// a plain one_shot task (never bound), leaving both pointers nil/omitted.
		if t.Status == assistants.StatusQueued {
			view.Quota = server.NewQuotaView(r.orch.TaskQuota(t.ID)) // a usage-limit park: same view as a stage's
		}
		if waived, reason, ok := r.orch.TaskWaiver(t.ID); ok && waived {
			view.Waived = true
			view.WaiverReason = reason
		}
		if queue, ok := r.orch.TaskRequests(t.ID); ok {
			view.ActiveRequestID = string(queue.Active)
			view.PendingRequests = queue.Pending
			view.LastRequestID = string(queue.Last)
			view.LastOutcome = string(queue.LastOutcome)
			view.Turn = queue.Turn
		}
		if passes, lastSeq, hasLast, ok := r.orch.WatcherStatus(t.ID); ok {
			view.WatchPasses = &passes
			if hasLast {
				view.WatchLastCheckpointSeq = &lastSeq
			}
		}
		out = append(out, view)
	}
	return out, nil
}

func (r collaborationReader) TaskEvents(taskID string, cursor uint64, limit int) ([]server.AssistantEventView, uint64, bool, error) {
	events, next, hasMore, err := r.orch.TaskEvents(assistants.TaskID(taskID), cursor, limit)
	if err != nil {
		return nil, 0, false, err
	}
	out := make([]server.AssistantEventView, 0, len(events))
	for _, e := range events {
		out = append(out, server.AssistantEventView{Seq: e.Seq, Type: e.Kind, Data: e.Payload})
	}
	return out, next, hasMore, nil
}

func (r collaborationReader) TaskActivity(taskID string, q stagefiles.ActivityQuery) (stagefiles.ActivityPage, error) {
	return r.orch.TaskActivity(assistants.TaskID(taskID), q)
}

// discussionService and proposalActions are nil-gated like actions() and reader(): both wrap the run's
// engine.CollaborationAPI, so they exist only after the orchestrator does.

func discussionService(r *engine.Collaboration, orch engine.CollaborationAPI) server.DiscussionService {
	if r == nil || !r.Policy.Enabled {
		return nil
	}
	return collaborationDiscussion{orch}
}

func proposalActions(r *engine.Collaboration, orch engine.CollaborationAPI) server.ProposalActions {
	if r == nil || !r.Policy.Enabled {
		return nil
	}
	return collaborationProposals{orch}
}

// collaborationDiscussion adapts engine.CollaborationAPI to server.DiscussionService.
type collaborationDiscussion struct{ orch engine.CollaborationAPI }

func (d collaborationDiscussion) Messages(stageID string, cursor uint64, limit int) ([]server.DiscussionMessageView, uint64, bool, error) {
	all := d.orch.DiscussionMessages(stageID)
	start := int(cursor)
	if start > len(all) {
		start = len(all)
	}
	end := start + limit
	if end > len(all) {
		end = len(all)
	}
	out := make([]server.DiscussionMessageView, 0, end-start)
	unanswered := d.orch.UnansweredDiscussionPosts(stageID)
	for _, m := range all[start:end] {
		out = append(out, server.DiscussionMessageView{
			ID: m.ID, AuthorKind: string(m.Author.Kind), AuthorID: m.Author.ID, Role: m.Role,
			Executor: m.Executor, Round: m.Round, ParentMessageID: m.ParentMessageID,
			ContextVersion: string(m.ContextVersion), Evidence: m.Evidence, Body: m.Body, Mentions: m.Mentions,
			Interrupt: m.Interrupt, Unanswered: unanswered[m.ID], AutoResult: m.AutoResult, CreatedAt: m.CreatedAt, Seq: m.Seq,
		})
	}
	return out, uint64(end), end < len(all), nil
}

func (d collaborationDiscussion) PostMessage(ctx context.Context, stageID string, post server.DiscussionPost) (string, error) {
	mentions := make([]agentcomm.ParticipantID, len(post.Mentions))
	for i, m := range post.Mentions {
		mentions[i] = agentcomm.ParticipantID(m)
	}
	return d.orch.PostDiscussionMessage(ctx, orchestrator.PostMessageRequest{
		OperationID: post.RequestID, StageID: stageID, AuthorKind: assistants.AuthorHuman,
		Round: assistants.RoundFreeform, Body: post.Body, Actor: state.Actor{Kind: "user"},
		Mentions: mentions, Interrupt: post.Interrupt,
	})
}

// collaborationProposals adapts engine.CollaborationAPI to server.ProposalActions.
type collaborationProposals struct{ orch engine.CollaborationAPI }

func (p collaborationProposals) Proposals(stageID string) ([]server.ProposalView, error) {
	props := p.orch.Proposals(stageID)
	out := make([]server.ProposalView, 0, len(props))
	for _, pr := range props {
		authors := make([]string, 0, len(pr.Authors))
		for _, a := range pr.Authors {
			if a.ID != "" {
				authors = append(authors, a.ID)
			}
		}
		out = append(out, server.ProposalView{
			ID: pr.ID, StageID: pr.StageID, TaskID: pr.TaskID, Type: string(pr.Type), Title: pr.Title,
			Rationale: pr.Rationale, Authors: authors, SourceMessageIDs: pr.SourceMessageIDs,
			SourceFindingIDs: pr.SourceFindingIDs, ExpectedStageGeneration: pr.ExpectedStageGeneration,
			Status: string(pr.Status), CreatedAt: pr.CreatedAt,
		})
	}
	return out, nil
}

func (p collaborationProposals) Accept(ctx context.Context, proposalID, requestID string) error {
	return p.orch.AcceptProposal(ctx, proposalID, requestID, state.Actor{Kind: "user"})
}

func (p collaborationProposals) Dismiss(ctx context.Context, proposalID, requestID, reason string) error {
	return p.orch.DismissProposal(ctx, proposalID, requestID, reason, state.Actor{Kind: "user"})
}
