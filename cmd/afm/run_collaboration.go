package main

import (
	"context"
	"fmt"
	"os"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/server"
	"github.com/akopichin/afm/pkg/state"
)

// collaborationPolicy is the resolved, immutable-for-the-process-lifetime outcome of the collaboration
// switch and its delegation-depth policy. Resolved ONCE at process start (see resolveCollaboration's call
// site in executeFlow) — no hot reload (dev-plan.md Этап 1 §3.3).
type collaborationPolicy struct {
	Enabled bool
	Depth   assistants.DepthPolicy
}

// resolveCollaboration resolves the effective collaboration toggle and delegation-depth policy, modeled on
// sideAgentEnabled (cmd/afm/run_side_agent.go:22).
//
// Enabled priority: env AFM_COLLABORATION > config collaboration.enabled > default ON — there is
// deliberately no flow-level override for "enabled" (flow.FlowCollaboration carries only MaxDepth; a flow
// cannot re-enable a feature switched off by env/config, see flow.FlowCollaboration.UnmarshalYAML).
//
// Depth priority: flow > project config > global config > built-in default (assistants.ResolveDepth, T0.2).
// cfg.Collaboration.MaxDepthLayers() recovers the global/project provenance that mergeFile already folded
// together, so DepthPolicy.Source names the right layer instead of always blaming "project".
func resolveCollaboration(cfg config.Config, f *flow.Flow) collaborationPolicy {
	enabled := cfg.Collaboration.IsEnabled()

	var flowDepth *int
	if f != nil && f.Collaboration != nil {
		flowDepth = f.Collaboration.MaxDepth
	}
	global, project := cfg.Collaboration.MaxDepthLayers()
	depth, err := assistants.ResolveDepth(global, project, flowDepth)
	if err != nil {
		// Negative max_depth is already rejected at config-load time (pkg/config/collaboration.go's
		// validateCollaborationMaxDepth) and at flow-parse time (flow.FlowCollaboration.UnmarshalYAML), so
		// this is unreachable in practice. Fail closed rather than panic: no helpers, not an unrestricted one.
		return collaborationPolicy{Enabled: false, Depth: assistants.DepthPolicy{Source: assistants.DepthSourceDefault}}
	}

	return collaborationPolicy{Enabled: enabled, Depth: depth}
}

// resolveCollaborationMode resolves the dashboard view mode: flow > config (global/project already merged,
// project wins) > assistants.CollaborationModeTeam. It is a pure presentation value, deliberately separate
// from resolveCollaboration: AFM_COLLABORATION, collaboration.enabled and max_depth do not affect it, and it
// is not part of the durable policy record (state.Store.EnsurePolicyRecorded).
func resolveCollaborationMode(cfg config.Config, f *flow.Flow) assistants.CollaborationMode {
	if f != nil && f.Collaboration != nil && f.Collaboration.Mode != nil {
		return *f.Collaboration.Mode
	}
	if cfg.Collaboration.Mode != nil {
		return *cfg.Collaboration.Mode
	}
	return assistants.CollaborationModeTeam
}

// collaborationRuntime owns the resolved policy and, when enabled, the derived profile registry. Mirrors
// sideAgentRuntime: a nil receiver / disabled policy / registry-build failure all collapse to "capability
// off" rather than failing the run — collaboration is additive, not reliability-core (Инвариант 9: default
// ON must not itself start any model call, and a config mistake here must not take the whole flow down).
type collaborationRuntime struct {
	policy   collaborationPolicy
	registry *assistants.Registry
}

// startCollaboration resolves the policy, durably records it (T2.6/T2.7 — see below) and, if enabled, builds
// the profile registry from this process's config. There is no code here creating profiles from an HTTP
// request — only config (global/project collaboration.profiles) and the (currently empty) built-in catalog
// feed the registry.
//
// store is used ONLY to durably record the resolved policy (state.Store.EnsurePolicyRecorded) before this
// function returns — the T2.7 recovery-ordering requirement that policy be resolved and any change recorded
// BEFORE any consumer (orchestrator.New) is created. No task is created here and no agent is spawned; this
// is pure bookkeeping, consistent with Инвариант 9 (default ON must not itself start any model call).
func startCollaboration(store *state.Store, cfg config.Config, f *flow.Flow) *collaborationRuntime {
	policy := resolveCollaboration(cfg, f)
	if !policy.Enabled {
		return &collaborationRuntime{policy: policy}
	}

	if _, err := store.EnsurePolicyRecorded(policy.Depth, state.Actor{Kind: "system", ID: "afm"}); err != nil {
		// A storage hiccup here must not take down the whole run before it even starts its stages — fall
		// back to "capability off" (same posture as a malformed profile below), surfaced as a warning rather
		// than a hard failure, mirroring startSideAgent's "open failed → disable, don't crash the flow".
		fmt.Fprintf(os.Stderr, "warning: collaboration disabled: failed to record policy: %v\n", err)
		return &collaborationRuntime{policy: collaborationPolicy{Enabled: false, Depth: policy.Depth}}
	}

	reg, err := buildProfileRegistry(cfg)
	if err != nil {
		// A malformed profile (duplicate/unsafe id) disables the capability instead of failing the whole
		// run — the same posture as startSideAgent's "open failed → disable, don't crash the flow".
		fmt.Fprintf(os.Stderr, "warning: collaboration disabled: %v\n", err)
		return &collaborationRuntime{policy: collaborationPolicy{Enabled: false, Depth: policy.Depth}}
	}
	return &collaborationRuntime{policy: policy, registry: reg}
}

// buildProfileRegistry builds the collaboration profile registry from config — the one validation of
// collaboration.profiles, shared by afm run and afm validate.
func buildProfileRegistry(cfg config.Config) (*assistants.Registry, error) {
	available := func(cmd string) (bool, string, assistants.ReadonlyEnforcement) {
		reason := config.HelperCommandRejection(cmd, cfg)
		return reason == "", reason, assistants.ReadonlyEnforcement(config.HelperReadonlyEnforcement(cmd, cfg))
	}
	return assistants.BuildRegistry(nil, profilesFromConfig(cfg.Collaboration.Profiles), nil, cfg.Client.Command, available)
}

// checkDeclaredAssistants preflights every stage's assistants: field against the resolved collaboration
// runtime, BEFORE any stage starts. Mirrors config.ValidateVerifySpecs's spot in executeFlow, but cannot live
// there: it needs the real profile registry, which doesn't exist until startCollaboration runs.
//
// When collaboration is disabled, a declared assistants: is a silent no-op at runtime (attachDeclaredAssistants's
// own gate) — so this does not error, only warns once per offending stage, keeping AFM_COLLABORATION=0 a
// working way to temporarily turn helpers off without editing every flow.yaml that declares them.
//
// Profile command AVAILABILITY (ProfileView.Available) is deliberately NOT checked here — PATH differs
// between host and container, and a hard check here would break Docker runs where the profile's command
// only exists inside the image. An unavailable profile is instead rejected, best-effort, by Attach itself at
// runtime (attachDeclaredAssistants already logs and moves on without blocking the stage).
func checkDeclaredAssistants(f *flow.Flow, collab *collaborationRuntime) error {
	if !collab.policy.Enabled || collab.registry == nil {
		for _, s := range f.Stages {
			if len(s.Assistants) == 0 {
				continue
			}
			fmt.Fprintf(os.Stderr, "warning: stage %q declares \"assistants\" but collaboration is disabled — they will not be attached\n", s.ID)
		}
		return nil
	}
	for _, s := range f.Stages {
		for _, a := range s.Assistants {
			if _, ok := collab.registry.Get(assistants.ProfileID(a.Profile)); !ok {
				return fmt.Errorf("stage %q: unknown assistant profile %q (not in collaboration.profiles)", s.ID, a.Profile)
			}
		}
	}
	return nil
}

// checkFlowTeam preflights the flow's team: like checkDeclaredAssistants does a stage's assistants:, before
// any stage starts. With collaboration disabled it only warns. Otherwise only the members this run has not
// created an agent for yet need an existing profile: a saved agent keeps the profile snapshot it was created
// with, so a resume still works after its profile left config.
func checkFlowTeam(store *state.Store, team flow.Team, collab *collaborationRuntime) error {
	if !collab.policy.Enabled || collab.registry == nil {
		warnTeamWithoutCollaboration(team)
		return nil
	}
	toCreate, err := orchestrator.TeamMembersToCreate(store, team)
	if err != nil {
		return err
	}
	return checkTeamProfiles(toCreate, collab.registry)
}

// checkTeamProfiles reports the first team member whose profile is not in the registry.
func checkTeamProfiles(team flow.Team, reg *assistants.Registry) error {
	for _, m := range team {
		if _, ok := reg.Get(assistants.ProfileID(m.Profile)); !ok {
			return fmt.Errorf("team member %q: unknown assistant profile %q (not in collaboration.profiles)", m.ID, m.Profile)
		}
	}
	return nil
}

// warnTeamWithoutCollaboration warns that a declared team will not be created: like stage assistants, a
// team never blocks a run with collaboration off, so AFM_COLLABORATION=0 still runs the flow.
func warnTeamWithoutCollaboration(team flow.Team) {
	if len(team) > 0 {
		fmt.Fprintln(os.Stderr, "warning: the flow declares \"team\" but collaboration is disabled — its agents will not be added")
	}
}

// assistantProfileCommands returns the distinct, non-empty commands named by this process's configured
// collaboration profiles — T8.5: Docker mount/wrapper preparation (reexecFlow, run_environment.go) needs this
// list BEFORE startCollaboration ever runs (that happens after re-exec, possibly inside the container), so a
// profile's non-standard command gets the same ":ro" binary mount flow.Stage commands already get. Returns
// nil when collaboration is disabled — no profile command needs preparing for a capability that is off.
func assistantProfileCommands(cfg config.Config, f *flow.Flow) []string {
	if !resolveCollaboration(cfg, f).Enabled {
		return nil
	}
	seen := make(map[string]bool, len(cfg.Collaboration.Profiles))
	var cmds []string
	for _, p := range cfg.Collaboration.Profiles {
		if p.Command == "" || seen[p.Command] {
			continue
		}
		seen[p.Command] = true
		cmds = append(cmds, p.Command)
	}
	return cmds
}

// profilesFromConfig converts the config layer's YAML-natural map[string]ProfileConfig into the slice form
// assistants.BuildRegistry expects — Profile.ID carries the map key, which BuildRegistry's duplicate-id
// guard and path-safety check then validate.
func profilesFromConfig(m map[string]config.ProfileConfig) []assistants.Profile {
	if len(m) == 0 {
		return nil
	}
	out := make([]assistants.Profile, 0, len(m))
	for id, p := range m {
		out = append(out, assistants.Profile{
			ID:           assistants.ProfileID(id),
			Title:        p.Title,
			Command:      p.Command,
			Instructions: p.Instructions,
			Capabilities: assistants.Capabilities{Delegate: p.Delegate},
		})
	}
	return out
}

// service returns the server-facing view of the registry. nil receiver, a disabled policy, or a failed
// registry build all return a nil interface — capability off: /api/assistant-profiles is not registered,
// mirroring sideAgentRuntime.service().
func (r *collaborationRuntime) service() server.AssistantProfilesService {
	if r == nil || !r.policy.Enabled || r.registry == nil {
		return nil
	}
	return collaborationService{r}
}

// collaborationService adapts collaborationRuntime to server.AssistantProfilesService.
type collaborationService struct{ r *collaborationRuntime }

func (s collaborationService) Profiles() []assistants.ProfileView { return s.r.registry.List() }

func (s collaborationService) MaxDepth() (depth int, source string) {
	return s.r.policy.Depth.MaxDepth, string(s.r.policy.Depth.Source)
}

// actions returns the server-facing write-adapter for the collaboration HTTP API (T3.7's AssistantActions:
// Attach/pause/continue/cancel one helper task), nil when the capability is off (same gate as service()
// above). Must be called AFTER orch is constructed (orchestrator.New) — unlike service(), which only needs
// the registry, this wraps the real *orchestrator.Orchestrator whose Attach/PauseTask/ContinueTask/CancelTask
// methods do the actual admission/lifecycle work (pkg/orchestrator/assistants.go, T3.4).
func (r *collaborationRuntime) actions(orch *orchestrator.Orchestrator) server.AssistantActions {
	if r == nil || !r.policy.Enabled || r.registry == nil {
		return nil
	}
	return collaborationActions{orch}
}

// reader returns the server-facing read-adapter (T3.7's AssistantsReader: a stage's task tree, one task's
// event-history page), nil under the same gate as actions() above.
func (r *collaborationRuntime) reader(orch *orchestrator.Orchestrator) server.AssistantsReader {
	if r == nil || !r.policy.Enabled || r.registry == nil {
		return nil
	}
	return collaborationReader{orch}
}

// collaborationActions adapts *orchestrator.Orchestrator to server.AssistantActions: translates the HTTP
// layer's server.AttachRequest/string ids into the orchestrator's own orchestrator.AttachRequest/
// assistants.TaskID shapes. Every mutation here originates from the dashboard, so Actor is always "user" —
// mirrors the convention pkg/orchestrator/assistants_test.go's attachTestTask already uses.
type collaborationActions struct{ orch *orchestrator.Orchestrator }

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

// collaborationReader adapts *orchestrator.Orchestrator to server.AssistantsReader: converts the
// orchestrator/assistants domain types (assistants.Task, state.TaskEvent) into the server package's own
// dashboard-facing view shapes.
type collaborationReader struct{ orch *orchestrator.Orchestrator }

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
		if waived, reason, ok := r.orch.TaskWaiver(t.ID); ok && waived {
			view.Waived = true
			view.WaiverReason = reason
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

// discussionService/proposalActions (T6.7) — same nil-gated adapter pattern as actions()/reader() above, wired
// AFTER orch is constructed since both wrap the real *orchestrator.Orchestrator.

func (r *collaborationRuntime) discussionService(orch *orchestrator.Orchestrator) server.DiscussionService {
	if r == nil || !r.policy.Enabled {
		return nil
	}
	return collaborationDiscussion{orch}
}

func (r *collaborationRuntime) proposalActions(orch *orchestrator.Orchestrator) server.ProposalActions {
	if r == nil || !r.policy.Enabled {
		return nil
	}
	return collaborationProposals{orch}
}

// collaborationDiscussion adapts *orchestrator.Orchestrator to server.DiscussionService.
type collaborationDiscussion struct{ orch *orchestrator.Orchestrator }

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

// collaborationProposals adapts *orchestrator.Orchestrator to server.ProposalActions.
type collaborationProposals struct{ orch *orchestrator.Orchestrator }

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
