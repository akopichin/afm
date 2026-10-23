package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/flow"
)

// quota_warning: provider says the limit is close. The event is not an agent action, so every executor.Config an
// agent runs under (author, interactive author, verifier, helper) carries the narrow OnLimitWarning; the notice is
// deduped and never carries the agent's raw output.

const warnReset = 1792713000 // 2026-10-22T23:50:00Z

// warningAgentScript writes an executable that emits an allowed_warning and a successful result, like the claude CLI.
func warningAgentScript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-claude")
	body := "#!/bin/sh\ncat >/dev/null\n" +
		`echo '{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning","resetsAt":` + "1792713000" + `,"rateLimitType":"five_hour","utilization":0.92}}'` + "\n" +
		`echo '{"type":"result","subtype":"success","is_error":false,"result":"ok key=sk-secret-token-123"}'` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil { //nolint:gosec // test fixture: an executable fake agent
		t.Fatal(err)
	}
	return path
}

// noticesOf — строки notices.jsonl данного типа.
func noticesOf(t *testing.T, o *Orchestrator, typ string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(o.opts.RunDir, "notices.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var rec struct {
			Type    string         `json:"type"`
			StageID string         `json:"stage_id"`
			Data    map[string]any `json:"data"`
		}
		if json.Unmarshal([]byte(line), &rec) == nil && rec.Type == typ {
			rec.Data["_stage"] = rec.StageID // notice's own stage field, next to its data
			out = append(out, rec.Data)
		}
	}
	return out
}

func checkWarningNotice(t *testing.T, o *Orchestrator, wantKey, wantID string) {
	t.Helper()
	got := noticesOf(t, o, "quota_warning")
	if len(got) != 1 {
		t.Fatalf("quota_warning notices = %d, want 1: %v", len(got), got)
	}
	d := got[0]
	if d[wantKey] != wantID || d["window"] != "five_hour" || d["reset_at"] != "2026-10-22T23:50:00Z" || d["provider"] == "" {
		t.Errorf("warning data = %v, want %s=%s window five_hour reset_at 2026-10-22T23:50:00Z and a provider", d, wantKey, wantID)
	}
	if msg, _ := d["message"].(string); !strings.Contains(msg, "92%") || !strings.Contains(msg, "UTC") {
		t.Errorf("message %q must say how close the limit is and name the zone", msg)
	}
	raw, _ := json.Marshal(d)
	if strings.Contains(string(raw), "sk-secret-token-123") {
		t.Errorf("notice leaked agent output: %s", raw)
	}
	if d["_stage"] != "s1" && d["_stage"] != assistantsTestStageID {
		t.Errorf("notice filed under stage %v, want the agent's (root) stage", d["_stage"])
	}
}

func TestLimitWarning_StageAuthorConfigsPublishNotice(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		name := "non-interactive"
		if interactive {
			name = "interactive"
		}
		t.Run(name, func(t *testing.T) {
			o := newTestOrchestrator(t)
			s := flow.Stage{ID: "s1", Name: "Stage", Interactive: interactive, Command: warningAgentScript(t)}
			if err := os.MkdirAll(o.stageDir(s.ID), 0o755); err != nil {
				t.Fatal(err)
			}
			logFile := filepath.Join(o.stageDir(s.ID), "implementation.log")
			if err := o.runnerFor(s, phaseImplementation).RunAgent(context.Background(), phaseImplementation, s.Name, "go", logFile); err != nil {
				t.Fatalf("RunAgent: %v", err)
			}
			checkWarningNotice(t, o, "stage_id", "s1")
		})
	}
}

func TestLimitWarning_VerifyAgentConfigPublishesNotice(t *testing.T) {
	o := newTestOrchestrator(t)
	s := flow.Stage{ID: "s1", Name: "Stage"}
	dir := o.stageDir(s.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := warningAgentScript(t)
	_, _ = o.runnerForVerify(s, cmd).RunVerifyAgent(context.Background(), cmd, s.Name, "verify", filepath.Join(dir, "v.log"), filepath.Join(dir, "r.json"))
	checkWarningNotice(t, o, "stage_id", "s1")
}

func TestLimitWarning_HelperConfigPublishesNoticeWithTaskID(t *testing.T) {
	o, _ := newAssistantsTestOrchestrator(t, true)
	o.runHelperAgent = func(context.Context, string, string, string, string, string, string) (executor.HelperOutcome, error) {
		return passResultOutcome(), nil
	}
	res := addTestRunAgent(t, o, "op-warn", nil)
	cmd := warningAgentScript(t)
	dir := t.TempDir()
	cfg := o.helperExecutorConfig(res.TaskID, cmd)
	_, _ = executor.New(cfg).RunHelperAgent(context.Background(), string(res.TaskID), "qa", "go", filepath.Join(dir, "h.log"), filepath.Join(dir, "r.json"))

	got := noticesOf(t, o, "quota_warning")
	if len(got) != 1 {
		t.Fatalf("quota_warning notices = %d, want 1: %v", len(got), got)
	}
	if got[0]["task_id"] != string(res.TaskID) {
		t.Errorf("helper notice must carry task_id %s: %v", res.TaskID, got[0])
	}
	if _, has := got[0]["stage_id"]; has {
		t.Errorf("a helper notice carries task_id instead of stage_id: %v", got[0])
	}
	if root := o.opts.Store.Snapshot().Assistants.Tasks[res.TaskID].RootStageID; got[0]["_stage"] != root {
		t.Errorf("helper notice filed under %v, want its root stage %q", got[0]["_stage"], root)
	}
}

func TestLimitWarning_DedupedPerProviderWindowAndReset(t *testing.T) {
	o := newTestOrchestrator(t)
	sink := o.limitWarningSink("s1", "")
	reset := time.Unix(warnReset, 0).UTC()
	w := executor.LimitWarning{Provider: "claude", Window: "five_hour", ResetAt: reset, Utilization: 0.9}

	for range 3 {
		sink(w) // the provider repeats the same event on every turn
	}
	if n := len(noticesOf(t, o, "quota_warning")); n != 1 {
		t.Fatalf("same warning thrice = %d notices, want 1", n)
	}
	// Another stage's agent of the same provider and window: still one warning for the run.
	o.limitWarningSink("s2", "")(w)
	if n := len(noticesOf(t, o, "quota_warning")); n != 1 {
		t.Fatalf("same (provider, window) from another agent = %d notices, want 1", n)
	}
	w2 := w
	w2.Window = "seven_day"
	sink(w2)
	w3 := w
	w3.ResetAt = reset.Add(5 * time.Hour) // the next window after a reset warns again
	sink(w3)
	w4 := w
	w4.Provider = "codex"
	sink(w4)
	if n := len(noticesOf(t, o, "quota_warning")); n != 4 {
		t.Fatalf("distinct window/reset/provider = %d notices, want 4", n)
	}
}

func TestLimitWarning_NoticeIsNotAnAction(t *testing.T) {
	// A warning must not reach the action feed: OnLimitWarning is a separate callback.
	o := newTestOrchestrator(t)
	_, ui := o.UIBus().Subscribe(64)
	s := flow.Stage{ID: "s1", Name: "Stage", Command: warningAgentScript(t)}
	if err := os.MkdirAll(o.stageDir(s.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := o.runnerFor(s, phaseImplementation).RunAgent(context.Background(), phaseImplementation, s.Name, "go", filepath.Join(o.stageDir(s.ID), "implementation.log")); err != nil {
		t.Fatal(err)
	}
	var sawWarning bool
	for len(ui) > 0 {
		ev := <-ui
		if string(ev.Type) == "quota_warning" {
			sawWarning = true
		}
	}
	if !sawWarning {
		t.Error("live quota_warning missing from the UI bus")
	}
}

// helper quota_wait / quota_reset: the park and the wake of a helper task are announced with task_id.
func TestHelperQuota_NoticesCarryTaskIDAndCloseTheWait(t *testing.T) {
	o, store, clk := newQuotaOrch(t)
	h := limitFirst(holdNow.Add(2 * time.Minute))
	o.runHelperAgent = h.run
	msg := "please review"
	res := addTestRunAgent(t, o, "op-notice", &msg)

	parked := waitForQuotaPark(t, store, res.TaskID)
	waitUntil(t, "the park is announced", func() bool { return len(noticesOf(t, o, "quota_wait")) > 0 })
	waits := noticesOf(t, o, "quota_wait")
	if len(waits) != 1 || waits[0]["task_id"] != string(res.TaskID) || waits[0]["provider"] != quotaProvider ||
		waits[0]["until"] != parked.Quota.Until.UTC().Format(time.RFC3339) {
		t.Fatalf("helper quota_wait = %v, want one for %s with the journaled until", waits, res.TaskID)
	}
	if _, has := waits[0]["stage_id"]; has {
		t.Errorf("a helper notice carries task_id instead of stage_id: %v", waits[0])
	}
	if n := len(noticesOf(t, o, "quota_reset")); n != 0 {
		t.Fatalf("quota_reset before the wake = %d", n)
	}

	clk.BlockUntil(1)
	advanceUntil(t, clk, time.Minute, "the helper wakes", func() bool { return h.calls.Load() >= 2 })
	waitUntil(t, "the helper's wake is announced", func() bool { return len(noticesOf(t, o, "quota_reset")) == 1 })
	resets := noticesOf(t, o, "quota_reset")
	if resets[0]["task_id"] != string(res.TaskID) {
		t.Errorf("helper quota_reset = %v", resets[0])
	}
	waitForRunAgentTurn(t, store, res.TaskID, 5*time.Second)
	if n := len(noticesOf(t, o, "quota_reset")); n != 1 {
		t.Errorf("quota_reset total = %d, want 1 (a later turn start is not a wake)", n)
	}
}
