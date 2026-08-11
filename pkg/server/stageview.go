package server

import (
	"os"
	"path/filepath"
	"time"

	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// StageView is the per-stage read model served by GET /api/status. It joins
// state.StageState (event-log-derived) with the flow's static config
// (Interactive/AutoApprove) and two filesystem-derived runtime flags
// (Autonomous/HasDialog), and precomputes the two dashboard visibility
// capabilities (ShowPlan/ShowDialog) that pkg/web/dashboard's App.tsx used to
// recompute client-side from the same four raw flags — one source of truth
// for "can this stage's plan/dialog panel be shown" instead of two
// (Go here, TypeScript there) that had to be kept in sync by hand.
type StageView struct {
	ID          string            `json:"id"`
	Name        string            `json:"name,omitempty"`
	Status      state.StageStatus `json:"status"`
	UpdatedAt   time.Time         `json:"updated_at"`
	Interactive bool              `json:"interactive"`
	Autonomous  bool              `json:"autonomous"`
	AutoApprove bool              `json:"auto_approve"`
	HasDialog   bool              `json:"has_dialog"`
	// ShowPlan/ShowDialog — the two visibility capabilities pkg/web/dashboard's
	// App.tsx computed from Autonomous/Status/Interactive/HasDialog. See that
	// file's showPlan/showDialog comment (removed in the frontend task of this
	// same plan) for the original rationale this mirrors exactly.
	ShowPlan   bool `json:"show_plan"`
	ShowDialog bool `json:"show_dialog"`
}

// buildStageViews joins rs.Stages (event-log state) with the flow's static
// interactive/auto_approve config and two on-disk runtime flags
// (autonomous.flag presence, any <phase>.dialog.jsonl presence) into one
// slice ordered by topoOrder(rs.StageOrder, dependsOn) — a display-only
// reordering. rs.StageOrder itself (the authoritative declaration order used
// by state/scheduling) is never touched. Replaces handleStatus's previous
// five-parallel-map construction.
func buildStageViews(rs state.RunState, runDir string, stageInteractive, stageAutoApprove map[string]bool, dependsOn map[string][]string) []StageView {
	order := topoOrder(rs.StageOrder, dependsOn)
	views := make([]StageView, 0, len(order))
	for _, id := range order {
		st := rs.Stages[id]
		autonomous := stageIsAutonomous(runDir, id)
		hasDialog := stageHasDialog(runDir, id)
		interactive := stageInteractive[id]
		showPlan := !autonomous || st.Status == state.StatusFailed
		showDialog := interactive || autonomous || hasDialog

		views = append(views, StageView{
			ID:          id,
			Name:        rs.StageNames[id],
			Status:      st.Status,
			UpdatedAt:   st.UpdatedAt,
			Interactive: interactive,
			Autonomous:  autonomous,
			AutoApprove: stageAutoApprove[id],
			HasDialog:   hasDialog,
			ShowPlan:    showPlan,
			ShowDialog:  showDialog,
		})
	}
	return views
}

// topoOrder returns ids reordered so every stage renders after all of its
// depends_on — a display-only concern the dashboard's stage list uses instead
// of raw flow.yaml declaration order. Stages with no ordering constraint
// between them (independent stages, or several unblocked by the same
// dependency) keep their original relative order: this is a stable Kahn's
// algorithm, queue seeded and re-fed in declaration order, so e.g. stage1
// (depends_on: [stage2]) among stage2..stage5 (no deps) renders as
// stage2, stage3, stage4, stage5, stage1 — not spliced ahead of its
// unrelated siblings just because it was declared first.
//
// flow.ParseFile's detectCycles already guarantees ids/dependsOn form a
// well-formed acyclic graph referencing only known stage ids by the time this
// runs; the length check below is a defensive fallback (return ids as-is)
// for that invariant, not a real code path.
func topoOrder(ids []string, dependsOn map[string][]string) []string {
	index := make(map[string]int, len(ids))
	for i, id := range ids {
		index[id] = i
	}

	indegree := make(map[string]int, len(ids))
	dependents := make(map[string][]string, len(ids))
	for _, id := range ids {
		for _, dep := range dependsOn[id] {
			if _, ok := index[dep]; !ok {
				continue
			}
			indegree[id]++
			dependents[dep] = append(dependents[dep], id)
		}
	}

	queue := make([]string, 0, len(ids))
	for _, id := range ids {
		if indegree[id] == 0 {
			queue = append(queue, id)
		}
	}

	result := make([]string, 0, len(ids))
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		result = append(result, id)
		for _, dependent := range dependents[id] {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				queue = append(queue, dependent)
			}
		}
	}

	if len(result) != len(ids) {
		return ids
	}
	return result
}

func stageIsAutonomous(runDir, stageID string) bool {
	_, err := os.Stat(filepath.Join(runDir, stageID, "autonomous.flag"))
	return err == nil
}

func stageHasDialog(runDir, stageID string) bool {
	for _, p := range flow.Phases() {
		if _, err := os.Stat(filepath.Join(runDir, stageID, string(p)+".dialog.jsonl")); err == nil {
			return true
		}
	}
	return false
}
