// Этап 9 (T9.1-T9.3): validating and resolving a dynamically-added regular stage — inserted before a
// not-yet-started stage, or appended to the current DAG's sinks. Everything here is pure (no pkg/state.Store,
// no locking): it only ever reads an immutable *Registry and returns a result or a *ValidationError. The
// live-run commit path (pkg/orchestrator) is what turns a resolved InsertionPlan into a durable
// state.StageGraphOperation and calls WithPlan under the SAME lock as the scheduler.
package graph

import (
	"fmt"
	"slices"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// StageDraft is Этап 9's deliberately narrow shape for a dynamically added regular stage: always the
// existing autonomous track (agents:[auto]) with a prompt and a command from this run's pre-prepared set —
// never script/script_before/script_after/hooks/mounts/secrets (dev-plan's Этап 9 scope note). A UI
// constructor for every flow.Stage field is explicitly out of scope for the first release.
type StageDraft struct {
	ID      string
	Name    string
	Command string
	Prompt  string
}

func (d StageDraft) toStage(dependsOn []string) flow.Stage {
	return flow.Stage{
		ID:        d.ID,
		Name:      d.Name,
		Agents:    []flow.AgentType{flow.AgentAuto},
		Prompt:    d.Prompt,
		Command:   d.Command,
		DependsOn: slices.Clone(dependsOn),
	}
}

// Sinks returns, in the registry's stable order, every stage with no dependent — "the end of the flow"
// (T9.3, §11.4) is defined as the current DAG's sinks, NOT the last element of the YAML stage array: the two
// can differ once dynamic stages exist. nil-safe (see Stages' doc comment).
func (r *Registry) Sinks() []string {
	if r == nil {
		return nil
	}
	hasDependent := make(map[string]bool, len(r.order))
	for _, id := range r.order {
		for _, dep := range r.deps[id] {
			hasDependent[dep] = true
		}
	}
	var sinks []string
	for _, id := range r.order {
		if !hasDependent[id] {
			sinks = append(sinks, id)
		}
	}
	return sinks
}

// ValidationError is a stable, machine-readable rejection for a stage-graph draft — the same {Code, Reason}
// shape as state.OperationRejectedError, kept as its own small type rather than reusing
// pkg/assistants.ErrorCode's catalog: that catalog is scoped to the collaboration (helper-task) API
// (pkg/assistants/errors.go's own doc comment), and this is a different request shape entirely.
type ValidationError struct {
	Code   string
	Reason string
}

func (e *ValidationError) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Reason) }

// Error codes for the add-stage draft API (Этап 9).
const (
	CodeInvalidStageID     = "invalid_stage_id"
	CodeDuplicateStageID   = "duplicate_stage_id"
	CodeUnknownDependency  = "unknown_dependency"
	CodeCommandNotPrepared = "command_not_prepared"
	CodeTargetNotFound     = "target_not_found"
)

// validateDraftShape checks the conditions common to InsertBefore and Append that depend ONLY on the
// registry's current stage set, never on live run state: id safety (the same [A-Za-z0-9._-] charset every
// other stage id in this codebase is validated against — flow.StageIDSafe, not a second regex), uniqueness,
// and — when isPrepared is non-nil — that draft.Command is one of this run's pre-prepared commands (T8.5): a
// command only added to config.yaml/flow.yaml after the run started must never become pickable
// (TestDynamic_CanOnlyPickPreparedExecutor).
func validateDraftShape(r *Registry, draft StageDraft, isPrepared func(cmd string) bool) error {
	if !flow.StageIDSafe(draft.ID) {
		return &ValidationError{Code: CodeInvalidStageID, Reason: fmt.Sprintf("stage id %q must match [A-Za-z0-9._-] (non-empty, not \".\"/\"..\")", draft.ID)}
	}
	if _, exists := r.StageByID(draft.ID); exists {
		return &ValidationError{Code: CodeDuplicateStageID, Reason: fmt.Sprintf("stage id %q already exists", draft.ID)}
	}
	if isPrepared != nil && !isPrepared(draft.Command) {
		return &ValidationError{Code: CodeCommandNotPrepared, Reason: fmt.Sprintf("command %q is not in this run's pre-prepared set", draft.Command)}
	}
	return nil
}

// InsertionPlan is the fully-resolved, side-effect-free result of validating ONE draft insertion against a
// registry — everything T9.1's preview needs to show the user (dependency diff, sinks an append will wait
// for), and everything T9.4's commit needs to build the durable state.StageGraphPayload, computed in exactly
// one place so preview and commit can never disagree.
type InsertionPlan struct {
	Stage flow.Stage
	// ExtraDeps maps an EXISTING stage id -> the dependency ids appended to it (empty for Append).
	ExtraDeps map[string][]string
	// Order is the full stage order AFTER this insertion.
	Order []string
	// Warnings are non-fatal notices (e.g. an explicit Append dependency that is currently failed/blocked).
	Warnings []string
}

// PlanInsertBefore validates and resolves inserting draft before targetID (§11.3): the new stage inherits
// target's CURRENT dependencies, and target gets the new stage added as an ADDITIONAL dependency — target's
// ORIGINAL dependencies are never removed, they may still be needed for its own direct artifact inputs
// (TestInsert_PreservesTargetDirectDependencies). Every other branch of the DAG is untouched: nothing here
// ever rewrites any OTHER stage's DependsOn. isPrepared may be nil to skip the command-prepared check (pure
// graph-shape tests).
func (r *Registry) PlanInsertBefore(draft StageDraft, targetID string, isPrepared func(cmd string) bool) (InsertionPlan, error) {
	if err := validateDraftShape(r, draft, isPrepared); err != nil {
		return InsertionPlan{}, err
	}
	target, ok := r.StageByID(targetID)
	if !ok {
		return InsertionPlan{}, &ValidationError{Code: CodeTargetNotFound, Reason: fmt.Sprintf("target stage %q not found", targetID)}
	}

	newStage := draft.toStage(target.DependsOn) // N inherits T's CURRENT deps, verbatim
	order := make([]string, 0, len(r.order)+1)
	for _, id := range r.order {
		if id == targetID {
			order = append(order, newStage.ID)
		}
		order = append(order, id)
	}
	return InsertionPlan{
		Stage:     newStage,
		ExtraDeps: map[string][]string{targetID: {newStage.ID}},
		Order:     order,
	}, nil
}

// PlanAppend validates and resolves appending draft to the end of the flow (§11.4): "end" means the current
// DAG's sinks (Registry.Sinks), not flow.yaml's last element. explicitDeps, if non-nil, overrides the sinks
// default; any id it names that does not exist in r is rejected. Depending on a currently failed/blocked
// stage is ALLOWED but returns a warning, not an error — failedOrBlocked reports that for one dependency id
// (nil means never warn, e.g. a pure structural test with no live run state to consult).
func (r *Registry) PlanAppend(draft StageDraft, explicitDeps []string, isPrepared func(cmd string) bool, failedOrBlocked func(id string) bool) (InsertionPlan, error) {
	if err := validateDraftShape(r, draft, isPrepared); err != nil {
		return InsertionPlan{}, err
	}
	deps := explicitDeps
	if deps == nil {
		deps = r.Sinks()
	}
	var warnings []string
	for _, dep := range deps {
		if _, ok := r.StageByID(dep); !ok {
			return InsertionPlan{}, &ValidationError{Code: CodeUnknownDependency, Reason: fmt.Sprintf("dependency %q not found", dep)}
		}
		if failedOrBlocked != nil && failedOrBlocked(dep) {
			warnings = append(warnings, fmt.Sprintf("depends on %q, which is currently failed or blocked", dep))
		}
	}
	newStage := draft.toStage(deps)
	order := append(slices.Clone(r.order), newStage.ID)
	return InsertionPlan{Stage: newStage, Order: order, Warnings: warnings}, nil
}

// addedStage bundles one stage_graph_added record's two mutating effects — WithPlan and Materialize below
// share this shape and the materializeStages helper so a live addition and a replayed one can never diverge.
type addedStage struct {
	stage     flow.Stage
	extraDeps map[string][]string
}

// WithPlan returns a NEW Registry one revision ahead of r, with plan's stage added — the live counterpart to
// Materialize's restart-time replay (T9.5).
func (r *Registry) WithPlan(plan InsertionPlan) *Registry {
	stages := materializeStages(r.Stages(), []addedStage{{stage: plan.Stage, extraDeps: plan.ExtraDeps}}, plan.Order)
	return r.WithStages(stages)
}

// Materialize rebuilds the full stage set from the stages frozen at run start (graph_registry.json), every
// stage_graph_added record committed since (dynamic, in commit order), and the run's current StageOrder
// (T9.5) — the same state.RunState.StageOrder every other recovery path already restores by replay, so this
// function never needs to re-derive ordering itself. dynamic == nil/empty returns frozen (reordered to match
// order) unchanged in content.
func Materialize(frozen []flow.Stage, dynamic []state.DynamicStageAdded, order []string) []flow.Stage {
	added := make([]addedStage, 0, len(dynamic))
	for _, d := range dynamic {
		added = append(added, addedStage{stage: d.Stage, extraDeps: d.ExtraDeps})
	}
	return materializeStages(frozen, added, order)
}

// materializeStages applies every addedStage (new stage definition + extra deps on an existing stage), in
// order, on top of base, then re-orders the result to match order (ids in order but missing from the
// resulting set — should never happen — are silently skipped rather than panicking).
func materializeStages(base []flow.Stage, added []addedStage, order []string) []flow.Stage {
	byID := make(map[string]flow.Stage, len(base)+len(added))
	for _, s := range base {
		byID[s.ID] = s
	}
	for _, a := range added {
		byID[a.stage.ID] = a.stage
		for target, extra := range a.extraDeps {
			t := byID[target]
			t.DependsOn = append(slices.Clone(t.DependsOn), extra...)
			byID[target] = t
		}
	}
	out := make([]flow.Stage, 0, len(order))
	seen := make(map[string]bool, len(order))
	for _, id := range order {
		if seen[id] {
			continue
		}
		seen[id] = true
		if s, ok := byID[id]; ok {
			out = append(out, s)
		}
	}
	return out
}
