package afmsdk

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// StageStatus mirrors afm's internal stage FSM status
// (github.com/akopichin/afm/pkg/state.StageStatus), duplicated here as a
// plain string type so this module has no dependency on afm's Go packages.
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
)

// RunStatus is a snapshot of a run's flow name and per-stage statuses.
type RunStatus struct {
	FlowName string
	Stages   map[string]StageStatus
	// Done is true when the run has at least one stage and every stage is StageDone.
	Done bool
	// Failed is true when any stage is StageFailed or StageHookFailed.
	Failed bool
}

type statusWireStage struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type statusWire struct {
	FlowName string            `json:"flow_name"`
	Stages   []statusWireStage `json:"stages"`
}

// Status fetches the current run status from afm's own dashboard API
// (GET /api/status on the run's isolated --port).
func (r *Run) Status(ctx context.Context) (RunStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.baseURL+"/api/status", nil)
	if err != nil {
		return RunStatus{}, fmt.Errorf("afmsdk: status: %w", err)
	}
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return RunStatus{}, fmt.Errorf("afmsdk: status: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return RunStatus{}, fmt.Errorf("afmsdk: status: unexpected response %s: %s", resp.Status, body)
	}

	var wire statusWire
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return RunStatus{}, fmt.Errorf("afmsdk: status: decode response: %w", err)
	}

	out := RunStatus{
		FlowName: wire.FlowName,
		Stages:   make(map[string]StageStatus, len(wire.Stages)),
	}
	allDone := len(wire.Stages) > 0
	for _, s := range wire.Stages {
		st := StageStatus(s.Status)
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
