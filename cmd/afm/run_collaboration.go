package main

import (
	"context"
	"fmt"
	"os"

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

	available := func(cmd string) (bool, string, assistants.ReadonlyEnforcement) {
		reason := config.HelperCommandRejection(cmd, cfg)
		return reason == "", reason, assistants.ReadonlyEnforcement(config.HelperReadonlyEnforcement(cmd, cfg))
	}
	global := profilesFromConfig(cfg.Collaboration.Profiles)
	reg, err := assistants.BuildRegistry(nil, global, nil, cfg.Client.Command, available)
	if err != nil {
		// A malformed profile (duplicate/unsafe id) disables the capability instead of failing the whole
		// run — the same posture as startSideAgent's "open failed → disable, don't crash the flow".
		fmt.Fprintf(os.Stderr, "warning: collaboration disabled: %v\n", err)
		return &collaborationRuntime{policy: collaborationPolicy{Enabled: false, Depth: policy.Depth}}
	}
	return &collaborationRuntime{policy: policy, registry: reg}
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

func (a collaborationActions) PauseTask(ctx context.Context, taskID string) error {
	return a.orch.PauseTask(ctx, assistants.TaskID(taskID))
}

func (a collaborationActions) ContinueTask(ctx context.Context, taskID string) error {
	return a.orch.ContinueTask(ctx, assistants.TaskID(taskID))
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
	out := make([]server.AssistantTaskView, 0, len(tasks))
	for _, t := range tasks {
		view := server.AssistantTaskView{
			ID:                  string(t.ID),
			ProfileID:           string(t.ProfileID),
			Title:               t.Title,
			Mode:                string(t.Mode),
			Trigger:             string(t.Trigger),
			Status:              string(t.Status),
			StatusReason:        t.StatusReason,
			ReadonlyEnforcement: string(t.ProfileSnapshot.ReadonlyEnforcement),
		}
		if t.ParentTaskID != nil {
			view.ParentTaskID = string(*t.ParentTaskID)
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
