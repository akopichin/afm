package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/server"
	"github.com/akopichin/afm/pkg/sideagent"
	"github.com/akopichin/afm/pkg/state"
)

const boardUnavailableMessage = "Board is unavailable in assistants mode"

// boardGateRun is a real orchestrator with a live stage agent behind a real HTTP server that exposes both the
// dashboard and the agent Board routes, in the given collaboration mode.
type boardGateRun struct {
	agent liveStageAgent
	srv   *httptest.Server
	opts  orchestrator.Options
}

func startBoardGateRun(t *testing.T, mode assistants.CollaborationMode) boardGateRun {
	t.Helper()
	var runOpts orchestrator.Options
	agent := startLiveStageAgentWith(t, func(opts *orchestrator.Options) {
		opts.CollaborationMode = mode
		if err := opts.Store.RecordBoardTaskProtocol(agentcomm.BoardTaskProtocolTypedV1, state.Actor{Kind: "system", ID: "afm"}); err != nil {
			t.Fatalf("RecordBoardTaskProtocol: %v", err)
		}
		runOpts = *opts
	})
	srv := httptest.NewServer(server.New(server.Config{
		AgentMesh: agentMeshActions{agent.orch}, Mesh: meshActions{agent.orch}, CollaborationMode: mode,
	}).Handler())
	t.Cleanup(srv.Close)
	return boardGateRun{agent: agent, srv: srv, opts: runOpts}
}

// frozenBoardState is the journal position and mesh projection a refused Board request must leave as it was.
func (r boardGateRun) frozenBoardState(t *testing.T) string {
	t.Helper()
	rs := r.agent.store.Snapshot()
	raw, err := json.Marshal(struct {
		LastSeq    uint64
		Assistants state.AssistantsState
	}{rs.LastSeq, rs.Assistants})
	if err != nil {
		t.Fatalf("marshal run state: %v", err)
	}
	return string(raw)
}

// boardRoute is one HTTP route of the Board, with the request body it takes.
type boardRoute struct {
	name, method, path string
	body               map[string]any
	dashboard          bool // a dashboard mutation: same-origin, no bearer token
}

func boardRoutes() []boardRoute {
	deadline := int64(4102444800) // a far-future unix time: the wait is valid, so only the mode can refuse it
	return []boardRoute{
		{name: "dashboard board read", method: http.MethodGet, path: "/api/mesh/board", dashboard: true},
		{name: "dashboard board publish", method: http.MethodPost, path: "/api/mesh/board", dashboard: true,
			body: map[string]any{"request_id": "gate-ui-pub", "key": "notes", "status": "ready"}},
		{name: "dashboard task change", method: http.MethodPost, path: "/api/mesh/tasks", dashboard: true,
			body: map[string]any{"request_id": "gate-ui-task", "action": "create", "key": "task/gate", "title": "Gate"}},
		{name: "agent board read", method: http.MethodGet, path: "/api/agent-mesh/board"},
		{name: "agent board publish", method: http.MethodPost, path: "/api/agent-mesh/publish",
			body: map[string]any{"request_id": "gate-agent-pub", "key": "notes", "status": "ready"}},
		{name: "agent tasks read", method: http.MethodGet, path: "/api/agent-mesh/tasks"},
		{name: "agent task change", method: http.MethodPost, path: "/api/agent-mesh/task",
			body: map[string]any{"request_id": "gate-agent-task", "action": "create", "key": "task/gate", "title": "Gate"}},
		{name: "agent board_key wait", method: http.MethodPost, path: "/api/agent-mesh/wait",
			body: map[string]any{
				"request_id": "gate-agent-wait", "deadline_unix": deadline,
				"condition": map[string]any{"kind": "board_key", "key": "notes", "status": "ready"},
			}},
	}
}

// do sends route to the run's server; token == "" sends no bearer token, origin == "" no Origin header.
func (r boardGateRun) do(t *testing.T, route boardRoute, token, origin string) (int, map[string]any) {
	t.Helper()
	var body io.Reader
	if route.body != nil {
		raw, err := json.Marshal(route.body)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(route.method, r.srv.URL+route.path, body)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var decoded map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&decoded)
	return resp.StatusCode, decoded
}

// credentials is what a legitimate caller of route presents: the dashboard its own origin, an agent its token.
func (r boardGateRun) credentials(route boardRoute) (token, origin string) {
	if route.dashboard {
		return "", r.srv.URL
	}
	return r.agent.token, ""
}

func TestBoardRoutesRefusedInAssistantsMode(t *testing.T) {
	run := startBoardGateRun(t, assistants.CollaborationModeAssistants)
	before := run.frozenBoardState(t)
	for _, route := range boardRoutes() {
		t.Run(route.name, func(t *testing.T) {
			token, origin := run.credentials(route)
			status, body := run.do(t, route, token, origin)
			want := map[string]any{"error": "feature_disabled", "message": boardUnavailableMessage}
			if status != http.StatusNotFound || !mapsEqualJSON(body, want) {
				t.Fatalf("%s %s = HTTP %d %v, want 404 %v", route.method, route.path, status, body, want)
			}
		})
	}
	if after := run.frozenBoardState(t); after != before {
		t.Fatalf("refused Board requests changed the run state (%d bytes before, %d after)", len(before), len(after))
	}
}

func mapsEqualJSON(a, b map[string]any) bool {
	rawA, errA := json.Marshal(a)
	rawB, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(rawA, rawB)
}

// Authentication and the origin check run before the mode check, so they answer the same in both modes.
func TestBoardRoutesKeepAuthenticationInAssistantsMode(t *testing.T) {
	run := startBoardGateRun(t, assistants.CollaborationModeAssistants)
	for _, route := range boardRoutes() {
		t.Run(route.name, func(t *testing.T) {
			if route.dashboard {
				if route.method == http.MethodPost {
					if status, _ := run.do(t, route, "", "http://evil.example"); status != http.StatusForbidden {
						t.Fatalf("foreign Origin: HTTP %d, want 403", status)
					}
				}
				return
			}
			if status, _ := run.do(t, route, "", ""); status != http.StatusUnauthorized {
				t.Fatalf("no token: HTTP %d, want 401", status)
			}
			if status, _ := run.do(t, route, "not-a-token", ""); status != http.StatusUnauthorized {
				t.Fatalf("unknown token: HTTP %d, want 401", status)
			}
		})
	}
}

// runBoardCLI runs `afm agent <args>` as the stage agent of run against its real server.
func runBoardCLI(t *testing.T, run boardGateRun, args ...string) (stdout, stderr map[string]any, err error) {
	t.Helper()
	writeTestMeshDescriptor(t, run.srv, run.agent.token, "stage:impl")
	cmd := newAgentCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err = cmd.Execute()
	decode := func(buf *bytes.Buffer) map[string]any {
		if buf.Len() == 0 {
			return nil
		}
		var m map[string]any
		if jerr := json.Unmarshal(buf.Bytes(), &m); jerr != nil {
			t.Fatalf("output must be one JSON object, got %q: %v", buf.String(), jerr)
		}
		return m
	}
	return decode(&out), decode(&errOut), err
}

func TestAgentBoardCLIRefusedInAssistantsMode(t *testing.T) {
	run := startBoardGateRun(t, assistants.CollaborationModeAssistants)
	before := run.frozenBoardState(t)
	cases := map[string][]string{
		"board get":     {"board", "get"},
		"board publish": {"board", "publish", "--key", "notes", "--status", "ready"},
		"task list":     {"task", "list"},
		"task create":   {"task", "create", "--key", "task/gate", "--title", "Gate"},
		"wait board_key": {
			"wait", "--condition", "board_key", "--key", "notes", "--status", "ready", "--deadline-seconds", "600",
		},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			stdout, stderr, err := runBoardCLI(t, run, args...)
			var exit *ExitError
			if !errors.As(err, &exit) || exit.Code != 1 {
				t.Fatalf("error = %v, want exit code 1", err)
			}
			if stdout != nil {
				t.Fatalf("stdout = %v, want nothing on a refusal", stdout)
			}
			if stderr["error"] != "feature_disabled" || !strings.Contains(stderr["message"].(string), boardUnavailableMessage) {
				t.Fatalf("stderr = %v, want a feature_disabled JSON error naming %q", stderr, boardUnavailableMessage)
			}
		})
	}
	if after := run.frozenBoardState(t); after != before {
		t.Fatalf("refused Board commands changed the run state (%d bytes before, %d after)", len(before), len(after))
	}
}

// Messages are not part of the Board: they keep working while the Board is off.
func TestAgentSendWorksInAssistantsMode(t *testing.T) {
	run := startBoardGateRun(t, assistants.CollaborationModeAssistants)
	stdout, stderr, err := runBoardCLI(t, run, "send", "--to", "user", "--kind", "message", "--body", "still talking")
	if err != nil || stdout["message_id"] == nil {
		t.Fatalf("send = %v, stderr %v, error %v; want a delivered message", stdout, stderr, err)
	}
}

func TestAgentBoardCLIWorksInTeamMode(t *testing.T) {
	run := startBoardGateRun(t, assistants.CollaborationModeTeam)
	if _, stderr, err := runBoardCLI(t, run, "board", "publish", "--key", "notes", "--status", "ready"); err != nil {
		t.Fatalf("board publish: %v (stderr %v)", err, stderr)
	}
	stdout, stderr, err := runBoardCLI(t, run, "board", "get", "--key", "notes")
	if entries, _ := stdout["entries"].([]any); err != nil || len(entries) != 1 {
		t.Fatalf("board get = %v, stderr %v, error %v; want the published entry", stdout, stderr, err)
	}
	if _, stderr, err := runBoardCLI(t, run, "task", "create", "--key", "task/gate", "--title", "Gate"); err != nil {
		t.Fatalf("task create: %v (stderr %v)", err, stderr)
	}
	stdout, stderr, err = runBoardCLI(t, run, "task", "get", "--key", "task/gate")
	if tasks, _ := stdout["tasks"].([]any); err != nil || len(tasks) != 1 {
		t.Fatalf("task get = %v, stderr %v, error %v; want the created task", stdout, stderr, err)
	}
}

// boardNoticeToStageAgent commits a Board notice for the stage agent the way a team-mode run did before it went on
// in assistants mode: through an orchestrator in team mode over the same store.
func (r boardGateRun) boardNoticeToStageAgent(t *testing.T) agentcomm.Message {
	t.Helper()
	teamOpts := r.opts
	teamOpts.CollaborationMode = assistants.CollaborationModeTeam
	stage := agentcomm.ParticipantID("stage:impl")
	if _, err := orchestrator.New(teamOpts).Publish(r.agent.ctx, orchestrator.PublishRequest{
		OperationID: "gate-notice-publish", Author: agentcomm.UserParticipantID, Key: "notes/api", Status: "ready",
		Notify: []agentcomm.ParticipantID{stage}, Actor: state.Actor{Kind: "user"},
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	mesh := r.agent.store.Snapshot().Assistants.Mesh
	for _, id := range mesh.Inbox[stage] {
		if m := mesh.Messages[id]; strings.Contains(m.Body, "afm agent") {
			return *m
		}
	}
	t.Fatal("the publish sent the stage agent no notice")
	return agentcomm.Message{}
}

// messageIDs lists the ids in a mailbox page of the agent HTTP API.
func messageIDs(page map[string]any) []string {
	var ids []string
	messages, _ := page["messages"].([]any)
	for _, m := range messages {
		if fields, ok := m.(map[string]any); ok {
			ids = append(ids, fields["id"].(string))
		}
	}
	return ids
}

// The agent's mailbox reads show a notice left from team mode only while the Board is on; ordinary messages in
// the same conversation show in both modes.
func TestAgentMailboxHidesBoardNoticeInAssistantsMode(t *testing.T) {
	for _, tc := range []struct {
		mode       assistants.CollaborationMode
		wantNotice bool
	}{
		{assistants.CollaborationModeTeam, true},
		{assistants.CollaborationModeAssistants, false},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			run := startBoardGateRun(t, tc.mode)
			notice := run.boardNoticeToStageAgent(t)
			note, err := run.agent.orch.SendFromAgent(run.agent.ctx, orchestrator.SendRequest{
				OperationID: "gate-note", ThreadID: notice.ThreadID, Sender: agentcomm.UserParticipantID,
				Recipients: []agentcomm.ParticipantID{"stage:impl"}, Kind: agentcomm.MessageKindMessage,
				Body: "ordinary note", Actor: state.Actor{Kind: "user"},
			})
			if err != nil {
				t.Fatalf("send note: %v", err)
			}
			for _, route := range []boardRoute{
				{name: "inbox", method: http.MethodGet, path: "/api/agent-mesh/inbox?all=1"},
				{name: "thread", method: http.MethodGet, path: "/api/agent-mesh/thread?id=" + string(notice.ThreadID)},
			} {
				status, page := run.do(t, route, run.agent.token, "")
				if status != http.StatusOK {
					t.Fatalf("%s: HTTP %d %v", route.name, status, page)
				}
				ids := messageIDs(page)
				if got := slices.Contains(ids, string(notice.ID)); got != tc.wantNotice {
					t.Errorf("%s shows the notice = %v, want %v (ids %v)", route.name, got, tc.wantNotice, ids)
				}
				if !slices.Contains(ids, string(note.MessageID)) {
					t.Errorf("%s lost the ordinary note (ids %v)", route.name, ids)
				}
			}
		})
	}
}

// promptRunner reports each prompt it is given, the way a real Assistant turn would receive it.
type promptRunner struct {
	cfg     executor.Config
	prompts chan<- string
}

func (r *promptRunner) RunAgent(ctx context.Context, _, _, prompt, _ string) error {
	r.cfg.OnDelivered()
	select {
	case r.prompts <- prompt:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// A run that committed a Board notice for the Assistant in team mode and goes on in assistants mode: neither the
// bridge nor the Assistant's earlier turn on such a notice hands it to the model.
func TestSideAgentMesh_AssistantsModeDoesNotPromptBoardNotice(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	runDir := t.TempDir()
	teamStore, err := state.Open(runDir, []string{"impl"})
	if err != nil {
		t.Fatal(err)
	}
	opts := orchestrator.Options{
		RunDir: runDir, RunID: "side-test", Store: teamStore, Config: config.Default(), Prompts: orchestrator.DefaultPrompts(),
		Stages:            []flow.Stage{{ID: "impl", Name: "impl", Agents: []flow.AgentType{flow.AgentAuto}}},
		CollaborationMode: assistants.CollaborationModeTeam,
	}
	team := orchestrator.New(opts)
	if _, err := team.RegisterSideAgent(ctx, orchestrator.RegisterSideAgentRequest{Command: "codex", Actor: state.Actor{Kind: "system"}}); err != nil {
		t.Fatal(err)
	}
	team.SetSideAgentAvailable(true)
	// Two notices: the Assistant answered the first in team mode, the second never reached it.
	for _, key := range []string{"notes/answered", "notes/pending"} {
		if _, err := team.Publish(ctx, orchestrator.PublishRequest{
			OperationID: "side-notice-" + key, Author: agentcomm.UserParticipantID, Key: key, Status: "ready",
			Notify: []agentcomm.ParticipantID{team.SideAgentParticipantID()}, Actor: state.Actor{Kind: "user"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	mesh := teamStore.Snapshot().Assistants.Mesh
	notices := mesh.Inbox[team.SideAgentParticipantID()]
	if len(notices) != 2 {
		t.Fatalf("persisted notices = %v, want exactly two", notices)
	}
	answered := mesh.Messages[notices[0]]
	if err := teamStore.Close(); err != nil {
		t.Fatal(err)
	}

	// The restart: the journal is replayed from disk and the run continues without the Board.
	store, err := state.Open(runDir, []string{"impl"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	opts.Store = store
	opts.CollaborationMode = assistants.CollaborationModeAssistants
	orch := orchestrator.New(opts)

	// The Assistant answered the notice in team mode: its journal still holds that turn.
	sideStore, err := sideagent.Open(t.TempDir(), "side-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sideStore.Close() })
	for _, event := range []struct {
		typ  sideagent.EventType
		data any
	}{
		{sideagent.EventTurnAccepted, sideagent.TurnAcceptedData{Message: answered.Body, SourceMessageID: string(answered.ID), SourceThreadID: string(answered.ThreadID), SourceSenderID: "user"}},
		{sideagent.EventTurnStarted, sideagent.TurnStartedData{Command: "codex"}},
		{sideagent.EventAssistantText, sideagent.AssistantTextData{Text: "published through afm agent board"}},
		{sideagent.EventTurnCompleted, sideagent.TurnCompletedData{}},
	} {
		if _, err := sideStore.Append(event.typ, "old-notice-turn", event.data); err != nil {
			t.Fatal(err)
		}
	}

	prompts := make(chan string, 2)
	mgr := newStartedSideManager(sideagent.Config{
		Store: sideStore, Command: "codex", SourceReply: orch.SideAgentExplicitReply,
		HideSource: func(id string) bool { return orch.HidesFromAgents(agentcomm.MessageID(id)) },
		OnSourceDelivered: func(id string) {
			if err := orch.MarkMessageDelivered(ctx, agentcomm.MessageID(id), orch.SideAgentParticipantID()); err != nil {
				t.Errorf("deliver: %v", err)
			}
		},
		NewExecutor: func(cfg executor.Config) sideagent.AgentRunner { return &promptRunner{cfg: cfg, prompts: prompts} },
	})
	bridge := startSideAgentMesh(orch, mgr)
	t.Cleanup(func() {
		bridge.Abort()
		mgr.Shutdown()
		cleanupCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		bridge.Drain(cleanupCtx)
		mgr.Drain(cleanupCtx)
	})

	// The ordinary message is the control: its turn proves the bridge ran past the notice.
	ordinary, err := orch.SendFromAgent(ctx, orchestrator.SendRequest{
		OperationID: "side-ordinary", Sender: agentcomm.UserParticipantID, Recipients: []agentcomm.ParticipantID{orch.SideAgentParticipantID()},
		Kind: agentcomm.MessageKindMessage, Body: "ordinary question", Actor: state.Actor{Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var prompt string
	select {
	case prompt = <-prompts:
	case <-ctx.Done():
		t.Fatal("the Assistant was never prompted for the ordinary message")
	}
	leaks := []string{"afm agent board", "published through", "notes/answered", "notes/pending"}
	for _, id := range notices {
		leaks = append(leaks, string(id))
	}
	for _, leak := range leaks {
		if strings.Contains(prompt, leak) {
			t.Errorf("the prompt shows Board notice content %q:\n%s", leak, prompt)
		}
	}
	if !strings.Contains(prompt, "ordinary question") || !strings.Contains(prompt, string(ordinary.MessageID)) {
		t.Fatalf("the prompt lost the ordinary message:\n%s", prompt)
	}

	bridge.CloseInput()
	if !bridge.Drain(ctx) {
		t.Fatal("bridge did not drain")
	}
	select {
	case extra := <-prompts:
		t.Fatalf("a second turn ran, prompt:\n%s", extra)
	default:
	}
	for _, e := range mgr.History() {
		var accepted sideagent.TurnAcceptedData
		if e.Type == sideagent.EventTurnAccepted && e.DecodeData(&accepted) == nil && slices.Contains(notices, agentcomm.MessageID(accepted.SourceMessageID)) && e.TurnID != "old-notice-turn" {
			t.Errorf("the Assistant accepted a new turn for the Board notice: %+v", accepted)
		}
	}
}
