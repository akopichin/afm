package mcpserver

import (
	"context"
	"path/filepath"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/akopichin/afm/contracts/control"
	"github.com/akopichin/afm/contracts/mcp"
	"github.com/akopichin/afm/pkg/server"
)

const (
	decisionApprove = "approve"
	decisionRevise  = "revise"
	decisionRetry   = "retry"

	codeQuestionNotPending = "question_not_pending"
)

// postState is where the run stands after a control call, whether it applied or not: the client decides its
// next step from it without another read.
type postState struct {
	StageID     string `json:"stage_id,omitempty"`
	StageStatus string `json:"stage_status,omitempty"`
	RunStatus   string `json:"run_status"`
	Cursor      string `json:"cursor"`
}

func (s *Service) postState(ctx context.Context, stageID string) *postState {
	rs := s.cfg.Reader.Snapshot()
	return &postState{
		StageID:     stageID,
		StageStatus: string(rs.Stages[stageID].Status),
		RunStatus:   string(rs.RunStatus),
		Cursor:      s.cursorOf(s.buildAttention(ctx, rs)),
	}
}

// controlResult is the success body of every control tool.
type controlResult struct {
	Applied string     `json:"applied"`
	State   *postState `json:"state"`
	// ApprovedRevision and TransitionSeq are set by an applied approve.
	ApprovedRevision string `json:"approved_revision,omitempty"`
	TransitionSeq    uint64 `json:"transition_seq,omitempty"`
	// Budget is set by the budget tools: the scope's view after the change.
	Budget any `json:"budget,omitempty"`
}

func notAllowed(message string) *ToolError {
	return &ToolError{Code: codeNotAllowed, Kind: "forbidden", Message: message}
}

func invalidArg(message string) *ToolError {
	return &ToolError{Code: codeInvalid, Kind: codeInvalid, Message: message}
}

// runControl is the shared envelope of the control tools: the run and permission checks come first, so a rejected
// call reaches the engine zero times. do runs on a context the request cannot cancel, because the work it starts
// belongs to the run and not to this connection.
func (s *Service) runControl(ctx context.Context, runID, stageID string, group Group, do func(ctx context.Context) (controlResult, error)) *sdkmcp.CallToolResult {
	release, err := s.enter()
	if err != nil {
		return errorResult(err)
	}
	defer release()
	// Another run's state is meaningless here, so this rejection reads nothing from the collaborators.
	if err := s.checkRun(runID); err != nil {
		return errorResult(err)
	}
	if !s.cfg.Groups.Has(group) {
		return s.controlError(ctx, stageID, notAllowed("the MCP permission group "+string(group)+" is not enabled"))
	}
	res, err := do(context.WithoutCancel(ctx))
	if err != nil {
		return s.controlError(ctx, stageID, err)
	}
	res.State = s.postState(ctx, stageID)
	return okResult(res)
}

func (s *Service) controlError(ctx context.Context, stageID string, err error) *sdkmcp.CallToolResult {
	te := *toolError(err)
	te.State = s.postState(ctx, stageID)
	return errorResult(&te)
}

// stageGuard rejects a stage id that could escape the run directory.
func stageGuard(stageID string) error {
	if !validStage(stageID) {
		return invalidArg("invalid stage_id")
	}
	return nil
}

// replyInput answers one question of one stage.
type replyInput struct {
	RunID       string `json:"run_id" jsonschema:"the run_id from afm_run_status"`
	StageID     string `json:"stage_id"`
	Phase       string `json:"phase" jsonschema:"phase of the question, from its pending action"`
	QuestionID  string `json:"question_id"`
	Answer      string `json:"answer"`
	FromOptions bool   `json:"from_options,omitempty" jsonschema:"true when answer is one of the offered options"`
}

func (s *Service) reply(ctx context.Context, in replyInput) *sdkmcp.CallToolResult {
	return s.runControl(ctx, in.RunID, in.StageID, GroupReply, func(ctx context.Context) (controlResult, error) {
		if s.cfg.Commands == nil {
			return controlResult{}, notAllowed("this endpoint cannot answer questions")
		}
		if err := stageGuard(in.StageID); err != nil {
			return controlResult{}, err
		}
		cur, ok, err := mcp.CurrentQuestion(filepath.Join(s.cfg.RunDir, in.StageID))
		if err != nil || !ok || cur.Phase != in.Phase || cur.ID != in.QuestionID {
			return controlResult{}, &ToolError{Code: codeQuestionNotPending, Kind: codeConflict, Message: "that question is not waiting for an answer"}
		}
		if err := s.cfg.Commands.Answer(in.StageID, in.Phase, in.QuestionID, in.Answer, in.FromOptions); err != nil {
			return controlResult{}, err
		}
		return controlResult{Applied: toolActionReply}, nil
	})
}

// decideInput approves, revises or retries one stage.
type decideInput struct {
	RunID            string `json:"run_id" jsonschema:"the run_id from afm_run_status"`
	StageID          string `json:"stage_id"`
	Decision         string `json:"decision" jsonschema:"approve, revise or retry"`
	ExpectedRevision string `json:"expected_revision,omitempty" jsonschema:"approve only: the plan revision you read"`
	Feedback         string `json:"feedback,omitempty" jsonschema:"revise only: what to change in the plan"`
}

// decide routes the decision to its group: approve needs approve, revise and retry need stages.
func (s *Service) decide(ctx context.Context, in decideInput) *sdkmcp.CallToolResult {
	group := GroupStages
	if in.Decision == decisionApprove {
		group = GroupApprove
	}
	return s.runControl(ctx, in.RunID, in.StageID, group, func(ctx context.Context) (controlResult, error) {
		if err := stageGuard(in.StageID); err != nil {
			return controlResult{}, err
		}
		switch in.Decision {
		case decisionApprove:
			return s.approve(ctx, in)
		case decisionRevise:
			return s.revise(ctx, in)
		case decisionRetry:
			if s.cfg.Commands == nil {
				return controlResult{}, notAllowed("this endpoint cannot control stages")
			}
			return controlResult{Applied: decisionRetry}, s.cfg.Commands.Retry(ctx, in.StageID)
		}
		return controlResult{}, invalidArg("decision must be approve, revise or retry")
	})
}

// approve applies only the revision the client read, and only while that is the plan afm_run_status would offer
// for approval: a caller may skip allowed_actions. The legacy approve is never reachable from here.
func (s *Service) approve(ctx context.Context, in decideInput) (controlResult, error) {
	if s.cfg.Approval == nil {
		return controlResult{}, &ToolError{Code: control.CodeApprovalUnavailable, Kind: codeConflict, Message: "conditional approval is not available for this run"}
	}
	if in.ExpectedRevision == "" {
		return controlResult{}, invalidArg("expected_revision is required to approve")
	}
	req := control.ApproveExpectedRequest{RunID: s.runID, StageID: in.StageID, ExpectedRevision: control.ApprovalRevision(in.ExpectedRevision)}
	snap, err := server.ApprovalPreflight(ctx, s.cfg.Approval, req)
	if err != nil {
		return controlResult{}, err
	}
	if _, err := s.confinedPlan(snap); err != nil {
		return controlResult{}, err
	}
	res, err := s.cfg.Approval.ApproveExpected(ctx, req)
	if err != nil {
		return controlResult{}, err
	}
	return controlResult{Applied: decisionApprove, ApprovedRevision: string(res.ApprovedRevision), TransitionSeq: res.TransitionSeq}, nil
}

func (s *Service) revise(ctx context.Context, in decideInput) (controlResult, error) {
	if s.cfg.Commands == nil {
		return controlResult{}, notAllowed("this endpoint cannot control stages")
	}
	if in.Feedback == "" {
		return controlResult{}, invalidArg("feedback is required to revise")
	}
	if err := s.cfg.Commands.CheckRevise(in.StageID); err != nil {
		return controlResult{}, err
	}
	return controlResult{Applied: decisionRevise}, s.cfg.Commands.Revise(ctx, in.StageID, in.Feedback)
}

// stageInput names one stage.
type stageInput struct {
	RunID   string `json:"run_id" jsonschema:"the run_id from afm_run_status"`
	StageID string `json:"stage_id"`
}

func (s *Service) pauseOrContinue(ctx context.Context, in stageInput, name string) *sdkmcp.CallToolResult {
	return s.runControl(ctx, in.RunID, in.StageID, GroupStages, func(ctx context.Context) (controlResult, error) {
		if s.cfg.Commands == nil {
			return controlResult{}, notAllowed("this endpoint cannot control stages")
		}
		if err := stageGuard(in.StageID); err != nil {
			return controlResult{}, err
		}
		if name == toolActionPause {
			return controlResult{Applied: name}, s.cfg.Commands.Pause(ctx, in.StageID)
		}
		return controlResult{Applied: name}, s.cfg.Commands.Continue(ctx, in.StageID)
	})
}

func (s *Service) registerControlTools() {
	addTool(s, toolInteractionReply, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in replyInput) (*sdkmcp.CallToolResult, any, error) {
		return s.reply(ctx, in), nil, nil
	})
	addTool(s, toolStageDecide, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in decideInput) (*sdkmcp.CallToolResult, any, error) {
		return s.decide(ctx, in), nil, nil
	})
	for _, name := range []string{toolActionPause, toolActionContinue} {
		addTool(s, "afm_stage_"+name, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in stageInput) (*sdkmcp.CallToolResult, any, error) {
			return s.pauseOrContinue(ctx, in, name), nil, nil
		})
	}
	s.registerBudgetTools()
}

func ptr[T any](v T) *T { return &v }
