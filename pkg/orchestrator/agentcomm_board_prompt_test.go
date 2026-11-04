package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/state"
)

// claimedBoardPrompts is a run where the stage agent, a helper and a run agent each hold an active typed board
// task claimed in team mode; it returns their prompt contexts built over that store in team and in assistants mode.
func claimedBoardPrompts(t *testing.T) (team, boardless map[string]string) {
	t.Helper()
	f := newBoardGateFixture(t)
	runAgent := addTestRunAgent(t, f.team, "prompt-run-agent", nil).ParticipantID
	holders := map[string]agentcomm.ParticipantID{
		"stage":     meshStageParticipantID(assistantsTestStageID),
		"helper":    f.helper,
		"run agent": runAgent,
	}
	for name, p := range holders {
		key := "task/" + strings.ReplaceAll(name, " ", "-")
		mustBoardTask(t, f.team, BoardTaskRequest{OperationID: "create-" + key, Action: BoardTaskCreate, Actor: agentcomm.UserParticipantID, Key: key, Title: new("Held")})
		mustBoardTask(t, f.team, BoardTaskRequest{OperationID: "claim-" + key, Action: BoardTaskClaim, Actor: p, Key: key, ExpectedVersion: 1})
	}
	return agentPromptContexts(t, modeView(t, f, assistants.CollaborationModeTeam), holders),
		agentPromptContexts(t, modeView(t, f, assistants.CollaborationModeAssistants), holders)
}

// modeView is a fresh orchestrator over f's store in mode: f.team's options are read by its running helper, so
// they stay untouched.
func modeView(t *testing.T, f boardGateFixture, mode assistants.CollaborationMode) *Orchestrator {
	t.Helper()
	opts := f.team.opts
	opts.DashboardURL = "http://127.0.0.1:1"
	opts.CollaborationMode = mode
	return assembleAssistantsTestOrchestrator(t, opts)
}

// agentPromptContexts renders what each holder gets from o: the stage agent its <agent_communication> and inbox
// blocks, a helper or run agent its whole task context.
func agentPromptContexts(t *testing.T, o *Orchestrator, holders map[string]agentcomm.ParticipantID) map[string]string {
	t.Helper()
	stage := *o.reg().Stage(assistantsTestStageID)
	contexts := map[string]string{"stage": o.agentCommBlockFor(stage) + o.agentCommInboxBlock(stage)}
	rs := o.opts.Store.Snapshot()
	for name, p := range holders {
		taskID, ok := participantTask(rs, p)
		if !ok {
			continue
		}
		task := rs.Assistants.Tasks[taskID].Task
		var root flow.Stage
		if task.RootStageID != "" {
			root = stage
		}
		tc, err := o.buildTaskContext(task, root)
		if err != nil {
			t.Fatalf("%s: buildTaskContext: %v", name, err)
		}
		contexts[name] = tc.Prompt
	}
	return contexts
}

// Assignments left from team mode are Board content too: in assistants mode no agent sees them or any Board command.
func TestBoardPrompt_AssistantsModeHidesBoardFromEveryAgent(t *testing.T) {
	team, boardless := claimedBoardPrompts(t)
	if len(team) != 3 || len(boardless) != 3 {
		t.Fatalf("contexts: team %d, assistants %d, want the stage, the helper and the run agent in each", len(team), len(boardless))
	}
	for name, prompt := range boardless {
		for _, banned := range []string{"<your_board_tasks>", "afm agent board", "afm agent task", "board_key", "Board task"} {
			if strings.Contains(prompt, banned) {
				t.Errorf("%s context in assistants mode contains %q:\n%s", name, banned, prompt)
			}
		}
		if !strings.Contains(prompt, "The Board is unavailable in this run") {
			t.Errorf("%s context in assistants mode does not say the Board is unavailable:\n%s", name, prompt)
		}
	}
	for name, prompt := range team {
		key := "task/" + strings.ReplaceAll(name, " ", "-")
		for _, want := range []string{"<your_board_tasks>", key, "afm agent task start", "afm agent board get"} {
			if !strings.Contains(prompt, want) {
				t.Errorf("%s context in team mode misses %q:\n%s", name, want, prompt)
			}
		}
		if strings.Contains(prompt, "The Board is unavailable") {
			t.Errorf("%s context in team mode says the Board is unavailable:\n%s", name, prompt)
		}
	}
}

// noticedRun is a team-mode run where the stage agent, a helper, a run agent and the Assistant each received Board
// notices and ordinary messages, all still unread. notices and notes list the message ids each one's context shows
// in team mode: what it received and, for the stage, the requests it sent. noticeIDs are every notice afm sent.
type noticedRun struct {
	f         boardGateFixture
	holders   map[string]agentcomm.ParticipantID
	notices   map[string][]agentcomm.MessageID
	notes     map[string][]agentcomm.MessageID
	noticeIDs map[agentcomm.MessageID]bool
}

func newNoticedRun(t *testing.T) noticedRun {
	t.Helper()
	f := newBoardGateFixture(t)
	stage := meshStageParticipantID(assistantsTestStageID)
	run := addTestRunAgent(t, f.team, "notice-run-agent", nil)
	runAgent := run.ParticipantID
	if _, err := f.team.RegisterSideAgent(context.Background(), RegisterSideAgentRequest{Command: "codex", Actor: state.Actor{Kind: "system"}}); err != nil {
		t.Fatalf("RegisterSideAgent: %v", err)
	}
	f.team.SetSideAgentAvailable(true)
	side := f.team.SideAgentParticipantID()
	r := noticedRun{
		f:         f,
		holders:   map[string]agentcomm.ParticipantID{"stage": stage, "helper": f.helper, "run agent": runAgent, "side agent": side},
		notices:   map[string][]agentcomm.MessageID{},
		notes:     map[string][]agentcomm.MessageID{},
		noticeIDs: map[agentcomm.MessageID]bool{},
	}
	notice := func(receipt BoardTaskReceipt, names ...string) {
		t.Helper()
		if receipt.MessageID == "" {
			t.Fatalf("the change of %s sent no notice", receipt.Key)
		}
		r.noticeIDs[receipt.MessageID] = true
		for _, name := range names {
			r.notices[name] = append(r.notices[name], receipt.MessageID)
		}
	}

	// Assignments by someone else: the user hands tasks to the stage and the run agent, the stage one to the helper.
	notice(mustBoardTask(t, f.team, BoardTaskRequest{OperationID: "assign-stage", Action: BoardTaskCreate, Actor: agentcomm.UserParticipantID,
		Key: "task/stage", Title: new("Stage work"), To: string(stage)}), "stage")
	notice(mustBoardTask(t, f.team, BoardTaskRequest{OperationID: "assign-run-agent", Action: BoardTaskCreate, Actor: agentcomm.UserParticipantID,
		Key: "task/run-agent", Title: new("Run agent work"), To: string(runAgent)}), "run agent")
	toHelper := mustBoardTask(t, f.team, BoardTaskRequest{OperationID: "assign-helper", Action: BoardTaskCreate, Actor: stage,
		Key: "task/helper", Title: new("Helper work"), To: string(f.helper)})
	notice(toHelper, "helper", "stage")

	// The helper finishes its task, and the stage, its coordinator, is told.
	started := mustBoardTask(t, f.team, BoardTaskRequest{OperationID: "start-helper", Action: BoardTaskStart, Actor: f.helper,
		Key: "task/helper", AssignmentID: toHelper.AssignmentID, ExpectedVersion: toHelper.Version})
	notice(mustBoardTask(t, f.team, BoardTaskRequest{OperationID: "done-helper", Action: BoardTaskDone, Actor: f.helper,
		Key: "task/helper", AssignmentID: toHelper.AssignmentID, ExpectedVersion: started.Version, Text: "shipped"}), "stage")

	// A publish that notifies the run agent and the Assistant, one notice each.
	if _, err := f.team.Publish(context.Background(), PublishRequest{OperationID: "publish-notes", Author: f.helper, Key: "notes/api",
		Status: "ready", Notify: []agentcomm.ParticipantID{runAgent, side}, Actor: state.Actor{Kind: "agent", ID: string(f.helper)}}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	mesh := f.store.Snapshot().Assistants.Mesh
	for name, p := range map[string]agentcomm.ParticipantID{"run agent": runAgent, "side agent": side} {
		published := slices.IndexFunc(mesh.Inbox[p], func(id agentcomm.MessageID) bool {
			return mesh.Messages[id].Sender == f.helper && strings.HasPrefix(mesh.Messages[id].Body, "New task on the board")
		})
		if published < 0 {
			t.Fatalf("the publish sent the %s no notice", name)
		}
		r.noticeIDs[mesh.Inbox[p][published]] = true
		r.notices[name] = append(r.notices[name], mesh.Inbox[p][published])
	}

	// Ordinary messages, worded like a notice: only what afm sent for a Board change is left out. The run agent
	// gets several, so its inbox outgrows a page.
	note := func(opID string, from agentcomm.ParticipantID, to string, names ...string) {
		t.Helper()
		sent, err := agentSend(f.team, opID, string(from), []string{string(r.holders[to])}, "Board task wording in a note to the "+to)
		if err != nil {
			t.Fatalf("send %s: %v", opID, err)
		}
		for _, name := range names {
			r.notes[name] = append(r.notes[name], sent.MessageID)
		}
	}
	note("note-stage", f.helper, "stage", "stage")
	note("note-helper", stage, "helper", "helper", "stage")
	note("note-run-agent", stage, "run agent", "run agent", "stage")
	note("note-run-agent-2", stage, "run agent", "run agent", "stage")
	note("note-run-agent-3", stage, "run agent", "run agent", "stage")
	note("note-side-agent", f.helper, "side agent", "side agent")

	// The messages start the run agent's turn; once it is inside the blocked runner the journal stops moving.
	waitForTrue(t, func() bool {
		ts := f.store.Snapshot().Assistants.Tasks[run.TaskID]
		return ts != nil && ts.Status == assistants.StatusRunning && ts.Turn > 0
	}, 2*time.Second, "the run agent did not start its turn")
	return r
}

// replayedView opens a copy of r's journal, the way a resumed run replays it, under an orchestrator in mode.
func (r noticedRun) replayedView(t *testing.T, mode assistants.CollaborationMode) *Orchestrator {
	t.Helper()
	journal, err := os.ReadFile(filepath.Join(r.f.team.opts.RunDir, "events.jsonl"))
	if err != nil {
		t.Fatalf("read the journal: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), journal, 0o644); err != nil {
		t.Fatalf("copy the journal: %v", err)
	}
	store, err := state.Open(dir, r.f.store.Snapshot().StageOrder)
	if err != nil {
		t.Fatalf("state.Open (replay): %v", err)
	}
	t.Cleanup(func() { store.Close() })
	opts := r.f.team.opts
	opts.RunDir = dir
	opts.Store = store
	opts.DashboardURL = "http://127.0.0.1:1"
	opts.CollaborationMode = mode
	return assembleAssistantsTestOrchestrator(t, opts)
}

// A run that used the Board in team mode and goes on in assistants mode: the notices it sent are journal data,
// but no agent's context repeats them or their Board commands; ordinary messages stay as they were.
func TestBoardPrompt_AssistantsModeHidesBoardNotices(t *testing.T) {
	r := newNoticedRun(t)
	team := agentPromptContexts(t, r.replayedView(t, assistants.CollaborationModeTeam), r.holders)
	boardless := agentPromptContexts(t, r.replayedView(t, assistants.CollaborationModeAssistants), r.holders)
	if len(team) != 3 || len(boardless) != 3 {
		t.Fatalf("contexts: team %d, assistants %d, want the stage, the helper and the run agent in each", len(team), len(boardless))
	}

	for name := range team {
		for _, id := range r.notices[name] {
			if !strings.Contains(team[name], string(id)) {
				t.Errorf("%s context in team mode misses Board notice %s:\n%s", name, id, team[name])
			}
			if strings.Contains(boardless[name], string(id)) {
				t.Errorf("%s context in assistants mode shows Board notice %s:\n%s", name, id, boardless[name])
			}
		}
		for _, id := range r.notes[name] {
			if !strings.Contains(team[name], string(id)) || !strings.Contains(boardless[name], string(id)) {
				t.Errorf("%s context misses ordinary message %s (team: %v, assistants: %v)", name, id,
					strings.Contains(team[name], string(id)), strings.Contains(boardless[name], string(id)))
			}
		}
		for _, banned := range []string{"afm agent task start", "afm agent board publish", "assigned to you", "New task on the board", "is done: shipped"} {
			if strings.Contains(boardless[name], banned) {
				t.Errorf("%s context in assistants mode contains %q:\n%s", name, banned, boardless[name])
			}
		}
	}
}
