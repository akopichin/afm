package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/server"
	"github.com/akopichin/afm/pkg/state"
)

// fakeCLIAgentMesh is a minimal server.AgentMeshActions good enough to exercise the afm agent CLI end to
// end over a real HTTP round trip (httptest.Server + the real pkg/server mux), without an orchestrator.
type fakeCLIAgentMesh struct {
	token       string
	participant string
	lastSend    server.MeshSendRequest
	sendErr     error
	inboxAll    []bool

	lastSendReqID    string
	lastPublishReqID string
	lastBoardKey     string
	lastTaskReqID    string
	lastTask         server.MeshBoardTaskRequest
	lastTasksKey     string

	loops        []orchestrator.LoopView
	lastLoopReq  string
	lastLoopCmd  server.MeshLoopCommand
	lastLoopCall int

	lastWait      server.MeshWaitRequest
	waitCalls     int
	waitReceipt   server.MeshWaitReceipt
	batches       []server.MeshWaitReceipt
	statusBatchID string
	statusErr     error
	waitErr       error

	lastSpawn      server.MeshSpawnRequest
	lastSpawnReqID string
	spawnCalls     int
	spawnErr       error
	helperTaskID   string
	helperResult   server.MeshHelperResult
	helperErr      error
}

func (f *fakeCLIAgentMesh) ResolveToken(token string) (string, bool) {
	if token != f.token {
		return "", false
	}
	return f.participant, true
}

func (f *fakeCLIAgentMesh) ListParticipants(ctx context.Context, actor string) ([]agentcomm.Participant, error) {
	return []agentcomm.Participant{{ID: "user", Kind: agentcomm.KindUser, Scope: agentcomm.ScopeRun, Reachable: true}}, nil
}

func (f *fakeCLIAgentMesh) Send(ctx context.Context, requestID, actor string, req server.MeshSendRequest) (server.MeshSendReceipt, error) {
	f.lastSend, f.lastSendReqID = req, requestID
	if f.sendErr != nil {
		return server.MeshSendReceipt{}, f.sendErr
	}
	return server.MeshSendReceipt{MessageID: "msg-1", Seq: 1}, nil
}

func (f *fakeCLIAgentMesh) Inbox(ctx context.Context, actor string, cursor uint64, limit int, all bool) (server.MeshMessagePage, error) {
	f.inboxAll = append(f.inboxAll, all)
	return server.MeshMessagePage{}, nil
}

func (f *fakeCLIAgentMesh) Thread(ctx context.Context, actor, threadID string, cursor uint64, limit int) (server.MeshMessagePage, error) {
	return server.MeshMessagePage{}, nil
}

func (f *fakeCLIAgentMesh) Ack(ctx context.Context, requestID, actor, messageID string) error {
	return nil
}

func (f *fakeCLIAgentMesh) Wait(ctx context.Context, requestID, actor string, req server.MeshWaitRequest) (server.MeshWaitReceipt, error) {
	f.lastWait = req
	f.waitCalls++
	if f.waitErr != nil {
		return server.MeshWaitReceipt{}, f.waitErr
	}
	if f.waitReceipt.Status != "" {
		return f.waitReceipt, nil
	}
	return server.MeshWaitReceipt{WaitID: "w1", Status: "pending"}, nil
}

func (f *fakeCLIAgentMesh) WaitStatus(ctx context.Context, actor, batchID string) ([]server.MeshWaitReceipt, error) {
	f.statusBatchID = batchID
	return f.batches, f.statusErr
}

func (f *fakeCLIAgentMesh) Spawn(ctx context.Context, requestID, actor string, req server.MeshSpawnRequest) (server.MeshSpawnReceipt, error) {
	f.lastSpawn, f.lastSpawnReqID = req, requestID
	f.spawnCalls++
	if f.spawnErr != nil {
		return server.MeshSpawnReceipt{}, f.spawnErr
	}
	return server.MeshSpawnReceipt{TaskID: "t-1", ParticipantID: "helper:t-1", MessageID: "mm-1"}, nil
}

func (f *fakeCLIAgentMesh) HelperResult(ctx context.Context, actor, taskID string) (server.MeshHelperResult, error) {
	f.helperTaskID = taskID
	return f.helperResult, f.helperErr
}

func (f *fakeCLIAgentMesh) CancelWait(ctx context.Context, requestID, actor string, req server.MeshCancelWaitRequest) error {
	return nil
}

func (f *fakeCLIAgentMesh) PublishBoard(ctx context.Context, requestID, actor string, req server.MeshPublishRequest) (agentcomm.BoardEntry, error) {
	f.lastPublishReqID = requestID
	return agentcomm.BoardEntry{AuthorID: agentcomm.ParticipantID(actor), Key: req.Key, Version: 1}, nil
}

func (f *fakeCLIAgentMesh) Board(ctx context.Context, key string) ([]agentcomm.BoardEntry, error) {
	f.lastBoardKey = key
	return nil, nil
}

func (f *fakeCLIAgentMesh) Loops(ctx context.Context, actor string) (orchestrator.LoopPage, error) {
	return orchestrator.LoopPage{Loops: f.loops}, nil
}

func (f *fakeCLIAgentMesh) Loop(ctx context.Context, requestID, actor string, cmd server.MeshLoopCommand) (orchestrator.LoopView, error) {
	f.lastLoopReq, f.lastLoopCmd = requestID, cmd
	f.lastLoopCall++
	return orchestrator.LoopView{ID: "L1", CreatorID: agentcomm.ParticipantID(actor), TargetID: agentcomm.ParticipantID(actor), Every: cmd.Every.String(), Status: orchestrator.LoopActive}, nil
}

// writeTestMeshDescriptor writes a 0600 descriptor file pointing at the given httptest server and sets
// AFM_MESH_DESCRIPTOR for the duration of the test.
func writeTestMeshDescriptor(t *testing.T, srv *httptest.Server, token, participant string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "descriptor.json")
	data, err := json.Marshal(agentMeshDescriptor{URL: srv.URL + "/api/agent-mesh", Token: token, ParticipantID: participant})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AFM_MESH_DESCRIPTOR", path)
}

func TestAgentCLI_NoDescriptor_ClearError(t *testing.T) {
	t.Setenv("AFM_MESH_DESCRIPTOR", "")
	cmd := newAgentWhoamiCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected an error with no descriptor")
	}
	if out.Len() != 0 {
		t.Fatalf("stdout must stay empty on failure, got %q", out.String())
	}
	var payload map[string]string
	if jerr := json.Unmarshal(errOut.Bytes(), &payload); jerr != nil {
		t.Fatalf("stderr must be one JSON object, got %q: %v", errOut.String(), jerr)
	}
	if payload["error"] != "messaging_unavailable" {
		t.Fatalf("error code = %q, want messaging_unavailable", payload["error"])
	}
	if payload["message"] == "" {
		t.Fatal("message must be non-empty")
	}
}

func TestAgentCLI_Whoami_OneJSONObjectOnStdout(t *testing.T) {
	fake := &fakeCLIAgentMesh{token: "tok-X", participant: "helper:abc"}
	srv := server.New(server.Config{AgentMesh: fake})
	httpSrv := httptest.NewServer(srv.Handler())
	defer httpSrv.Close()
	writeTestMeshDescriptor(t, httpSrv, "tok-X", "helper:abc")

	cmd := newAgentWhoamiCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v (stderr=%s)", err, errOut.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr must stay empty on success, got %q", errOut.String())
	}
	var payload map[string]string
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("stdout must be exactly one JSON object: %v (got %q)", err, out.String())
	}
	if payload["participant_id"] != "helper:abc" {
		t.Fatalf("participant_id = %q, want helper:abc", payload["participant_id"])
	}
}

func TestAgentCLI_Send_ReachesServiceWithTokenIdentity(t *testing.T) {
	fake := &fakeCLIAgentMesh{token: "tok-X", participant: "helper:abc"}
	srv := server.New(server.Config{AgentMesh: fake})
	httpSrv := httptest.NewServer(srv.Handler())
	defer httpSrv.Close()
	writeTestMeshDescriptor(t, httpSrv, "tok-X", "helper:abc")

	cmd := newAgentSendCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"--thread", "t1", "--to", "helper:qa", "--body", "hello"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v (stderr=%s)", err, errOut.String())
	}
	if fake.lastSend.ThreadID != "t1" || fake.lastSend.Body != "hello" || len(fake.lastSend.Recipients) != 1 || fake.lastSend.Recipients[0] != "helper:qa" {
		t.Fatalf("Service received %+v", fake.lastSend)
	}
	var payload map[string]any
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("stdout must be one JSON object: %v", err)
	}
}

func TestAgentCLI_Wait_NeverBlocksAndReportsYieldOrReady(t *testing.T) {
	fake := &fakeCLIAgentMesh{token: "tok-X", participant: "helper:abc"}
	srv := server.New(server.Config{AgentMesh: fake})
	httpSrv := httptest.NewServer(srv.Handler())
	defer httpSrv.Close()
	writeTestMeshDescriptor(t, httpSrv, "tok-X", "helper:abc")

	cmd := newAgentWaitCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"--condition", "reply_to", "--message-id", "m1"})
	done := make(chan error, 1)
	go func() { done <- cmd.Execute() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Execute: %v (stderr=%s)", err, errOut.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("afm agent wait must return promptly, never block")
	}
	var payload map[string]string
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("stdout must be one JSON object: %v", err)
	}
	if payload["status"] != agentWaitStatusYield {
		t.Fatalf("status = %q, want yield (fake Service returns pending)", payload["status"])
	}
	if payload["wait_id"] != "w1" {
		t.Fatalf("wait_id = %q, want w1", payload["wait_id"])
	}
}

// A rejected send prints the server's reason, so the agent learns what to fix instead of "HTTP 500".
func TestAgentCLI_Send_RejectionPrintsServerReason(t *testing.T) {
	fake := &fakeCLIAgentMesh{token: "tok-X", participant: "helper:abc", sendErr: &state.OperationRejectedError{
		Code: assistants.CodeNotFound, Reason: "unknown thread t1 — send without a thread to start a conversation",
	}}
	srv := server.New(server.Config{AgentMesh: fake})
	httpSrv := httptest.NewServer(srv.Handler())
	defer httpSrv.Close()
	writeTestMeshDescriptor(t, httpSrv, "tok-X", "helper:abc")

	cmd := newAgentSendCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"--thread", "t1", "--to", "helper:qa", "--body", "hello"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("Execute: want an error for a rejected send")
	}
	var payload map[string]string
	if err := json.Unmarshal(errOut.Bytes(), &payload); err != nil {
		t.Fatalf("stderr must be one JSON object: %v (got %q)", err, errOut.String())
	}
	if payload["error"] != "not_found" || !strings.Contains(payload["message"], "send without a thread") {
		t.Fatalf("stderr = %v, want not_found with the server's reason", payload)
	}
}

// `afm agent inbox` asks for new messages only; --all asks for the whole history.
func TestAgentCLI_Inbox_NewOnlyUnlessAll(t *testing.T) {
	fake := &fakeCLIAgentMesh{token: "tok-X", participant: "run:t-1"}
	srv := server.New(server.Config{AgentMesh: fake})
	httpSrv := httptest.NewServer(srv.Handler())
	defer httpSrv.Close()
	writeTestMeshDescriptor(t, httpSrv, "tok-X", "run:t-1")

	for _, args := range [][]string{nil, {"--all"}} {
		cmd := newAgentInboxCmd()
		var out, errOut bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&errOut)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("Execute %v: %v (stderr=%s)", args, err, errOut.String())
		}
	}
	if len(fake.inboxAll) != 2 || fake.inboxAll[0] || !fake.inboxAll[1] {
		t.Fatalf("Service got all = %v, want [false true]", fake.inboxAll)
	}
}

// ---- Batch waits ----

// runAgentCLI starts the mesh server over fake, runs cmd with args as the participant of the fake and returns
// the one JSON object on stdout, or the one on stderr when the command failed.
func runAgentCLI(t *testing.T, fake *fakeCLIAgentMesh, cmd *cobra.Command, args ...string) (stdout, stderr map[string]any, err error) {
	t.Helper()
	httpSrv := httptest.NewServer(server.New(server.Config{AgentMesh: fake}).Handler())
	t.Cleanup(httpSrv.Close)
	writeTestMeshDescriptor(t, httpSrv, fake.token, fake.participant)
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

// reply_to_all registers one batch: the ids reach the Service as a list in the given order (spaces around them
// dropped), as a reply_to condition with no message id, and the receipt shows every item's outcome.
func TestAgentWait_ReplyToAllRegistersTheBatchAndShowsEveryItem(t *testing.T) {
	fake := &fakeCLIAgentMesh{token: "tok-X", participant: "stage:s1", waitReceipt: server.MeshWaitReceipt{
		BatchID: "mb-1", Status: "pending", Items: []server.MeshWaitItem{
			{MessageID: "m1", WaitID: "mw-1", Status: "ready"}, {MessageID: "m2", WaitID: "mw-2", Status: "pending"}, {MessageID: "m3", WaitID: "mw-3", Status: "pending"},
		},
	}}

	out, _, err := runAgentCLI(t, fake, newAgentWaitCmd(), "--condition", "reply_to_all", "--message-ids", " m1, m2 ,m3 ", "--deadline-seconds", "300")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if want := []string{"m1", "m2", "m3"}; !reflect.DeepEqual(fake.lastWait.MessageIDs, want) || fake.lastWait.Condition.Kind != agentcomm.WaitOnReplyTo || fake.lastWait.Condition.MessageID != "" {
		t.Fatalf("Service got %+v, want a reply_to condition with ids %v", fake.lastWait, want)
	}
	if fake.lastWait.DeadlineUnix == nil {
		t.Fatal("the deadline of the batch did not reach the Service")
	}
	if out["status"] != agentWaitStatusYield || out["batch_id"] != "mb-1" {
		t.Fatalf("output = %v, want yield for batch mb-1", out)
	}
	items, _ := out["items"].([]any)
	if len(items) != 3 || items[0].(map[string]any)["status"] != "ready" || items[1].(map[string]any)["message_id"] != "m2" {
		t.Fatalf("items = %v, want the three outcomes in order", out["items"])
	}
}

// reply_to with a list is the same batch as reply_to_all.
func TestAgentWait_ReplyToWithMessageIDsIsABatchToo(t *testing.T) {
	fake := &fakeCLIAgentMesh{token: "tok-X", participant: "stage:s1", waitReceipt: server.MeshWaitReceipt{BatchID: "mb-1", Status: "pending"}}

	out, _, err := runAgentCLI(t, fake, newAgentWaitCmd(), "--condition", "reply_to", "--message-ids", "m1,m2")
	if err != nil || out["batch_id"] != "mb-1" || !reflect.DeepEqual(fake.lastWait.MessageIDs, []string{"m1", "m2"}) {
		t.Fatalf("output = %v, %v, Service got %+v, want the batch of m1,m2", out, err, fake.lastWait)
	}
}

// The reason the Service refuses a batch names the request at fault; the agent gets it as the server wrote it.
func TestAgentWait_ReplyToAllShowsWhichRequestWasRefused(t *testing.T) {
	fake := &fakeCLIAgentMesh{token: "tok-X", participant: "stage:s1", waitErr: &state.OperationRejectedError{
		Code: assistants.CodeNotFound, Reason: "unknown message mm-7 in a batch wait",
	}}

	out, errOut, err := runAgentCLI(t, fake, newAgentWaitCmd(), "--condition", "reply_to_all", "--message-ids", "mm-1,mm-7")
	if err == nil || out != nil || errOut["error"] != "not_found" || !strings.Contains(errOut["message"].(string), "mm-7") {
		t.Fatalf("out %v, stderr %v, err %v, want not_found naming mm-7", out, errOut, err)
	}
}

// Every item answered: the agent need not end its turn.
func TestAgentWait_ReplyToAllIsReadyWhenEveryItemEnded(t *testing.T) {
	fake := &fakeCLIAgentMesh{token: "tok-X", participant: "stage:s1", waitReceipt: server.MeshWaitReceipt{
		BatchID: "mb-1", Status: "ready", Items: []server.MeshWaitItem{{MessageID: "m1", WaitID: "mw-1", Status: "ready"}},
	}}

	out, _, err := runAgentCLI(t, fake, newAgentWaitCmd(), "--condition", "reply_to_all", "--message-ids", "m1")
	if err != nil || out["status"] != agentWaitStatusReady {
		t.Fatalf("output = %v, %v, want ready", out, err)
	}
}

// A batch the CLI can tell is malformed is refused before any request, with the id at fault in the message.
func TestAgentWait_ReplyToAllRefusesAMalformedBatchBeforeTheService(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		mention string
	}{
		{"no ids", []string{"--condition", "reply_to_all"}, "--message-ids"},
		{"blank ids", []string{"--condition", "reply_to_all", "--message-ids", " "}, "--message-ids"},
		{"empty entry", []string{"--condition", "reply_to_all", "--message-ids", "m1,,m2"}, "empty"},
		{"trailing comma", []string{"--condition", "reply_to_all", "--message-ids", "m1,m2,"}, "empty"},
		{"duplicate", []string{"--condition", "reply_to_all", "--message-ids", "m1,m2, m1"}, "m1"},
		{"with --message-id", []string{"--condition", "reply_to_all", "--message-ids", "m1,m2", "--message-id", "m3"}, "--message-id"},
		{"other condition", []string{"--condition", "addressed_message", "--message-ids", "m1,m2"}, "reply_to"},
		{"reply_to with --message-id", []string{"--condition", "reply_to", "--message-ids", "m1,m2", "--message-id", "m3"}, "--message-id"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := &fakeCLIAgentMesh{token: "tok-X", participant: "stage:s1"}
			out, errOut, err := runAgentCLI(t, fake, newAgentWaitCmd(), c.args...)
			if err == nil || out != nil {
				t.Fatalf("Execute = %v with output %v, want a refusal with nothing on stdout", err, out)
			}
			if errOut["error"] != "invalid_request" || !strings.Contains(errOut["message"].(string), c.mention) {
				t.Fatalf("stderr = %v, want invalid_request mentioning %q", errOut, c.mention)
			}
			if fake.waitCalls != 0 {
				t.Fatal("a malformed batch reached the Service")
			}
		})
	}
}

// The single wait keeps its command, its flags and its output.
func TestAgentWait_SingleReplyToIsUnchanged(t *testing.T) {
	fake := &fakeCLIAgentMesh{token: "tok-X", participant: "stage:s1"}

	out, _, err := runAgentCLI(t, fake, newAgentWaitCmd(), "--condition", "reply_to", "--message-id", "m1")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(out) != 2 || out["status"] != agentWaitStatusYield || out["wait_id"] != "w1" {
		t.Fatalf("output = %v, want exactly status yield and wait_id w1", out)
	}
	if fake.lastWait.Condition.MessageID != "m1" || len(fake.lastWait.MessageIDs) != 0 {
		t.Fatalf("Service got %+v, want the one message id and no list", fake.lastWait)
	}
}

// wait-status prints what the server read for the caller: all batches, or the one named by --wait-id.
func TestAgentWaitStatus_ListsBatchesOrOpensOne(t *testing.T) {
	batch := server.MeshWaitReceipt{BatchID: "mb-1", Status: "ready", Items: []server.MeshWaitItem{
		{MessageID: "m1", WaitID: "mw-1", Status: "ready"}, {MessageID: "m2", WaitID: "mw-2", Status: "cancelled", Reason: "reviewer is gone"},
	}}
	fake := &fakeCLIAgentMesh{token: "tok-X", participant: "stage:s1", batches: []server.MeshWaitReceipt{batch}}

	out, _, err := runAgentCLI(t, fake, newAgentWaitStatusCmd())
	if err != nil || fake.statusBatchID != "" {
		t.Fatalf("list: %v, batch %q, want no batch id", err, fake.statusBatchID)
	}
	batches, _ := out["batches"].([]any)
	if len(batches) != 1 {
		t.Fatalf("batches = %v, want the one batch", out["batches"])
	}
	items := batches[0].(map[string]any)["items"].([]any)
	if got := items[1].(map[string]any); got["status"] != "cancelled" || got["reason"] != "reviewer is gone" {
		t.Fatalf("item 2 = %v, want cancelled with its reason", got)
	}

	if _, _, err := runAgentCLI(t, fake, newAgentWaitStatusCmd(), "--wait-id", "mb-1"); err != nil || fake.statusBatchID != "mb-1" {
		t.Fatalf("one batch: %v, batch %q, want mb-1", err, fake.statusBatchID)
	}
}

// A batch of another participant is refused by the server; the agent sees its error code, not a result.
func TestAgentWaitStatus_RefusalOfTheServerReachesTheAgent(t *testing.T) {
	fake := &fakeCLIAgentMesh{token: "tok-X", participant: "stage:s2", statusErr: &state.OperationRejectedError{Code: assistants.CodeNotFound, Reason: "unknown batch mb-1"}}

	out, errOut, err := runAgentCLI(t, fake, newAgentWaitStatusCmd(), "--wait-id", "mb-1")
	if err == nil || out != nil || errOut["error"] != "not_found" || !strings.Contains(errOut["message"].(string), "mb-1") {
		t.Fatalf("out %v, stderr %v, err %v, want not_found naming mb-1", out, errOut, err)
	}
}

// wait-status is a subcommand of `afm agent`, next to wait.
func TestAgentCmd_HasWaitStatus(t *testing.T) {
	found, _, err := newAgentCmd().Find([]string{"wait-status"})
	if err != nil || found.Name() != "wait-status" {
		t.Fatalf("Find wait-status = %v, %v", found, err)
	}
}

// asParticipant is the real mesh adapter with one fixed token that resolves to one participant. It stands in for
// participants without an invocation of their own; a stage agent's wait needs a live invocation token instead.
type asParticipant struct {
	agentMeshActions
	token, participant string
}

func (a asParticipant) ResolveToken(token string) (string, bool) {
	return a.participant, token == a.token
}

// Through the real adapter and orchestrator: one reply_to_all registers the batch, wait-status shows each
// item's outcome as the answers come in, and another participant can neither list nor open the batch.
func TestAgentWait_BatchEndToEndOverTheRealService(t *testing.T) {
	agent := startLiveStageAgent(t)
	orch := agent.orch
	const stage, stranger = "stage:impl", "stage:other"
	ctx := context.Background()
	var requests []string
	var thread agentcomm.ThreadID
	for i := range 2 {
		sent, err := orch.SendFromAgent(ctx, orchestrator.SendRequest{
			OperationID: fmt.Sprintf("op-ask-%d", i), ThreadID: thread, Sender: stage, Recipients: []agentcomm.ParticipantID{"user"},
			Kind: agentcomm.MessageKindRequest, Body: "please review", Actor: state.Actor{Kind: "agent", ID: stage},
		})
		if err != nil {
			t.Fatalf("send request %d: %v", i, err)
		}
		requests, thread = append(requests, string(sent.MessageID)), sent.ThreadID
	}
	cli := func(participant string, cmd *cobra.Command, args ...string) (map[string]any, map[string]any, error) {
		mesh, token := server.AgentMeshActions(asParticipant{agentMeshActions{orch}, "tok", participant}), "tok"
		if participant == stage {
			mesh, token = agentMeshActions{orch}, agent.token
		}
		httpSrv := httptest.NewServer(server.New(server.Config{AgentMesh: mesh}).Handler())
		t.Cleanup(httpSrv.Close)
		writeTestMeshDescriptor(t, httpSrv, token, participant)
		var out, errOut bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&errOut)
		cmd.SetArgs(args)
		err := cmd.Execute()
		var o, e map[string]any
		_ = json.Unmarshal(out.Bytes(), &o)
		_ = json.Unmarshal(errOut.Bytes(), &e)
		return o, e, err
	}

	out, _, err := cli(stage, newAgentWaitCmd(), "--condition", "reply_to_all", "--message-ids", requests[0]+", "+requests[1], "--deadline-seconds", "300")
	if err != nil || out["status"] != agentWaitStatusYield || out["batch_id"] == "" {
		t.Fatalf("batch wait = %v, %v, want a yield with a batch id", out, err)
	}
	batchID := out["batch_id"].(string)

	if _, err := orch.Send(ctx, orchestrator.SendRequest{
		OperationID: "op-reply-1", ThreadID: thread, Sender: "user", Recipients: []agentcomm.ParticipantID{stage},
		Kind: agentcomm.MessageKindReply, InReplyTo: new(agentcomm.MessageID(requests[1])), Body: "looks good",
	}); err != nil {
		t.Fatalf("reply: %v", err)
	}
	status, _, err := cli(stage, newAgentWaitStatusCmd(), "--wait-id", batchID)
	if err != nil {
		t.Fatalf("wait-status: %v", err)
	}
	batch := status["batches"].([]any)[0].(map[string]any)
	items := batch["items"].([]any)
	if batch["status"] != "pending" || items[0].(map[string]any)["status"] != "pending" || items[1].(map[string]any)["status"] != "ready" || items[1].(map[string]any)["message_id"] != requests[1] {
		t.Fatalf("batch = %v, want pending with the second request answered", batch)
	}

	listed, _, err := cli(stranger, newAgentWaitStatusCmd())
	if err != nil || len(listed["batches"].([]any)) != 0 {
		t.Fatalf("batches of a stranger = %v, %v, want none", listed, err)
	}
	_, refused, err := cli(stranger, newAgentWaitStatusCmd(), "--wait-id", batchID)
	if err == nil || refused["error"] != "not_found" {
		t.Fatalf("a stranger opening the batch: %v, %v, want not_found", refused, err)
	}
}

// A second `afm agent wait` for the same pending condition (each call has its own request id) is answered with
// the first wait: still a yield, with already_pending set and no second wait registered.
func TestAgentWait_RepeatedSingleWaitReportsTheSameWaitAsAlreadyPending(t *testing.T) {
	agent := startLiveStageAgent(t)
	orch, store := agent.orch, agent.store
	const stage = "stage:impl"
	sent, err := orch.SendFromAgent(context.Background(), orchestrator.SendRequest{
		OperationID: "op-ask", Sender: stage, Recipients: []agentcomm.ParticipantID{"user"},
		Kind: agentcomm.MessageKindRequest, Body: "please review", Actor: state.Actor{Kind: "agent", ID: stage},
	})
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	wait := func() map[string]any {
		t.Helper()
		httpSrv := httptest.NewServer(server.New(server.Config{AgentMesh: agentMeshActions{orch}}).Handler())
		t.Cleanup(httpSrv.Close)
		writeTestMeshDescriptor(t, httpSrv, agent.token, stage)
		cmd := newAgentWaitCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"--condition", "reply_to", "--message-id", string(sent.MessageID)})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("wait: %v", err)
		}
		var o map[string]any
		if err := json.Unmarshal(out.Bytes(), &o); err != nil {
			t.Fatalf("output must be one JSON object: %v", err)
		}
		return o
	}

	first, second := wait(), wait()
	if first["status"] != agentWaitStatusYield || len(first) != 2 {
		t.Fatalf("first = %v, want exactly status yield and wait_id", first)
	}
	alreadyPending, _ := second["already_pending"].(bool)
	if second["status"] != agentWaitStatusYield || second["wait_id"] != first["wait_id"] || !alreadyPending {
		t.Fatalf("second = %v, want a yield on wait %v with already_pending", second, first["wait_id"])
	}
	if n := len(store.Snapshot().Assistants.Mesh.Waits); n != 1 {
		t.Fatalf("waits = %d, want 1", n)
	}
}

// ---- spawn / helper-result ----

// spawn sends exactly the profile, the title and the instruction, and prints the ids it created.
func TestAgentSpawn_SendsOnlyProfileTitleInstructionAndPrintsTheIDs(t *testing.T) {
	fake := &fakeCLIAgentMesh{token: "tok-X", participant: "stage:s1"}

	out, _, err := runAgentCLI(t, fake, newAgentSpawnCmd(), "--profile", "reviewer", "--title", "Review cart", "--instruction", "look at the cart", "--request-id", "req-1")
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if want := (server.MeshSpawnRequest{ProfileID: "reviewer", Title: "Review cart", Instruction: "look at the cart"}); fake.lastSpawn != want || fake.lastSpawnReqID != "req-1" {
		t.Fatalf("Service got %+v request %q, want %+v and req-1", fake.lastSpawn, fake.lastSpawnReqID, want)
	}
	if out["task_id"] != "t-1" || out["participant_id"] != "helper:t-1" || out["message_id"] != "mm-1" {
		t.Fatalf("out = %v, want the task, participant and message ids", out)
	}
}

func TestAgentSpawn_RequiresProfileTitleAndInstructionBeforeTheService(t *testing.T) {
	full := map[string]string{"--profile": "reviewer", "--title": "T", "--instruction": "I"}
	for i, flag := range []string{"--profile", "--title", "--instruction"} {
		var args []string
		for name, value := range full {
			if name != flag {
				args = append(args, name, value)
			}
		}
		fake := &fakeCLIAgentMesh{token: "tok-X", participant: "stage:s1"}
		_, errOut, err := runAgentCLI(t, fake, newAgentSpawnCmd(), args...)
		if err == nil || errOut["error"] != "invalid_request" || !strings.Contains(errOut["message"].(string), flag) || fake.spawnCalls != 0 {
			t.Fatalf("case %d without %s: stderr %v, err %v, calls %d, want invalid_request naming the flag and no Service call", i, flag, errOut, err, fake.spawnCalls)
		}
	}
}

func TestAgentSpawn_RefusalOfTheServerReachesTheAgent(t *testing.T) {
	fake := &fakeCLIAgentMesh{token: "tok-X", participant: "helper:t-1", spawnErr: &state.OperationRejectedError{Code: assistants.CodeActorNotAllowed, Reason: "only a stage agent can spawn a helper"}}
	out, errOut, err := runAgentCLI(t, fake, newAgentSpawnCmd(), "--profile", "p", "--title", "t", "--instruction", "i")
	if err == nil || out != nil || errOut["error"] != "actor_not_allowed" {
		t.Fatalf("out %v, stderr %v, err %v, want actor_not_allowed", out, errOut, err)
	}
}

// helper-result asks for one task id and prints what the server read: the status, and the full result once there is one.
func TestAgentHelperResult_PrintsStatusAndTheFullResult(t *testing.T) {
	fake := &fakeCLIAgentMesh{token: "tok-X", participant: "stage:s1", helperResult: server.MeshHelperResult{
		TaskID: "t-1", Title: "Review", Status: "completed", Result: json.RawMessage(`{"verdict":"needs_changes","findings":[{"id":"f1"}]}`),
	}}
	out, _, err := runAgentCLI(t, fake, newAgentHelperResultCmd(), "--task-id", "t-1")
	if err != nil || fake.helperTaskID != "t-1" {
		t.Fatalf("helper-result: %v, task %q", err, fake.helperTaskID)
	}
	result, _ := out["result"].(map[string]any)
	if out["status"] != "completed" || result["verdict"] != "needs_changes" || len(result["findings"].([]any)) != 1 {
		t.Fatalf("out = %v, want completed with the full result", out)
	}

	fake.helperResult = server.MeshHelperResult{TaskID: "t-2", Title: "Other", Status: "running"}
	out, _, err = runAgentCLI(t, fake, newAgentHelperResultCmd(), "--task-id", "t-2")
	if _, hasResult := out["result"]; err != nil || out["status"] != "running" || hasResult {
		t.Fatalf("running helper: out %v, err %v, want a status and no result", out, err)
	}
}

func TestAgentHelperResult_NeedsTaskIDAndShowsTheServersRefusal(t *testing.T) {
	fake := &fakeCLIAgentMesh{token: "tok-X", participant: "stage:s1"}
	if _, errOut, err := runAgentCLI(t, fake, newAgentHelperResultCmd()); err == nil || errOut["error"] != "invalid_request" {
		t.Fatalf("no --task-id: stderr %v, err %v, want invalid_request", errOut, err)
	}
	fake.helperErr = &state.OperationRejectedError{Code: assistants.CodeNotFound, Reason: "unknown helper t-9"}
	if _, errOut, err := runAgentCLI(t, fake, newAgentHelperResultCmd(), "--task-id", "t-9"); err == nil || errOut["error"] != "not_found" {
		t.Fatalf("a task of another stage: stderr %v, err %v, want not_found", errOut, err)
	}
}

func TestAgentCmd_HasSpawnAndHelperResult(t *testing.T) {
	for _, name := range []string{"spawn", "helper-result"} {
		if found, _, err := newAgentCmd().Find([]string{name}); err != nil || found.Name() != name {
			t.Fatalf("Find %s = %v, %v", name, found, err)
		}
	}
}

// Through the real adapter and orchestrator: only a stage agent may spawn, and an unknown task is not found.
func TestAgentMeshActions_SpawnAndHelperResultOverTheRealService(t *testing.T) {
	orch, _ := newSideMeshOrch(t)
	actions := agentMeshActions{orch}
	ctx := context.Background()

	_, err := actions.Spawn(ctx, "op-1", "run:t-9", server.MeshSpawnRequest{ProfileID: "reviewer", Title: "T", Instruction: "I"})
	var rejected *state.OperationRejectedError
	if !errors.As(err, &rejected) || rejected.Code != assistants.CodeActorNotAllowed {
		t.Fatalf("spawn by a non-stage participant: %v, want actor_not_allowed", err)
	}
	_, err = actions.HelperResult(ctx, "stage:impl", "t-nope")
	if !errors.As(err, &rejected) || rejected.Code != assistants.CodeNotFound {
		t.Fatalf("helper-result of an unknown task: %v, want not_found", err)
	}
}
