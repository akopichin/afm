package mcpserver

import (
	"context"
	"slices"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

var readTools = []string{"afm_artifact_read", "afm_events_read", "afm_run_status", "afm_run_wait"}

func TestToolsListFollowsGroups(t *testing.T) {
	cases := []struct {
		groups string
		extra  []string
	}{
		{"", nil},
		{"reply", []string{"afm_interaction_reply"}},
		{"stages", []string{"afm_stage_continue", "afm_stage_decide", "afm_stage_pause"}},
		{"approve", []string{"afm_stage_decide"}},
		{"approve,stages", []string{"afm_stage_continue", "afm_stage_decide", "afm_stage_pause"}},
		{"budget", []string{"afm_budget_adjust", "afm_budget_resume"}},
		{"reply,stages,approve,budget", []string{
			"afm_budget_adjust", "afm_budget_resume", "afm_interaction_reply", "afm_stage_continue", "afm_stage_decide", "afm_stage_pause",
		}},
	}
	for _, tc := range cases {
		t.Run("groups="+tc.groups, func(t *testing.T) {
			h := newHarness(t, tc.groups)
			got := toolNames(t, h.connect(t, nil))
			slices.Sort(got)
			want := slices.Sorted(slices.Values(slices.Concat(readTools, tc.extra)))
			if !slices.Equal(got, want) {
				t.Fatalf("tools = %v, want %v", got, want)
			}
		})
	}
}

func TestToolsGroupsCannotBeCalledWhenAbsent(t *testing.T) {
	h := newHarness(t, "")
	session := h.connect(t, nil)
	_, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name: "afm_stage_decide", Arguments: map[string]any{"run_id": "run-1", "stage_id": "s", "decision": "retry"},
	})
	if err == nil {
		t.Fatal("a tool outside the enabled groups must not exist")
	}
	if len(h.cmds.callList()) != 0 {
		t.Fatal("engine reached")
	}
}

func TestToolsControlOverMCPReturnsPostState(t *testing.T) {
	h := newHarness(t, "reply,stages,approve")
	awaitingPlan(h, "# plan")
	session := h.connect(t, nil)

	res, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name: "afm_stage_decide",
		Arguments: map[string]any{
			"run_id": "run-1", "stage_id": "plan", "decision": "approve", "expected_revision": "rev-1",
		},
	})
	if err != nil || res.IsError {
		t.Fatalf("call: %v %+v", err, res)
	}
	var got controlResult
	decode(t, res, &got)
	if got.ApprovedRevision != "rev-1" || got.State == nil || got.State.StageID != "plan" {
		t.Fatalf("result = %+v", got)
	}

	res, err = session.CallTool(context.Background(), &sdkmcp.CallToolParams{
		Name: "afm_stage_decide", Arguments: map[string]any{"run_id": "run-2", "stage_id": "plan", "decision": "approve", "expected_revision": "rev-1"},
	})
	if err != nil || errCode(t, res) != codeRunChanged {
		t.Fatalf("run mismatch: %v %+v", err, res)
	}
	if n := h.approval.approveCount(); n != 1 {
		t.Fatalf("ApproveExpected called %d times", n)
	}
}
