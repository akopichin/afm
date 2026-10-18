package orchestrator

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/agentcomm"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/state"
)

func publishAsUser(o *Orchestrator, opID, key, status string, expectedVersion int, notify ...agentcomm.ParticipantID) (agentcomm.BoardEntry, error) {
	return o.Publish(context.Background(), PublishRequest{
		OperationID: opID, Author: meshUserParticipantID, Key: key, Status: status, ExpectedVersion: expectedVersion,
		Notify: notify, Actor: state.Actor{Kind: "user"},
	})
}

// A board task with notify reaches an idle run agent: one message from the human starts its turn, and a
// retried request does not send a second one.
func TestPublish_NotifyWakesIdleRunAgent(t *testing.T) {
	o, store := newAssistantsTestOrchestrator(t, true)
	var mu sync.Mutex
	var prompts []string
	o.runHelperAgent = func(ctx context.Context, taskID, profileID, cmd, prompt, logFile, resultFile string) (executor.HelperOutcome, error) {
		mu.Lock()
		defer mu.Unlock()
		prompts = append(prompts, prompt)
		return passResultOutcome(), nil
	}
	res := addTestRunAgent(t, o, "op-add", nil)

	for range 2 { // the second call is a retry of the same request
		if _, err := publishAsUser(o, "op-task", "task/csv-export", "CSV export", 0, res.ParticipantID); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}
	waitForRunAgentTurn(t, store, res.TaskID, 2*time.Second)

	inbox := store.Snapshot().Assistants.Mesh.Inbox[res.ParticipantID]
	if len(inbox) != 1 {
		t.Fatalf("messages to the agent = %d, want exactly 1", len(inbox))
	}
	want := "New task on the board: task/csv-export — CSV export. Report under task/csv-export/<your alias>."
	if body := store.Snapshot().Assistants.Mesh.Messages[inbox[0]].Body; body != want {
		t.Fatalf("notification = %q, want %q", body, want)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(prompts) != 1 || !strings.Contains(prompts[0], "task/csv-export") {
		t.Fatalf("agent turns = %d, want one turn that sees the task", len(prompts))
	}
}

// Publish answers with the revision its own operation committed — also on a replay, after a newer revision.
func TestPublish_ReplayReturnsItsOwnRevision(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	first, err := publishAsUser(o, "op-v1", "decision", "draft", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publishAsUser(o, "op-v2", "decision", "final", 1); err != nil {
		t.Fatal(err)
	}
	replay, err := publishAsUser(o, "op-v1", "decision", "draft", 0)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replay.Version != 1 || replay.Status != "draft" || replay.Seq != first.Seq || !replay.Ts.Equal(first.Ts) {
		t.Fatalf("replay = %+v, want the first revision %+v", replay, first)
	}
}

// A board status is redacted like a message body: in the journal, the response and the notice.
func TestPublish_RedactsSecretsInStatus(t *testing.T) {
	const secret = "sk-test-board-secret-424242" //nolint:gosec // test fixture value, not a real credential
	t.Setenv("ANTHROPIC_API_KEY", secret)
	o, store := newAssistantsTestOrchestrator(t, true)
	_, events := o.ui.Subscribe(16)

	entry, err := publishAsUser(o, "op-secret", "creds", "token is "+secret, 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(entry.Status, secret) || strings.Contains(store.Snapshot().Assistants.Mesh.Board["creds"].Status, secret) {
		t.Fatalf("secret leaked into the board entry: %q", entry.Status)
	}
	select {
	case ev := <-events:
		data, _ := ev.Data.(map[string]any)
		if s, _ := data["status"].(string); strings.Contains(s, secret) {
			t.Fatalf("secret leaked into the notice: %q", s)
		}
	case <-time.After(time.Second):
		t.Fatal("no board notice published")
	}
}
