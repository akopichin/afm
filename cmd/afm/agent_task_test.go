package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/server"
	"github.com/spf13/cobra"
)

func (f *fakeCLIAgentMesh) BoardTask(ctx context.Context, requestID, actor string, req server.MeshBoardTaskRequest) (orchestrator.BoardTaskReceipt, error) {
	f.lastTaskReqID, f.lastTask = requestID, req
	return orchestrator.BoardTaskReceipt{Key: req.Key, Version: req.ExpectedVersion + 1, Status: agentcomm.BoardTaskDone, Seq: 5}, nil
}

func (f *fakeCLIAgentMesh) BoardTasks(ctx context.Context, key string) ([]agentcomm.BoardTaskView, error) {
	f.lastTasksKey = key
	return []agentcomm.BoardTaskView{{BoardTask: agentcomm.BoardTask{Key: key, Title: "T", Version: 1}, Status: agentcomm.BoardTaskTodo}}, nil
}

// runAgentTestCmd runs one afm agent command against a real server backed by fake and returns its stdout.
func runAgentTestCmd(t *testing.T, fake *fakeCLIAgentMesh, cmd *cobra.Command, args ...string) string {
	t.Helper()
	httpSrv := httptest.NewServer(server.New(server.Config{AgentMesh: fake}).Handler())
	defer httpSrv.Close()
	writeTestMeshDescriptor(t, httpSrv, fake.token, fake.participant)

	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute %v: %v (stderr=%s)", args, err, errOut.String())
	}
	return out.String()
}

func TestAgentTaskCLI_DoneSendsResultAndRequestID(t *testing.T) {
	fake := &fakeCLIAgentMesh{token: "tok-X", participant: "run:t-1"}
	out := runAgentTestCmd(t, fake, newAgentTaskCmd(), "done", "--key", "task/api", "--assignment-id", "as-1",
		"--expected-version", "3", "--summary", "shipped", "--artifacts", "a.md, b.md", "--request-id", "rq-1")

	want := server.MeshBoardTaskRequest{
		Action: "done", Key: "task/api", ExpectedVersion: 3, AssignmentID: "as-1", Text: "shipped", ArtifactRefs: []string{"a.md", "b.md"},
	}
	if fake.lastTaskReqID != "rq-1" || !reflect.DeepEqual(fake.lastTask, want) {
		t.Fatalf("Service got %q %+v, want rq-1 %+v", fake.lastTaskReqID, fake.lastTask, want)
	}
	var receipt orchestrator.BoardTaskReceipt
	if err := json.Unmarshal([]byte(out), &receipt); err != nil || receipt.Version != 4 || receipt.Status != agentcomm.BoardTaskDone {
		t.Fatalf("stdout = %q (%v), want the receipt", out, err)
	}
}

func TestAgentTaskCLI_MutationsMapFlags(t *testing.T) {
	cases := []struct {
		args []string
		want server.MeshBoardTaskRequest
	}{
		{[]string{"create", "--key", "task/api", "--title", "API", "--description", "D", "--assign-to", "docs", "--coordinator", "user"},
			server.MeshBoardTaskRequest{Action: "create", Key: "task/api", Title: "API", Description: "D", To: "docs", Coordinator: "user"}},
		{[]string{"assign", "--key", "task/api", "--to", "docs", "--expected-version", "2"},
			server.MeshBoardTaskRequest{Action: "assign", Key: "task/api", To: "docs", ExpectedVersion: 2}},
		{[]string{"claim", "--key", "task/api", "--expected-version", "1"},
			server.MeshBoardTaskRequest{Action: "claim", Key: "task/api", ExpectedVersion: 1}},
		{[]string{"start", "--key", "task/api", "--assignment-id", "as-1", "--expected-version", "2"},
			server.MeshBoardTaskRequest{Action: "start", Key: "task/api", AssignmentID: "as-1", ExpectedVersion: 2}},
		{[]string{"progress", "--key", "task/api", "--assignment-id", "as-1", "--expected-version", "3", "--text", "half"},
			server.MeshBoardTaskRequest{Action: "progress", Key: "task/api", AssignmentID: "as-1", ExpectedVersion: 3, Text: "half"}},
		{[]string{"release", "--key", "task/api", "--assignment-id", "as-1", "--expected-version", "3", "--reason", "blocked"},
			server.MeshBoardTaskRequest{Action: "release", Key: "task/api", AssignmentID: "as-1", ExpectedVersion: 3, Text: "blocked"}},
		{[]string{"reopen", "--key", "task/api", "--expected-version", "4", "--reason", "regressed"},
			server.MeshBoardTaskRequest{Action: "reopen", Key: "task/api", ExpectedVersion: 4, Text: "regressed"}},
		{[]string{"edit", "--key", "task/api", "--expected-version", "5", "--title", "API v2"},
			server.MeshBoardTaskRequest{Action: "edit", Key: "task/api", ExpectedVersion: 5, Title: "API v2"}},
	}
	for _, c := range cases {
		fake := &fakeCLIAgentMesh{token: "tok-X", participant: "run:t-1"}
		runAgentTestCmd(t, fake, newAgentTaskCmd(), c.args...)
		if !reflect.DeepEqual(fake.lastTask, c.want) {
			t.Errorf("%v: Service got %+v, want %+v", c.args, fake.lastTask, c.want)
		}
		if !strings.HasPrefix(fake.lastTaskReqID, "cli-") {
			t.Errorf("%v: request id = %q, want a fresh cli- id", c.args, fake.lastTaskReqID)
		}
	}
}

// Keys travel in the query escaped: '+', ' ', '&' and '#' reach the server as typed.
func TestAgentTaskCLI_GetAndBoardGetEscapeKey(t *testing.T) {
	const key = "task/a+b c&x=1#y"
	fake := &fakeCLIAgentMesh{token: "tok-X", participant: "run:t-1"}
	out := runAgentTestCmd(t, fake, newAgentTaskCmd(), "get", "--key", key)
	if fake.lastTasksKey != key {
		t.Fatalf("task get key = %q, want %q", fake.lastTasksKey, key)
	}
	if !strings.Contains(out, `"assignments":[]`) {
		t.Fatalf("stdout = %s, want the task with assignments []", out)
	}

	runAgentTestCmd(t, fake, newAgentBoardCmd(), "get", "--key", key)
	if fake.lastBoardKey != key {
		t.Fatalf("board get key = %q, want %q", fake.lastBoardKey, key)
	}
}

func TestAgentTaskCLI_ListReadsEveryTask(t *testing.T) {
	fake := &fakeCLIAgentMesh{token: "tok-X", participant: "run:t-1", lastTasksKey: "unset"}
	out := runAgentTestCmd(t, fake, newAgentTaskCmd(), "list")
	if fake.lastTasksKey != "" {
		t.Fatalf("task list key = %q, want empty", fake.lastTasksKey)
	}
	var payload struct {
		Tasks []agentcomm.BoardTaskView `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil || len(payload.Tasks) != 1 {
		t.Fatalf("stdout = %q (%v), want one task", out, err)
	}
}

// send and board publish take an explicit --request-id, so an agent can retry the same change safely.
func TestAgentCLI_SendAndPublishTakeRequestID(t *testing.T) {
	fake := &fakeCLIAgentMesh{token: "tok-X", participant: "run:t-1"}
	runAgentTestCmd(t, fake, newAgentSendCmd(), "--to", "docs", "--body", "hi", "--request-id", "rq-send")
	if fake.lastSendReqID != "rq-send" {
		t.Fatalf("send request id = %q, want rq-send", fake.lastSendReqID)
	}
	runAgentTestCmd(t, fake, newAgentBoardCmd(), "publish", "--key", "notes", "--status", "ok", "--request-id", "rq-pub")
	if fake.lastPublishReqID != "rq-pub" {
		t.Fatalf("publish request id = %q, want rq-pub", fake.lastPublishReqID)
	}
}

// Both HTTP adapters hand the Service every body field under its own name, with the actor they authenticated.
func TestBoardTaskRequest_MapsEveryField(t *testing.T) {
	got := boardTaskRequest("rq-1", "run:t-1", server.MeshBoardTaskRequest{
		Action: "done", Key: "task/api", ExpectedVersion: 3, AssignmentID: "as-1", To: "docs", Title: "T",
		Description: "D", Coordinator: "user", Text: "ok", ArtifactRefs: []string{"a.md"},
	})
	want := orchestrator.BoardTaskRequest{
		OperationID: "rq-1", Action: orchestrator.BoardTaskDone, Actor: "run:t-1", Key: "task/api", ExpectedVersion: 3,
		AssignmentID: "as-1", To: "docs", Title: "T", Description: "D", CoordinatorID: "user", Text: "ok", ArtifactRefs: []string{"a.md"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}
