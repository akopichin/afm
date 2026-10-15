package main

import (
	"fmt"
	"os"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
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
