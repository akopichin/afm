package orchestrator

import (
	"fmt"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/flow"
)

func TestBoardTaskPrompt_ParticipantSeesItsAssignmentID(t *testing.T) {
	o, _ := newBoardTaskTestOrchestrator(t)
	const key = "task/login"
	mustBoardTask(t, o, BoardTaskRequest{
		OperationID: "create-p", Action: BoardTaskCreate, Actor: agentcomm.UserParticipantID, Key: key, Title: "Login form", To: boardTaskTestBackend,
	})
	assignmentID := o.opts.Store.Snapshot().Assistants.Mesh.BoardTasks[key].Assignments[0].ID

	block := o.agentCommInboxBlock(flow.Stage{ID: boardTaskTestBackend})
	for _, want := range []string{"<your_board_tasks>", key, "Login form", assignmentID} {
		if !strings.Contains(block, want) {
			t.Errorf("backend's prompt context misses %q:\n%s", want, block)
		}
	}
	if other := o.agentCommInboxBlock(flow.Stage{ID: boardTaskTestFrontend}); strings.Contains(other, "<your_board_tasks>") {
		t.Errorf("frontend has no assignment but sees a task block:\n%s", other)
	}
}

func TestBoardTaskPrompt_ListsAtMostTwentyTasks(t *testing.T) {
	o, _ := newBoardTaskTestOrchestrator(t)
	for i := range 23 {
		mustBoardTask(t, o, BoardTaskRequest{
			OperationID: fmt.Sprintf("create-%d", i), Action: BoardTaskCreate, Actor: agentcomm.UserParticipantID,
			Key: fmt.Sprintf("task/%02d", i), Title: "T", To: boardTaskTestBackend,
		})
	}
	block := o.agentCommInboxBlock(flow.Stage{ID: boardTaskTestBackend})
	if got := strings.Count(block, "\n- task/"); got != 20 {
		t.Errorf("listed %d tasks, want 20:\n%s", got, block)
	}
	if !strings.Contains(block, "… and 3 more — afm agent task list") {
		t.Errorf("block does not count the omitted tasks:\n%s", block)
	}
}

func TestBoardTaskPrompt_LegacyRunHasNoTaskBlockAndLegacyInstructions(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	if block := o.agentCommInboxBlock(flow.Stage{ID: "any"}); strings.Contains(block, "<your_board_tasks>") {
		t.Errorf("legacy run got a task block:\n%s", block)
	}
	o.opts.DashboardURL = "http://127.0.0.1:1"
	if block := o.agentCommBlockFor(flow.Stage{ID: "any"}); !strings.Contains(block, "in progress: <progress>") || strings.Contains(block, "afm agent task start") {
		t.Errorf("legacy run got typed instructions:\n%s", block)
	}
}

func TestBoardTaskPrompt_TypedRunGetsTypedInstructions(t *testing.T) {
	o, _ := newBoardTaskTestOrchestrator(t)
	o.opts.DashboardURL = "http://127.0.0.1:1"
	for name, block := range map[string]string{
		"stage":  o.agentCommBlockFor(flow.Stage{ID: boardTaskTestBackend}),
		"helper": o.agentCommBlockForTask("missing-task"),
	} {
		if !strings.Contains(block, "afm agent task start") || strings.Contains(block, "in progress:") {
			t.Errorf("%s block of a typed run lacks the typed instructions:\n%s", name, block)
		}
	}
}
