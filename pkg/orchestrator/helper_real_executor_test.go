package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
)

// These tests run the REAL helper executor (execHelperAgent → executor.RunHelperAgent) on a stub agent script:
// the stub prints Claude stream-json and no API key is involved. The first turn yields — it ends after a tool
// call with no final text, exactly what an agent prints after `afm agent wait` said "yield" — and every later
// turn answers with a valid result.

// yieldingAgent is a stub helper agent driven through files in its own directory.
type yieldingAgent struct {
	t      *testing.T
	dir    string
	script string
}

func newYieldingAgent(t *testing.T) *yieldingAgent {
	t.Helper()
	dir := t.TempDir()
	toolCall := mustJSONLine(t, map[string]any{"type": "assistant", "message": map[string]any{"content": []map[string]any{
		{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "afm agent wait --condition addressed_message"}},
	}}})
	answer := mustJSONLine(t, map[string]any{"type": "assistant", "message": map[string]any{"content": []map[string]any{
		{"type": "text", "text": `{"schema_version":1,"kind":"result","verdict":"pass","summary":"looks fine"}`},
	}}})
	script := "#!/bin/bash\n" +
		"dir='" + dir + "'\n" +
		`n=$(cat "$dir/turns" 2>/dev/null || echo 0); n=$((n+1)); echo "$n" > "$dir/turns"` + "\n" +
		`if [ "$n" -eq 1 ]; then` + "\n" +
		`  touch "$dir/started"` + "\n" +
		`  while [ ! -e "$dir/release" ]; do sleep 0.01; done` + "\n" +
		"  cat <<'EOF'\n" + toolCall + "\nEOF\n" +
		`  echo '{"type":"result","subtype":"success"}'` + "\n" +
		"  exit 0\n" +
		"fi\n" +
		"cat <<'EOF'\n" + answer + "\nEOF\n" +
		`echo '{"type":"result","subtype":"success"}'` + "\n"
	path := filepath.Join(dir, "agent.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return &yieldingAgent{t: t, dir: dir, script: path}
}

func mustJSONLine(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// useRealExecutor runs every helper turn of o through the production executor with the stub as its command.
func (a *yieldingAgent) useRealExecutor(o *Orchestrator) {
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, _, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return o.execHelperAgent(ctx, taskID, profileID, a.script, prompt, logFile, resultFile)
	}
}

func (a *yieldingAgent) waitStarted() {
	a.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(a.dir, "started")); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	a.t.Fatal("the stub agent never started its first turn")
}

func (a *yieldingAgent) release() {
	a.t.Helper()
	if err := os.WriteFile(filepath.Join(a.dir, "release"), nil, 0o644); err != nil {
		a.t.Fatal(err)
	}
}

func (a *yieldingAgent) turns() int {
	raw, err := os.ReadFile(filepath.Join(a.dir, "turns"))
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	return n
}

// registerHelperWait registers, on behalf of the helper, the wait its `afm agent wait` call would: a message
// to it in thread.
func registerHelperWait(t *testing.T, o *Orchestrator, helperID agentcomm.ParticipantID, thread agentcomm.ThreadID) {
	t.Helper()
	if _, err := o.Wait(context.Background(), WaitRequest{
		OperationID: "op-yield-wait", Waiter: helperID,
		Condition: agentcomm.WaitCondition{Kind: agentcomm.WaitOnAddressedMessage, ThreadID: thread}, Deadline: testFarDeadline(),
	}); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

// replyToHelper opens a thread between the human and the helper and sends the helper one message in it.
func replyToHelper(t *testing.T, o *Orchestrator, helperID agentcomm.ParticipantID) {
	t.Helper()
	ctx := context.Background()
	user := agentcomm.ParticipantID("user")
	if _, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-yield-open", Members: []agentcomm.ParticipantID{helperID, user}, Scope: agentcomm.ScopeStage,
	}); err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	if _, err := o.Send(ctx, SendRequest{
		OperationID: "op-yield-send", ThreadID: deriveMeshThreadID("op-yield-open"), Sender: user,
		Recipients: []agentcomm.ParticipantID{helperID}, Kind: agentcomm.MessageKindMessage, Body: "here is the answer",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
}

// TestHelperYield_RealExecutor_NoFinalTextParksNotProtocolError: a helper that yields into a mesh wait prints no
// final text, so the executor reports a protocol error — the yield must be classified first: the task parks in
// waiting_peer and its next turn, after the reply, completes it.
func TestHelperYield_RealExecutor_NoFinalTextParksNotProtocolError(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	agent := newYieldingAgent(t)
	agent.useRealExecutor(o)

	taskID := deriveTaskID("op-yield-real")
	helperID := meshHelperParticipantID(taskID)
	attachTestTask(context.Background(), t, o, "op-yield-real")

	agent.waitStarted()
	registerHelperWait(t, o, helperID, deriveMeshThreadID("op-yield-open"))
	agent.release()

	tk := waitForTaskStatus(t, store, taskID, assistants.StatusWaitingPeer, 5*time.Second)
	if strings.Contains(tk.StatusReason, "protocol_error") {
		t.Fatalf("a yield is not a protocol error: %q", tk.StatusReason)
	}

	replyToHelper(t, o, helperID)
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 5*time.Second)
	if got := agent.turns(); got != 2 {
		t.Fatalf("the agent ran %d turns, want 2 (the yield and the resumed turn)", got)
	}
	o.concurrency.WaitAgents()
}

// TestHelperYield_RealExecutor_ReplyBeforeParkResumes closes the ready-before-park race: the reply lands while
// the yielding process is still finishing, so the wait is already ready when the task is classified. The task
// must neither fail with protocol_error nor park in waiting_peer forever — it resumes and completes.
func TestHelperYield_RealExecutor_ReplyBeforeParkResumes(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	agent := newYieldingAgent(t)
	agent.useRealExecutor(o)

	taskID := deriveTaskID("op-yield-race")
	helperID := meshHelperParticipantID(taskID)
	attachTestTask(context.Background(), t, o, "op-yield-race")

	agent.waitStarted()
	registerHelperWait(t, o, helperID, deriveMeshThreadID("op-yield-open"))
	replyToHelper(t, o, helperID) // the wait is ready before the process has even exited
	agent.release()

	tk := waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 5*time.Second)
	if strings.Contains(tk.StatusReason, "protocol_error") {
		t.Fatalf("a yield is not a protocol error: %q", tk.StatusReason)
	}
	if got := agent.turns(); got != 2 {
		t.Fatalf("the agent ran %d turns, want 2 (the yield and the resumed turn)", got)
	}
	o.concurrency.WaitAgents()
}

// TestHelperTurn_RealExecutor_RunsInProjectRootWithScratchEnv: a helper turn runs with the project root as its
// working directory and gets its scratch directory as AFM_SCRATCH_DIR — checked on the real executor.
func TestHelperTurn_RealExecutor_RunsInProjectRootWithScratchEnv(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	root := t.TempDir()
	o.opts.RootDir = root
	out := t.TempDir()
	answer := mustJSONLine(t, map[string]any{"type": "assistant", "message": map[string]any{"content": []map[string]any{
		{"type": "text", "text": `{"schema_version":1,"kind":"result","verdict":"pass","summary":"looks fine"}`},
	}}})
	script := filepath.Join(out, "agent.sh")
	if err := os.WriteFile(script, []byte("#!/bin/bash\n"+
		`pwd -P > '`+out+`/pwd'`+"\n"+
		`printf '%s' "$AFM_SCRATCH_DIR" > '`+out+`/scratch'`+"\n"+
		"cat <<'EOF'\n"+answer+"\nEOF\n"+
		`echo '{"type":"result","subtype":"success"}'`+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, _, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return o.execHelperAgent(ctx, taskID, profileID, script, prompt, logFile, resultFile)
	}

	taskID := attachTestTask(context.Background(), t, o, "op-workspace")
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 5*time.Second)
	o.concurrency.WaitAgents()

	wantRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := readTrimmed(t, filepath.Join(out, "pwd")); got != wantRoot {
		t.Fatalf("helper CWD = %q, want the project root %q", got, wantRoot)
	}
	if got := readTrimmed(t, filepath.Join(out, "scratch")); got != o.taskScratchDir(taskID) {
		t.Fatalf("AFM_SCRATCH_DIR = %q, want %q", got, o.taskScratchDir(taskID))
	}
}

func readTrimmed(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(raw))
}

// TestHelperTurn_RealExecutor_SecretSplitAcrossStderrWritesIsRedacted: the helper prints a known secret to
// stderr across two writes and the 64 KiB flush point of a long line; agent.stderr.log never holds it — the executor redacts the stream knowing the secrets.
func TestHelperTurn_RealExecutor_SecretSplitAcrossStderrWritesIsRedacted(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	activateStage(t, o, assistantsTestStageID)
	const secret = "s3cr3t-token-split-across-writes"
	o.opts.SecretValues = []string{secret}
	out := t.TempDir()
	answer := mustJSONLine(t, map[string]any{"type": "assistant", "message": map[string]any{"content": []map[string]any{
		{"type": "text", "text": `{"schema_version":1,"kind":"result","verdict":"pass","summary":"looks fine"}`},
	}}})
	script := filepath.Join(out, "agent.sh")
	if err := os.WriteFile(script, []byte("#!/bin/bash\n"+
		// A long line: the secret straddles the 64 KiB point where a line-based redactor would flush.
		`{ printf 'token='; head -c 65530 /dev/zero | tr '\0' a; printf '%s' '`+secret[:10]+`'; } >&2`+"\n"+
		"sleep 0.1\n"+
		`printf '%s\n' '`+secret[10:]+`' >&2`+"\n"+
		"cat <<'EOF'\n"+answer+"\nEOF\n"+
		`echo '{"type":"result","subtype":"success"}'`+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, _, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return o.execHelperAgent(ctx, taskID, profileID, script, prompt, logFile, resultFile)
	}

	taskID := attachTestTask(context.Background(), t, o, "op-stderr-secret")
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 5*time.Second)
	o.concurrency.WaitAgents()

	raw, err := os.ReadFile(filepath.Join(o.taskDir(taskID), "agent.stderr.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) || !strings.Contains(string(raw), "token=") {
		t.Fatalf("agent.stderr.log ends with %q, want the stderr line with the secret masked", raw[max(0, len(raw)-80):])
	}
}
