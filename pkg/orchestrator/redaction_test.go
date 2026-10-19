package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/state"
)

// Secrets never reach the journal, a task's files or its logs: every free-text field an agent or a human hands
// to afm goes through o.RedactSecrets (pkg/redact) before it is written.

const redactionTestSecret = "s3cr3t-redaction-value" //nolint:gosec // test fixture value, not a real credential

func newRedactingTestOrchestrator(t *testing.T) (*Orchestrator, *state.Store) {
	t.Helper()
	o, store := newAssistantsTestOrchestrator(t, true)
	o.opts.SecretValues = []string{redactionTestSecret}
	activateStage(t, o, assistantsTestStageID)
	return o, store
}

// assertNoSecretIn fails when any of files contains the secret.
func assertNoSecretIn(t *testing.T, files ...string) {
	t.Helper()
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if strings.Contains(string(raw), redactionTestSecret) {
			t.Fatalf("%s carries the secret:\n%s", f, raw)
		}
	}
}

func journalOf(o *Orchestrator) string { return filepath.Join(o.opts.RunDir, "events.jsonl") }

// TestRedaction_TaskCreatedTitleAndInstruction: a task's title and instruction are redacted in task_created,
// and a retry of the same request is still the same request, not a conflict.
func TestRedaction_TaskCreatedTitleAndInstruction(t *testing.T) {
	o, store := newRedactingTestOrchestrator(t)
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		return passResultOutcome(), nil
	}
	req := AttachRequest{
		OperationID: "op-red-task", StageID: assistantsTestStageID, ProfileID: "reviewer",
		Title: "check " + redactionTestSecret, Instruction: "use token " + redactionTestSecret, Actor: state.Actor{Kind: "user"},
	}
	taskID, err := o.Attach(context.Background(), req)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)
	o.concurrency.WaitAgents()

	assertNoSecretIn(t, journalOf(o), filepath.Join(o.taskDir(taskID), "context.md"))
	if again, err := o.Attach(context.Background(), req); err != nil || again != taskID {
		t.Fatalf("retry of the same attach = %s, %v; want %s", again, err, taskID)
	}
}

// TestRedaction_TaskResult: every free-text field of a helper's result is redacted in result.json and in the
// task_result record.
func TestRedaction_TaskResult(t *testing.T) {
	o, store := newRedactingTestOrchestrator(t)
	s := redactionTestSecret
	raw := `{"schema_version":1,"kind":"result","verdict":"needs_changes","summary":"saw ` + s + `",` +
		`"findings":[{"id":"f-` + s + `","severity":"major","path":"a/` + s + `.go","detail":"leaks ` + s + `"}],` +
		`"evidence":["log line ` + s + `"],"suggested_change":"rotate ` + s + `","limitations":"only checked ` + s + `"}`
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		return executor.HelperOutcome{ProcessOK: true, RawText: raw}, nil
	}
	taskID := attachTestTask(context.Background(), t, o, "op-red-result")
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 2*time.Second)
	o.concurrency.WaitAgents()

	assertNoSecretIn(t, journalOf(o), filepath.Join(o.taskDir(taskID), "result.json"))
}

// TestRedaction_ArtifactRefs: a message's and a board entry's artifact references are redacted, and a retry
// carrying the same references is not a conflict.
func TestRedaction_ArtifactRefs(t *testing.T) {
	o, store := newRedactingTestOrchestrator(t)
	helper := meshHelperParticipantID(newRunningHelperForMesh(t, o, store, "op-red-refs-helper"))
	ctx := context.Background()
	if _, err := o.OpenThread(ctx, OpenThreadRequest{
		OperationID: "op-red-refs-open", Members: []agentcomm.ParticipantID{meshUserParticipantID, helper}, Scope: agentcomm.ScopeStage,
	}); err != nil {
		t.Fatalf("OpenThread: %v", err)
	}
	refs := []string{"artifacts/" + redactionTestSecret + ".md"}
	send := SendRequest{
		OperationID: "op-red-refs-send", ThreadID: deriveMeshThreadID("op-red-refs-open"), Sender: meshUserParticipantID,
		Recipients: []agentcomm.ParticipantID{helper}, Kind: agentcomm.MessageKindMessage, Body: "see refs", ArtifactRefs: refs,
	}
	if _, err := o.Send(ctx, send); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if _, err := o.Send(ctx, send); err != nil {
		t.Fatalf("retry of the same send: %v", err)
	}
	publish := PublishRequest{
		OperationID: "op-red-refs-pub", Author: meshUserParticipantID, Key: "report", Status: "done", ArtifactRefs: refs,
		Actor: state.Actor{Kind: "user"},
	}
	entry, err := o.Publish(ctx, publish)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if strings.Contains(strings.Join(entry.ArtifactRefs, ","), redactionTestSecret) {
		t.Fatalf("the published entry carries the secret: %v", entry.ArtifactRefs)
	}
	if _, err := o.Publish(ctx, publish); err != nil {
		t.Fatalf("retry of the same publish: %v", err)
	}
	assertNoSecretIn(t, journalOf(o))
}

// TestRedaction_HelperLogsOnRealExecutor: what a helper prints — a tool call, stderr, its final answer — is
// redacted in agent.log, agent.jsonl, agent.stderr.log and the raw answer file, on the real executor.
func TestRedaction_HelperLogsOnRealExecutor(t *testing.T) {
	o, store := newRedactingTestOrchestrator(t)
	dir := t.TempDir()
	s := redactionTestSecret
	toolCall := mustJSONLine(t, map[string]any{"type": "assistant", "message": map[string]any{"content": []map[string]any{
		{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "echo " + s}},
	}}})
	answer := mustJSONLine(t, map[string]any{"type": "assistant", "message": map[string]any{"content": []map[string]any{
		{"type": "text", "text": `{"schema_version":1,"kind":"result","verdict":"pass","summary":"saw ` + s + `"}`},
	}}})
	script := filepath.Join(dir, "agent.sh")
	if err := os.WriteFile(script, []byte("#!/bin/bash\n"+
		"cat <<'EOF'\n"+toolCall+"\nEOF\n"+
		"echo 'stderr carries "+s+"' >&2\n"+
		"cat <<'EOF'\n"+answer+"\nEOF\n"+
		`echo '{"type":"result","subtype":"success"}'`+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, _, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		return o.execHelperAgent(ctx, taskID, profileID, script, prompt, logFile, resultFile)
	}

	taskID := attachTestTask(context.Background(), t, o, "op-red-logs")
	waitForTaskStatus(t, store, taskID, assistants.StatusCompleted, 5*time.Second)
	o.concurrency.WaitAgents()

	taskDir := o.taskDir(taskID)
	assertNoSecretIn(t, journalOf(o),
		filepath.Join(taskDir, "agent.log"), filepath.Join(taskDir, "agent.jsonl"), filepath.Join(taskDir, "agent.stderr.log"),
		filepath.Join(taskDir, rawResultFileName), filepath.Join(taskDir, "result.json"))
}
