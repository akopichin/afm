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
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/server"
	"github.com/akopichin/afm/pkg/state"
)

// loopRejectingMesh is a fakeCLIAgentMesh whose loop changes are refused with err.
type loopRejectingMesh struct {
	*fakeCLIAgentMesh
	err error
}

func (m loopRejectingMesh) Loop(ctx context.Context, requestID, actor string, cmd server.MeshLoopCommand) (orchestrator.LoopView, error) {
	return orchestrator.LoopView{}, m.err
}

// execAgentLoop runs `afm agent loop args…` against a real server backed by mesh, with stdin as the command's
// input, and returns stdout, stderr and the command's error.
func execAgentLoop(t *testing.T, mesh server.AgentMeshActions, token, participant, stdin string, args ...string) (string, string, error) {
	t.Helper()
	httpSrv := httptest.NewServer(server.New(server.Config{AgentMesh: mesh}).Handler())
	t.Cleanup(httpSrv.Close)
	writeTestMeshDescriptor(t, httpSrv, token, participant)

	var cmd = newAgentLoopCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

func runAgentLoop(t *testing.T, fake *fakeCLIAgentMesh, stdin string, args ...string) string {
	t.Helper()
	out, errOut, err := execAgentLoop(t, fake, fake.token, fake.participant, stdin, args...)
	if err != nil {
		t.Fatalf("Execute %v: %v (stderr=%s)", args, err, errOut)
	}
	return out
}

func newLoopFake() *fakeCLIAgentMesh {
	return &fakeCLIAgentMesh{token: "tok-X", participant: "helper:abc"}
}

// Without --to the agent schedules a message to itself: the target stays empty and the Service, which knows
// the actor from the token, resolves it. The CLI never names the caller itself.
func TestAgentLoopCLI_AddWithoutTargetSchedulesSelf(t *testing.T) {
	fake := newLoopFake()
	out := runAgentLoop(t, fake, "", "add", "--every", "5m", "--prompt", "check the logs", "--request-id", "rq-1")

	want := server.MeshLoopCommand{Action: "add", Every: 5 * time.Minute, Prompt: "check the logs"}
	if fake.lastLoopReq != "rq-1" || fake.lastLoopCmd != want {
		t.Fatalf("Service got %q %+v, want rq-1 %+v", fake.lastLoopReq, fake.lastLoopCmd, want)
	}
	var view orchestrator.LoopView
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		t.Fatalf("stdout = %q: %v", out, err)
	}
	if view.ID != "L1" || view.CreatorID != "helper:abc" || view.TargetID != "helper:abc" || view.Status != orchestrator.LoopActive {
		t.Fatalf("stdout view = %+v, want L1 created by and aimed at the token's participant", view)
	}
	if strings.Count(strings.TrimSpace(out), "\n") != 0 {
		t.Fatalf("stdout must be exactly one JSON object, got %q", out)
	}
}

func TestAgentLoopCLI_AddExplicitTargetBareOrAlias(t *testing.T) {
	for _, target := range []string{"docs", "@docs", "run:t-9"} {
		fake := newLoopFake()
		runAgentLoop(t, fake, "", "add", "--every", "30s", "--prompt", "ping", "--to", target)
		if fake.lastLoopCmd.Target != target || fake.lastLoopCmd.Every != 30*time.Second {
			t.Errorf("--to %s: Service got %+v, want the target passed through untouched", target, fake.lastLoopCmd)
		}
	}
}

func TestAgentLoopCLI_AddUntilStage(t *testing.T) {
	fake := newLoopFake()
	runAgentLoop(t, fake, "", "add", "--every", "1m", "--prompt", "p", "--until-stage", "implement")
	if fake.lastLoopCmd.UntilStage != "implement" {
		t.Fatalf("Service got %+v, want until_stage implement", fake.lastLoopCmd)
	}
}

// The origin stage is the server's to derive for an agent: the CLI has no flag for it, and sends none.
func TestAgentLoopCLI_AddNeverSendsOriginStage(t *testing.T) {
	fake := newLoopFake()
	runAgentLoop(t, fake, "", "add", "--every", "1m", "--prompt", "p")
	if fake.lastLoopCmd.OriginStage != "" {
		t.Fatalf("Service got origin %q, want none", fake.lastLoopCmd.OriginStage)
	}
	if _, _, err := execAgentLoop(t, fake, fake.token, fake.participant, "", "add", "--every", "1m", "--prompt", "p", "--origin-stage", "x"); err == nil {
		t.Fatal("--origin-stage must not exist on the agent CLI")
	}
}

func TestAgentLoopCLI_PromptFromFileAndStdin(t *testing.T) {
	file := filepath.Join(t.TempDir(), "prompt.txt")
	if err := os.WriteFile(file, []byte("from a file\nsecond line"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, stdin string
		args        []string
		want        string
	}{
		{"file", "", []string{"--prompt-file", file}, "from a file\nsecond line"},
		{"stdin", "token=s3cret\nfrom stdin", []string{"--prompt-file", "-"}, "token=s3cret\nfrom stdin"},
	}
	for _, tc := range cases {
		fake := newLoopFake()
		runAgentLoop(t, fake, tc.stdin, append([]string{"add", "--every", "2m"}, tc.args...)...)
		if fake.lastLoopCmd.Prompt != tc.want {
			t.Errorf("%s: Service got prompt %q, want %q", tc.name, fake.lastLoopCmd.Prompt, tc.want)
		}
	}
}

// Every invalid invocation is refused before anything reaches the Service, with one JSON error on stderr.
func TestAgentLoopCLI_InvalidFlagsNeverReachTheService(t *testing.T) {
	file := filepath.Join(t.TempDir(), "prompt.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	tooBig := strings.Repeat("a", agentcomm.MaxLoopPromptBytes+1)
	cases := []struct {
		name, stdin string
		args        []string
		wantCode    string
	}{
		{"no every", "", []string{"add", "--prompt", "p"}, "invalid_flags"},
		{"unparsable every", "", []string{"add", "--every", "soon", "--prompt", "p"}, "invalid_flags"},
		{"no prompt", "", []string{"add", "--every", "1m"}, "invalid_flags"},
		{"blank prompt", "", []string{"add", "--every", "1m", "--prompt", "  "}, "invalid_flags"},
		{"both prompts", "", []string{"add", "--every", "1m", "--prompt", "p", "--prompt-file", file}, "invalid_flags"},
		{"missing prompt file", "", []string{"add", "--every", "1m", "--prompt-file", file + ".none"}, "invalid_flags"},
		{"empty stdin", "", []string{"add", "--every", "1m", "--prompt-file", "-"}, "invalid_flags"},
		{"oversized stdin", tooBig, []string{"add", "--every", "1m", "--prompt-file", "-"}, "invalid_flags"},
		{"oversized flag", "", []string{"add", "--every", "1m", "--prompt", tooBig}, "invalid_flags"},
		{"stop without id", "", []string{"stop"}, "invalid_flags"},
	}
	for _, tc := range cases {
		fake := newLoopFake()
		out, errOut, err := execAgentLoop(t, fake, fake.token, fake.participant, tc.stdin, tc.args...)
		if err == nil || out != "" {
			t.Errorf("%s: err=%v stdout=%q, want a failure with empty stdout", tc.name, err, out)
			continue
		}
		var payload map[string]string
		if jerr := json.Unmarshal([]byte(errOut), &payload); jerr != nil || payload["error"] != tc.wantCode || payload["message"] == "" {
			t.Errorf("%s: stderr = %q, want one JSON error %q with a message", tc.name, errOut, tc.wantCode)
		}
		if fake.lastLoopCall != 0 {
			t.Errorf("%s: the Service was called", tc.name)
		}
	}
}

func TestAgentLoopCLI_ListPrintsLoopsJSON(t *testing.T) {
	fake := newLoopFake()
	next := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	fake.loops = []orchestrator.LoopView{
		{ID: "L1", CreatorID: "helper:abc", TargetID: "helper:abc", Every: "5m", UntilStage: "implement", Status: orchestrator.LoopActive, NextTickAt: &next},
		{ID: "L2", CreatorID: "helper:abc", TargetID: "run:docs", Every: "1m", Status: orchestrator.LoopStopped, EndReason: "stopped by helper:abc"},
	}
	out := runAgentLoop(t, fake, "", "list")

	var page orchestrator.LoopPage
	if err := json.Unmarshal([]byte(out), &page); err != nil {
		t.Fatalf("stdout = %q: %v", out, err)
	}
	if len(page.Loops) != 2 || page.Loops[0].ID != "L1" || page.Loops[0].UntilStage != "implement" ||
		page.Loops[0].NextTickAt == nil || !page.Loops[0].NextTickAt.Equal(next) || page.Loops[1].EndReason != "stopped by helper:abc" {
		t.Fatalf("loops = %+v, want both views with their fields", page.Loops)
	}
}

func TestAgentLoopCLI_ListEmptyIsAnEmptyArray(t *testing.T) {
	out := runAgentLoop(t, newLoopFake(), "", "list")
	if !strings.Contains(out, `"loops":[]`) {
		t.Fatalf("stdout = %q, want an empty loops array", out)
	}
}

func TestAgentLoopCLI_StopSendsLoopIDAndRequestID(t *testing.T) {
	fake := newLoopFake()
	out := runAgentLoop(t, fake, "", "stop", "--loop-id", "L3", "--request-id", "rq-9")

	want := server.MeshLoopCommand{Action: "stop", LoopID: "L3"}
	if fake.lastLoopReq != "rq-9" || fake.lastLoopCmd != want {
		t.Fatalf("Service got %q %+v, want rq-9 %+v", fake.lastLoopReq, fake.lastLoopCmd, want)
	}
	var view orchestrator.LoopView
	if err := json.Unmarshal([]byte(out), &view); err != nil || view.ID != "L1" {
		t.Fatalf("stdout = %q (%v), want the loop view", out, err)
	}
}

// A mutation that is not given --request-id still carries a fresh one, so the HTTP layer accepts it; two
// invocations never share it.
func TestAgentLoopCLI_MutationsCarryAFreshRequestID(t *testing.T) {
	fake := newLoopFake()
	runAgentLoop(t, fake, "", "add", "--every", "1m", "--prompt", "p")
	first := fake.lastLoopReq
	runAgentLoop(t, fake, "", "stop", "--loop-id", "L1")
	if !strings.HasPrefix(first, "cli-") || fake.lastLoopReq == first || !strings.HasPrefix(fake.lastLoopReq, "cli-") {
		t.Fatalf("request ids %q, %q: want two distinct generated ids", first, fake.lastLoopReq)
	}
}

// The server's reason is printed, so the agent learns what to fix.
func TestAgentLoopCLI_ServerRejectionPrintsCodeAndReason(t *testing.T) {
	mesh := loopRejectingMesh{newLoopFake(), &state.OperationRejectedError{
		Code: assistants.CodeNotFound, Reason: "unknown loop L7",
	}}
	_, errOut, err := execAgentLoop(t, mesh, "tok-X", "helper:abc", "", "stop", "--loop-id", "L7")
	if err == nil {
		t.Fatal("Execute: want an error for a rejected stop")
	}
	var payload map[string]string
	if jerr := json.Unmarshal([]byte(errOut), &payload); jerr != nil {
		t.Fatalf("stderr must be one JSON object: %v (got %q)", jerr, errOut)
	}
	if payload["error"] != "not_found" || !strings.Contains(payload["message"], "unknown loop L7") {
		t.Fatalf("stderr = %v, want not_found with the server's reason", payload)
	}
}

// The bearer token is this invocation's own: the participant is whoever the token resolves to, and a token
// the server does not know changes nothing.
func TestAgentLoopCLI_TokenIdentifiesTheCaller(t *testing.T) {
	fake := &fakeCLIAgentMesh{token: "tok-for-b", participant: "run:b"}
	out := runAgentLoop(t, fake, "", "add", "--every", "1m", "--prompt", "p")
	if !strings.Contains(out, `"creator_id":"run:b"`) {
		t.Fatalf("stdout = %q, want the loop created by the token's participant", out)
	}

	stale := newLoopFake()
	_, errOut, err := execAgentLoop(t, stale, "tok-OLD", "helper:abc", "", "add", "--every", "1m", "--prompt", "p")
	if err == nil || !strings.Contains(errOut, `"error":"unauthorized"`) || stale.lastLoopCall != 0 {
		t.Fatalf("err=%v stderr=%q calls=%d, want unauthorized and no Service call", err, errOut, stale.lastLoopCall)
	}
}

func TestAgentLoopCLI_RegisteredUnderAgentCmd(t *testing.T) {
	loop, _, err := newAgentCmd().Find([]string{"loop"})
	if err != nil || loop.Name() != "loop" {
		t.Fatalf("afm agent loop is not registered: %v", err)
	}
}
