// Package orchestrator (this file, Agent Mesh Phase 6, T6.2b): AddRunAgent — the scope=run caller of
// AttachAndSend (agentcomm_attach.go). "Add run agent" is the dashboard action that creates a standing,
// run-scoped role ("my QA", "my DevOps") that outlives whatever stage is active when it is created.
package orchestrator

import (
	"context"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/state"
)

// AddRunAgentRequest is AttachAndSendRequest without Scope/StageID: a run agent is always run-scoped. Each
// call with a new OperationID adds a NEW agent — several agents of the same profile are allowed; replaying the
// same OperationID (a double-click, a retry) returns the agent that call already created.
type AddRunAgentRequest struct {
	OperationID  string
	ProfileID    assistants.ProfileID
	Title        string
	Instruction  string
	FirstMessage *string
	Recipients   []agentcomm.ParticipantID
	Actor        state.Actor
}

// AddRunAgent is a thin request-shaping wrapper over AttachAndSend — dev-plan.md's T6.2b "Done when" line is
// explicit that this function must contain no ApplyRunOperation call of its own. Attaching after the run has
// started finalizing is rejected by AttachAndSend's own ApplyRunOperation call (ApplyRunOperation's
// unconditional Step 1 check), with no extra plumbing needed here.
func (o *Orchestrator) AddRunAgent(ctx context.Context, req AddRunAgentRequest) (AttachAndSendResult, error) {
	return o.AttachAndSend(ctx, AttachAndSendRequest{
		OperationID:  req.OperationID,
		ProfileID:    req.ProfileID,
		Scope:        assistants.ScopeRun,
		Title:        req.Title,
		Instruction:  req.Instruction,
		FirstMessage: req.FirstMessage,
		Recipients:   req.Recipients,
		Actor:        req.Actor,
	})
}
