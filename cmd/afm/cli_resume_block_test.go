package main

import (
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/state"
)

// TestCli_DisabledFeatureExplainsResumeBlock — T11.5: hitting `afm retry`/`afm revise`/`afm approve` against
// a stage parked in awaiting_assistants (an incomplete required helper-task branch) must explain what to do
// — name the blocking task and point at waiving it or re-enabling collaboration — instead of the bare,
// generic "stage %q is %v, not failed/awaiting_approval" every other status mismatch gets.
func TestCli_DisabledFeatureExplainsResumeBlock(t *testing.T) {
	chdirTemp(t)
	runDir := makeRunState(t, "flow-20260101-120000", "impl", state.StatusAwaitingAssistants)

	appendControlLine(t, runDir, 2, state.Control{
		Kind:   "task_created",
		Actor:  state.Actor{Kind: "user"},
		Target: state.Target{TaskID: "sec-1", StageID: "impl"},
		Payload: mustJSON(t, state.TaskCreatedPayload{
			ProfileID: "security-reviewer",
			Title:     "Security pass on the new endpoint",
			Mode:      "required",
			CreatedBy: "user",
		}),
	})
	appendControlLine(t, runDir, 3, state.Control{
		Kind:    "task_status_changed",
		Target:  state.Target{TaskID: "sec-1"},
		Payload: mustJSON(t, state.TaskStatusChangedPayload{Status: "failed", Reason: "helper process crashed"}),
	})

	assertExplains := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			t.Fatal("expected an error — the stage is not in the status this command requires")
		}
		msg := err.Error()
		if strings.Contains(msg, "not failed") || strings.Contains(msg, "not awaiting_approval") {
			t.Fatalf("got the bare generic status-mismatch message, want an explanation naming the blocker: %q", msg)
		}
		if !strings.Contains(msg, "sec-1") {
			t.Fatalf("error does not name the blocking task: %q", msg)
		}
		if !strings.Contains(msg, "waive") {
			t.Fatalf("error does not explain the waive option: %q", msg)
		}
		if !strings.Contains(msg, "collaboration") {
			t.Fatalf("error does not mention re-enabling collaboration: %q", msg)
		}
	}

	t.Run("retry", func(t *testing.T) {
		cmd := newRetryCmd()
		cmd.SetArgs([]string{"impl"})
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		assertExplains(t, cmd.Execute())
	})
	t.Run("approve", func(t *testing.T) {
		cmd := newApproveCmd()
		cmd.SetArgs([]string{"impl"})
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		assertExplains(t, cmd.Execute())
	})
	t.Run("revise", func(t *testing.T) {
		cmd := newReviseCmd()
		cmd.SetArgs([]string{"impl", "--feedback", "please fix"})
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		assertExplains(t, cmd.Execute())
	})
}
