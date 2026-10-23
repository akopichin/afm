package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/config"
	"github.com/akopichin/afm/pkg/flow"
	"github.com/akopichin/afm/pkg/orchestrator"
	"github.com/akopichin/afm/pkg/server"
	"github.com/akopichin/afm/pkg/state"
)

// writeLimitedCodexHelper writes a "codex-as-claude" script whose every turn hits the claude-style usage limit
// (the structured rate_limit_event + api_error the executor recognises), resetting an hour from now.
func writeLimitedCodexHelper(t *testing.T, resetAt time.Time) string {
	t.Helper()
	script := "#!/bin/sh\ncat >/dev/null\n" +
		`echo '{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":` + strconv.FormatInt(resetAt.Unix(), 10) + `,"rateLimitType":"five_hour"}}'` + "\n" +
		`echo '{"type":"result","subtype":"success","is_error":true,"api_error":"usage_limit_reached","result":"You hit your limit key=sk-secret-token-123"}'` + "\n"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "codex-as-claude"), []byte(script), 0o755); err != nil { //nolint:gosec // test fixture: an executable fake helper
		t.Fatal(err)
	}
	return dir
}

// A helper task that hit a usage limit sits in queued; the dashboard reader gives it the SAME small quota view as a
// stage (provider/window/source/until), not the journal's whole QuotaWait.
func TestCollaborationReader_ParkedHelperExposesTheSmallQuotaView(t *testing.T) {
	resetAt := time.Now().Add(time.Hour).Truncate(time.Second)
	t.Setenv("PATH", writeLimitedCodexHelper(t, resetAt)+string(os.PathListSeparator)+os.Getenv("PATH"))

	runDir := t.TempDir()
	const stageID = "work"
	store, err := state.Open(runDir, []string{stageID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	enabled := true
	cfg := config.Default()
	cfg.Collaboration = config.CollaborationConfig{
		Enabled:  &enabled,
		Profiles: map[string]config.ProfileConfig{"reviewer": {Title: "Reviewer", Command: "codex-as-claude", Instructions: "Review."}},
	}
	collab := startCollaboration(store, cfg, &flow.Flow{Name: "quota"})
	runner := &fakeAutoRunner{acquired: make(chan string, 1), release: make(chan struct{})}
	orch := orchestrator.New(orchestrator.Options{
		RunDir: runDir, Stages: []flow.Stage{{ID: stageID, Name: "Work", Agents: []flow.AgentType{flow.AgentAuto}}},
		Store: store, Config: cfg, Prompts: orchestrator.DefaultPrompts(), Runner: runner,
		AssistantsEnabled: collab.policy.Enabled, AssistantsRegistry: collab.registry,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- orch.Run(ctx) }()
	select {
	case <-runner.acquired:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the stage to start")
	}

	taskID, err := collab.actions(orch).Attach(ctx, stageID, server.AttachRequest{RequestID: "req-1", ProfileID: "reviewer", Title: "Check", Instruction: "Look."})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	var view server.AssistantTaskView
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		tree, err := collab.reader(orch).TaskTree(stageID)
		if err != nil {
			t.Fatal(err)
		}
		for _, tv := range tree {
			if tv.ID == taskID && tv.Quota != nil {
				view = tv
			}
		}
		if view.Quota != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if view.Quota == nil {
		t.Fatalf("a helper parked for a usage limit must expose quota (task %s)", taskID)
	}
	if view.Status != string(assistants.StatusQueued) {
		t.Errorf("status = %q, want queued", view.Status)
	}
	if view.Quota.Provider == "" || view.Quota.Source != state.QuotaSourceStructured || view.Quota.Window != "five_hour" ||
		!view.Quota.ResetAt.Equal(resetAt.UTC()) || view.Quota.Until.Before(resetAt) {
		t.Errorf("quota = %+v, want structured five_hour with reset %s and until after it", view.Quota, resetAt)
	}
	raw, err := json.Marshal(view.Quota)
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]any
	_ = json.Unmarshal(raw, &keys)
	for k := range keys {
		switch k {
		case "provider", "window", "source", "until", "reset_at", "pause_reason":
		default:
			t.Errorf("quota JSON carries journal-only key %q: %s", k, raw)
		}
	}

	cancel() // the wait is an hour: stop the run, the park stays in the journal
	select {
	case <-runDone:
	case <-time.After(10 * time.Second):
		t.Fatal("orch.Run did not return after cancel")
	}
}
