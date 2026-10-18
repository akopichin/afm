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

// AddRunAgentRequest carries no OperationID: the request itself has no natural per-click idempotency key
// (unlike AttachRequest/AttachAndSendRequest, whose caller already owns an HTTP request id), and dev-plan.md's
// own test name for this wrapper — TestAddRunAgent_IdempotentPerProfile — asks for a STRONGER guarantee than
// plain operation-id replay: two "Add run agent" submissions for the SAME profile (e.g. a double-click, or two
// browser tabs, each minting its own request id) must resolve to the SAME participant, not two standing roles
// for the one profile. addRunAgentOperationID below derives a canonical, profile-keyed operation id instead of
// taking the caller's — making that guarantee a plain idempotent AttachAndSend replay under the hood.
type AddRunAgentRequest struct {
	ProfileID    assistants.ProfileID
	Title        string
	Instruction  string
	FirstMessage *string
	Recipients   []agentcomm.ParticipantID
	Actor        state.Actor
}

// addRunAgentOperationID derives AddRunAgent's canonical operation id from the profile id ALONE — the one
// thing that makes a run-scoped role a *standing* role is that there is exactly one of it per profile per run,
// regardless of how many times "Add run agent" is submitted for that profile or under how many different
// caller-generated request ids.
func addRunAgentOperationID(profileID assistants.ProfileID) string {
	return "add-run-agent:" + string(profileID)
}

// AddRunAgent is a thin request-shaping wrapper over AttachAndSend — dev-plan.md's T6.2b "Done when" line is
// explicit that this function must contain no ApplyRunOperation call of its own. Attaching after the run has
// started finalizing is rejected by AttachAndSend's own ApplyRunOperation call (ApplyRunOperation's
// unconditional Step 1 check), with no extra plumbing needed here.
func (o *Orchestrator) AddRunAgent(ctx context.Context, req AddRunAgentRequest) (AttachAndSendResult, error) {
	return o.AttachAndSend(ctx, AttachAndSendRequest{
		OperationID:  addRunAgentOperationID(req.ProfileID),
		ProfileID:    req.ProfileID,
		Scope:        assistants.ScopeRun,
		Title:        req.Title,
		Instruction:  req.Instruction,
		FirstMessage: req.FirstMessage,
		Recipients:   req.Recipients,
		Actor:        req.Actor,
	})
}
