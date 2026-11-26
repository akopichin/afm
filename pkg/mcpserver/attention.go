package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"

	"github.com/akopichin/afm/contracts/budget"
	"github.com/akopichin/afm/contracts/control"
	"github.com/akopichin/afm/contracts/mcp"
	"github.com/akopichin/afm/contracts/state"
)

// Pending action types; each one is a distinct reason the run waits for a person, so clients never have to
// guess a cause from a bare status.
const (
	actionQuestion       = "question"
	actionApproval       = "approval"
	actionFailed         = "failed"
	actionHookFailed     = "hook_failed"
	actionPaused         = "paused"
	actionQuota          = "quota"
	actionAssistants     = "assistants_blocked"
	actionBudgetHold     = "budget_hold"
	reasonPreCompletion  = "pre_completion"
	toolActionReply      = "reply"
	toolActionApprove    = "approve"
	toolActionRevise     = "revise"
	toolActionRetry      = "retry"
	toolActionContinue   = "continue"
	toolActionPause      = "pause"
	toolActionBudgetSet  = "budget_adjust"
	toolActionBudgetCont = "budget_resume"
)

// pendingAction is one thing the run is waiting on. ID is stable while the cause stays the same, so a client can
// tell a new request from one it has already seen.
type pendingAction struct {
	ID             string   `json:"id"`
	Type           string   `json:"type"`
	StageID        string   `json:"stage_id,omitempty"`
	Scope          string   `json:"scope,omitempty"`
	Revision       string   `json:"revision,omitempty"`
	Phase          string   `json:"phase,omitempty"`
	QuestionID     string   `json:"question_id,omitempty"`
	Question       string   `json:"question,omitempty"`
	Options        []string `json:"options,omitempty"`
	AllowCustom    *bool    `json:"allow_custom,omitempty"`
	Reason         string   `json:"reason,omitempty"`
	AllowedActions []string `json:"allowed_actions"`
	// Resources lists MCP resource URIs worth reading before deciding.
	Resources []string `json:"resources,omitempty"`
}

// attentionModel is the compact "what waits for a decision" view of the run. It carries no progress percentage:
// stages differ too much in size for one to mean anything.
type attentionModel struct {
	RunStatus         state.RunStatus `json:"run_status"`
	AttentionRequired bool            `json:"attention_required"`
	PendingActions    []pendingAction `json:"pending_actions"`
	Counts            map[string]int  `json:"counts"`
	ActiveStages      []string        `json:"active_stages"`
}

// activeStatuses are the stages that are doing work right now.
var activeStatuses = []state.StageStatus{
	state.StatusPlanning, state.StatusRunning, state.StatusRevising, state.StatusRetrying,
}

// buildAttention derives the model from the run state, the dialog files, the plan snapshots and the budgets.
func (s *Service) buildAttention(ctx context.Context, rs state.RunState) attentionModel {
	model := attentionModel{
		RunStatus:      rs.RunStatus,
		PendingActions: []pendingAction{},
		Counts:         map[string]int{},
		ActiveStages:   []string{},
	}
	ids := slices.Clone(rs.StageOrder)
	for id := range rs.Stages {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		st := rs.Stages[id]
		model.Counts[string(st.Status)]++
		if slices.Contains(activeStatuses, st.Status) {
			model.ActiveStages = append(model.ActiveStages, id)
		}
		if action, ok := s.stageAction(ctx, id, st, rs); ok {
			model.PendingActions = append(model.PendingActions, action)
		}
	}
	model.PendingActions = append(model.PendingActions, s.budgetActions(ctx)...)
	model.AttentionRequired = len(model.PendingActions) > 0
	return model
}

// stageAction maps one stage to the decision it waits for, if any.
func (s *Service) stageAction(ctx context.Context, id string, st state.StageState, rs state.RunState) (pendingAction, bool) {
	action := pendingAction{StageID: id, AllowedActions: []string{}}
	switch st.Status {
	case state.StatusAwaitingUserInput:
		q, ok, err := mcp.CurrentQuestion(filepath.Join(s.cfg.RunDir, id))
		if err != nil || !ok || q.Malformed {
			// The question file is not readable yet; the dialog poller notices it on a later tick.
			return pendingAction{}, false
		}
		allowCustom := q.AllowCustom
		action.Type = actionQuestion
		action.Phase, action.QuestionID, action.Question = q.Phase, q.ID, s.redact(q.Question)
		action.Options, action.AllowCustom = s.redactAll(q.Options), &allowCustom
		action.ID = id + ":" + actionQuestion + ":" + q.Phase + ":" + q.ID
		s.allow(&action, GroupReply, toolActionReply)
	case state.StatusAwaitingApproval:
		action.Type = actionApproval
		s.approvalAction(ctx, &action)
	case state.StatusFailed:
		action.Type = actionFailed
		s.allow(&action, GroupStages, toolActionRetry)
	case state.StatusHookFailed:
		action.Type = actionHookFailed
	case state.StatusPaused:
		action.Type = actionPaused
		action.Reason = string(st.PausedFrom)
		s.allow(&action, GroupStages, toolActionContinue)
	case state.StatusAwaitingQuota:
		action.Type = actionQuota
		if st.Quota != nil {
			action.Reason = st.Quota.PauseReason
		}
		s.allow(&action, GroupStages, toolActionPause)
	case state.StatusAwaitingAssistants:
		if !rs.Assistants.NeedsHumanDecision(id) {
			return pendingAction{}, false
		}
		action.Type = actionAssistants
	case state.StatusAwaitingPreCompletion:
		// Only a review nobody can settle by waiting is the person's decision; a running review is machine work.
		if st.PreCompletion == nil || st.PreCompletion.Decision != state.PreCompletionNeedsDecision {
			return pendingAction{}, false
		}
		action.Type = actionAssistants
		action.Reason = reasonPreCompletion
	default:
		return pendingAction{}, false
	}
	if action.ID == "" {
		action.ID = id + ":" + action.Type
	}
	return action, true
}

// approvalAction fills the approval action from the engine's plan snapshot. Without a consistent snapshot
// approve is not offered: there is no fallback to the legacy approve that takes no revision.
func (s *Service) approvalAction(ctx context.Context, action *pendingAction) {
	s.allow(action, GroupStages, toolActionRevise)
	if s.cfg.Approval == nil {
		action.Reason = control.CodeApprovalUnavailable
		return
	}
	_, revision, err := s.reviewedPlan(ctx, action.StageID)
	if err != nil {
		action.Reason = toolError(err).Code
		return
	}
	action.Revision = revision
	action.ID = action.StageID + ":" + actionApproval + ":" + action.Revision
	action.Resources = []string{planURI(s.runID, action.StageID, action.Revision)}
	s.allow(action, GroupApprove, toolActionApprove)
}

// budgetActions lists the scopes that are held by a budget limit.
func (s *Service) budgetActions(ctx context.Context) []pendingAction {
	if s.cfg.Budgets == nil {
		return nil
	}
	views, err := s.cfg.Budgets.List(ctx)
	if err != nil {
		return nil
	}
	var out []pendingAction
	for _, v := range views {
		if v.Hold == nil {
			continue
		}
		action := pendingAction{
			ID:             "budget:" + string(v.Scope) + ":" + string(v.Hold.Reason),
			Type:           actionBudgetHold,
			Scope:          string(v.Scope),
			Revision:       revisionString(v.Revision),
			Reason:         string(v.Hold.Reason),
			AllowedActions: []string{},
		}
		if v.Scope != budget.RunScope() {
			action.StageID = strings.TrimPrefix(string(v.Scope), string(budget.ScopeStage)+":")
			if action.StageID == string(v.Scope) {
				action.StageID = ""
			}
		}
		s.allow(&action, GroupBudget, toolActionBudgetSet, toolActionBudgetCont)
		out = append(out, action)
	}
	return out
}

// allow adds tool actions to the action when the group that owns them is enabled.
func (s *Service) allow(action *pendingAction, group Group, names ...string) {
	if s.cfg.Groups.Has(group) {
		action.AllowedActions = append(action.AllowedActions, names...)
	}
}

// cursorOf is the wait cursor of a model: "v1:<run_id>:<digest>". The digest changes whenever the model does and
// not otherwise, so equal cursors mean nothing needs attention that did not before.
func (s *Service) cursorOf(model attentionModel) string {
	raw, _ := json.Marshal(model) // plain data; cannot fail
	sum := sha256.Sum256(raw)
	return cursorPrefix + s.runID + ":" + hex.EncodeToString(sum[:8])
}

const (
	cursorPrefix = "v1:"
	// cursorDigestLen is the hex length of the 8-byte digest in a cursor.
	cursorDigestLen = 16
)

// cursorBelongs tells whether cursor is well formed, exactly as cursorOf builds it, and was issued for this run.
func (s *Service) cursorBelongs(cursor string) bool {
	digest, ok := strings.CutPrefix(cursor, cursorPrefix+s.runID+":")
	if !ok || len(digest) != cursorDigestLen {
		return false
	}
	for _, c := range digest {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
