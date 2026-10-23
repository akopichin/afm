package orchestrator

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/prompts"
	"github.com/akopichin/afm/pkg/state"
)

// SpawnHelperRequest is a stage agent's request for one temporary helper. It carries only what the agent
// chooses — the profile, the task's title and its instruction. The stage, scope, mode, trigger and the
// executor are never the caller's to choose: the stage is the actor's own, the helper is an advisory one-shot,
// and the profile resolves to its command, model and capabilities on the server.
type SpawnHelperRequest struct {
	OperationID string
	ProfileID   assistants.ProfileID
	Title       string
	Instruction string
	Actor       state.Actor // the stage agent: Kind "agent", ID "stage:<id>"
}

// SpawnHelper starts a helper for the stage actor through AttachAndSend, so the task, its participant, the
// thread and the stage's request to it are ONE commit under the existing limits (quota, depth, profile
// availability, a stage that has not finished). The returned message id is the request the helper answers:
// the stage can wait on it like on any other request (reply_to / reply_to_all), and the helper's result comes
// back as a reply to it (finishing a helper of OriginStageAgent, runHelperTask), or the wait ends with the
// helper's failure reason.
func (o *Orchestrator) SpawnHelper(ctx context.Context, req SpawnHelperRequest) (AttachAndSendResult, error) {
	stageID, ok := stageAgentOf(req.Actor)
	if !ok {
		return AttachAndSendResult{}, attachRejected(assistants.CodeActorNotAllowed, "only a stage agent can spawn a helper, not %s %q", req.Actor.Kind, req.Actor.ID)
	}
	instruction := strings.TrimSpace(req.Instruction)
	if instruction == "" {
		return AttachAndSendResult{}, attachRejected(assistants.CodeInvalidRequest, "an instruction is required")
	}
	// The request the helper answers is its instruction; the task keeps the full text.
	request := prompts.TruncateUTF8(instruction, agentcomm.MaxBodyBytes, "…")
	stage := meshStageParticipantID(stageID)
	return o.AttachAndSend(ctx, AttachAndSendRequest{
		OperationID: req.OperationID, ProfileID: req.ProfileID, Scope: assistants.ScopeStage, StageID: stageID,
		Mode: assistants.ModeAdvisory, Title: req.Title, Instruction: req.Instruction,
		FirstMessage: &request, FirstKind: agentcomm.MessageKindRequest, Sender: stage,
		Recipients: []agentcomm.ParticipantID{stage}, Origin: assistants.OriginStageAgent, Actor: req.Actor,
	})
}

// stageAgentOf is the stage whose agent actor is: ok only for the mesh identity "stage:<id>" of an agent.
func stageAgentOf(actor state.Actor) (stageID string, ok bool) {
	participant := agentcomm.ParticipantID(actor.ID)
	stageID, isStage := stageIDFromMeshParticipant(participant)
	return stageID, isStage && actor == boardTaskActor(participant)
}

// HelperResultView is how a helper stands now: its status and, once it finished with a result, the last result
// the journal accepted. Result is nil while the helper runs and for a helper that failed or was cancelled — the
// StatusReason then says why.
type HelperResultView struct {
	TaskID       assistants.TaskID
	Title        string
	Status       assistants.TaskStatus
	StatusReason string
	Result       json.RawMessage
}

// HelperResult reads the helper taskID that the stage agent actor spawned. A task of another stage, one no stage
// agent spawned and an id that does not exist are all the same NotFound, so the answer does not tell whether a
// task of someone else exists. The result is the task's last COMMITTED one, never result.json, which may hold an
// answer the journal did not accept. The status and the result are read from Store.TaskAndEvents in ONE lock
// acquisition: two separate Store reads could let a finish commit land between them and pair a stale (not yet
// terminal) status with the result it just committed.
func (o *Orchestrator) HelperResult(actor state.Actor, taskID assistants.TaskID) (HelperResultView, error) {
	stageID, ok := stageAgentOf(actor)
	if !ok {
		return HelperResultView{}, attachRejected(assistants.CodeActorNotAllowed, "only a stage agent reads the result of its helper, not %s %q", actor.Kind, actor.ID)
	}
	task, events, found := o.opts.Store.TaskAndEvents(taskID)
	if !found || task.RootStageID != stageID || task.CreatedBy != assistants.OriginStageAgent {
		return HelperResultView{}, &state.OperationRejectedError{Code: assistants.CodeNotFound, Reason: fmt.Sprintf("unknown helper %s", taskID)}
	}
	view := HelperResultView{TaskID: taskID, Title: task.Title, Status: task.Status, StatusReason: task.StatusReason}
	if raw := latestTaskResultJSON(events); raw != "" {
		view.Result = json.RawMessage(raw)
	}
	return view, nil
}

// maxListedHelpers bounds the <your_helpers> block, which repeats on every turn of the stage.
const maxListedHelpers = 20

// stageHelpersBlock renders the <your_helpers> block of stage stageID: the helpers it spawned, oldest first, from
// the durable tasks (id, title and status only) of the stage's CURRENT root generation: a fresh attempt of the
// stage (Revise) invalidates the helpers of the old one. The newest ones are dropped when there are too many.
func stageHelpersBlock(rs state.RunState, stageID string) string {
	generation := rs.Assistants.RootGenerations[stageID]
	var mine []*state.TaskState
	for _, t := range rs.Assistants.Tasks {
		if t.RootStageID == stageID && t.RootGeneration == generation && t.CreatedBy == assistants.OriginStageAgent {
			mine = append(mine, t)
		}
	}
	slices.SortFunc(mine, func(a, b *state.TaskState) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.ID, b.ID))
	})
	omitted := max(0, len(mine)-maxListedHelpers)
	mine = mine[:len(mine)-omitted]
	helpers := make([]prompts.SpawnedHelper, len(mine))
	for i, t := range mine {
		helpers[i] = prompts.SpawnedHelper{TaskID: string(t.ID), Title: t.Title, Status: string(t.Status)}
	}
	return prompts.AgentCommHelpersBlock(helpers, omitted)
}
