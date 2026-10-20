package afmsdk

import (
	"context"
	"time"
)

// StageStatus mirrors afm's internal stage FSM status, duplicated here as a plain string type so this
// module has no dependency on afm's Go packages.
type StageStatus string

const (
	StagePending           StageStatus = "pending"
	StagePlanning          StageStatus = "planning"
	StageAwaitingApproval  StageStatus = "awaiting_approval"
	StageRevising          StageStatus = "revising"
	StageReady             StageStatus = "ready"
	StageRunning           StageStatus = "running"
	StageRetrying          StageStatus = "retrying"
	StageAwaitingUserInput StageStatus = "awaiting_user_input"
	StageDone              StageStatus = "done"
	StageFailed            StageStatus = "failed"
	StageHookFailed        StageStatus = "hook_failed"
	// StageAwaitingAssistants (afm's helper-agents feature, dev-plan.md Stage 7/T7.1): a stage whose main
	// work is done but whose completion barrier is still waiting on a required helper task. Neither Done nor
	// Failed — an SDK consumer polling Status in a loop must be able to tell this apart from "still running"
	// or "actually stuck", rather than seeing an unrecognized raw string.
	StageAwaitingAssistants StageStatus = "awaiting_assistants"
	StageAwaitingPeer       StageStatus = "awaiting_peer"
	StagePaused             StageStatus = "paused"
)

// StageView is the dashboard's joined runtime and configuration view. It has
// updated_at but no per-stage sequence or conditional action token.
type StageView struct {
	ID                        string      `json:"id"`
	Name                      string      `json:"name,omitempty"`
	Status                    StageStatus `json:"status"`
	UpdatedAt                 time.Time   `json:"updated_at"`
	Interactive               bool        `json:"interactive"`
	Autonomous                bool        `json:"autonomous"`
	AutoApprove               bool        `json:"auto_approve"`
	HasDialog                 bool        `json:"has_dialog"`
	IsScript                  bool        `json:"is_script"`
	PausedFrom                StageStatus `json:"paused_from,omitempty"`
	ShowPlan                  bool        `json:"show_plan"`
	ShowDialog                bool        `json:"show_dialog"`
	Buttons                   []string    `json:"buttons,omitempty"`
	AwaitingAssistantsBlocked bool        `json:"awaiting_assistants_blocked,omitempty"`
}

type Capabilities struct {
	FileBrowser   bool `json:"file_browser"`
	SideAgent     bool `json:"side_agent"`
	Collaboration bool `json:"collaboration"`
	AddStage      bool `json:"add_stage"`
}

// RunStatus is a snapshot of a run's flow name and per-stage statuses.
type RunStatus struct {
	FlowName     string
	RunID        string
	StartedAt    time.Time
	RunState     string
	EndedAt      *time.Time
	LastSeq      uint64
	Capabilities Capabilities
	StageViews   []StageView
	Stages       map[string]StageStatus
	// Done is true when the run has at least one stage and every stage is StageDone.
	Done bool
	// Failed is true when any stage is StageFailed or StageHookFailed.
	Failed bool
}

type statusWire struct {
	FlowName     string       `json:"flow_name"`
	RunID        string       `json:"run_id,omitempty"`
	StartedAt    time.Time    `json:"started_at"`
	RunState     string       `json:"run_status,omitempty"`
	EndedAt      *time.Time   `json:"ended_at,omitempty"`
	LastSeq      uint64       `json:"last_seq"`
	Capabilities Capabilities `json:"capabilities"`
	Stages       []StageView  `json:"stages"`
}

// Status fetches the current run status from afm's own dashboard API
// (GET /api/status on the run's isolated --port).
func (r *Run) Status(ctx context.Context) (RunStatus, error) {
	var wire statusWire
	if err := r.getJSON(ctx, "/api/status", &wire); err != nil {
		return RunStatus{}, err
	}

	out := RunStatus{
		FlowName: wire.FlowName, RunID: wire.RunID, StartedAt: wire.StartedAt,
		RunState: wire.RunState, EndedAt: wire.EndedAt, LastSeq: wire.LastSeq,
		Capabilities: wire.Capabilities, StageViews: wire.Stages,
		Stages: make(map[string]StageStatus, len(wire.Stages)),
	}
	allDone := len(wire.Stages) > 0
	for _, s := range wire.Stages {
		st := s.Status
		out.Stages[s.ID] = st
		if st == StageFailed || st == StageHookFailed {
			out.Failed = true
		}
		if st != StageDone {
			allDone = false
		}
	}
	out.Done = allDone
	return out, nil
}
