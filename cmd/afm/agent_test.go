package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
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
	return server.MeshWaitReceipt{WaitID: "w1", Status: "pending"}, nil
}

func (f *fakeCLIAgentMesh) CancelWait(ctx context.Context, requestID, actor, waitID, reason string) error {
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
