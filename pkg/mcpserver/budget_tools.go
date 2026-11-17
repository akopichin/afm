package mcpserver

import (
	"context"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/akopichin/afm/contracts/budget"
)

// budgetAdjustInput sets the absolute limit of one scope, so repeating the call is harmless.
type budgetAdjustInput struct {
	RunID            string  `json:"run_id" jsonschema:"the run_id from afm_run_status"`
	Scope            string  `json:"scope" jsonschema:"scope of the held budget, from its pending action"`
	ExpectedRevision int64   `json:"expected_revision" jsonschema:"revision from the pending action"`
	Tokens           uint64  `json:"tokens,omitempty" jsonschema:"new token limit; 0 leaves tokens unlimited"`
	MoneyUSD         float64 `json:"money_usd,omitempty" jsonschema:"new money limit in USD; 0 leaves money unlimited"`
	StopPolicy       string  `json:"stop_policy,omitempty" jsonschema:"hard, medium or soft; default hard"`
	Unlimited        bool    `json:"unlimited,omitempty" jsonschema:"remove the scope's own limit instead of setting one"`
}

// budgetResumeInput continues a held scope without changing its limit.
type budgetResumeInput struct {
	RunID            string `json:"run_id" jsonschema:"the run_id from afm_run_status"`
	Scope            string `json:"scope"`
	ExpectedRevision int64  `json:"expected_revision"`
}

func (in budgetAdjustInput) override() (budget.Override, error) {
	limit := in.Tokens > 0 || in.MoneyUSD > 0
	if in.Unlimited == limit {
		return budget.Override{}, invalidArg("give either unlimited, or tokens/money_usd")
	}
	if in.Unlimited {
		return budget.Override{Kind: budget.OverrideUnlimited}, nil
	}
	spec := budget.Spec{Tokens: in.Tokens, MoneyUSD: in.MoneyUSD, StopPolicy: budget.Policy(in.StopPolicy)}.Normalize()
	override := budget.Override{Kind: budget.OverrideLimit, Spec: spec}
	if err := override.Validate(); err != nil {
		return budget.Override{}, invalidArg(err.Error())
	}
	return override, nil
}

func (s *Service) budgetAdjust(ctx context.Context, in budgetAdjustInput) *sdkmcp.CallToolResult {
	return s.runControl(ctx, in.RunID, "", GroupBudget, func(ctx context.Context) (controlResult, error) {
		if s.cfg.Budgets == nil {
			return controlResult{}, notAllowed("budgets are not available for this run")
		}
		scope, err := budget.ParseScopeID(in.Scope)
		if err != nil {
			return controlResult{}, invalidArg(err.Error())
		}
		override, err := in.override()
		if err != nil {
			return controlResult{}, err
		}
		// Resume stays false: lifting the limit and releasing the held work are two decisions.
		res, err := s.cfg.Budgets.Update(ctx, budget.UpdateRequest{Scope: scope, ExpectedRevision: in.ExpectedRevision, Override: override})
		if err != nil {
			return controlResult{}, err
		}
		return controlResult{Applied: toolActionBudgetSet, Budget: res.View}, nil
	})
}

func (s *Service) budgetResume(ctx context.Context, in budgetResumeInput) *sdkmcp.CallToolResult {
	return s.runControl(ctx, in.RunID, "", GroupBudget, func(ctx context.Context) (controlResult, error) {
		if s.cfg.Budgets == nil {
			return controlResult{}, notAllowed("budgets are not available for this run")
		}
		scope, err := budget.ParseScopeID(in.Scope)
		if err != nil {
			return controlResult{}, invalidArg(err.Error())
		}
		view, err := s.cfg.Budgets.Resume(ctx, budget.ResumeRequest{Scope: scope, ExpectedRevision: in.ExpectedRevision})
		if err != nil {
			return controlResult{}, err
		}
		return controlResult{Applied: toolActionBudgetCont, Budget: view}, nil
	})
}

func (s *Service) registerBudgetTools() {
	addTool(s, toolBudgetAdjust, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in budgetAdjustInput) (*sdkmcp.CallToolResult, any, error) {
		return s.budgetAdjust(ctx, in), nil, nil
	})
	addTool(s, toolBudgetResume, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in budgetResumeInput) (*sdkmcp.CallToolResult, any, error) {
		return s.budgetResume(ctx, in), nil, nil
	})
}
