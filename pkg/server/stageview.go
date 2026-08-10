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
// ordered slice, following rs.StageOrder. Replaces handleStatus's previous
// five-parallel-map construction.
func buildStageViews(rs state.RunState, runDir string, stageInteractive, stageAutoApprove map[string]bool) []StageView {
	views := make([]StageView, 0, len(rs.StageOrder))
	for _, id := range rs.StageOrder {
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
