package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	implstate "github.com/akopichin/afm-engine/testkit/state"
	"github.com/akopichin/afm/contracts/state"
	"github.com/spf13/cobra"
)

// Контракт офлайн approve/retry/revise: переход, файлы стадии, точные тексты CLI и отказ активному рану.

const offlineStage = "impl"

// runOffline выполняет команду и возвращает её stdout и ошибку.
func runOffline(t *testing.T, cmd *cobra.Command, args ...string) (string, error) {
	t.Helper()
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs(args)
	var err error
	out := captureStdout(t, func() { err = cmd.Execute() })
	return out, err
}

func readEvents(t *testing.T, runDir string) []state.Transition {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var out []state.Transition
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var tr state.Transition
		if err := json.Unmarshal([]byte(line), &tr); err != nil {
			t.Fatalf("bad event line %q: %v", line, err)
		}
		out = append(out, tr)
	}
	return out
}

// lastTransition проверяет, что команда дописала ровно один переход, и возвращает его.
func lastTransition(t *testing.T, runDir string, wantEvents int) state.Transition {
	t.Helper()
	events := readEvents(t, runDir)
	if len(events) != wantEvents {
		t.Fatalf("events = %d, want %d: %+v", len(events), wantEvents, events)
	}
	return events[len(events)-1]
}

func writePlan(t *testing.T, runDir, plan string) string {
	t.Helper()
	stageDir := filepath.Join(runDir, offlineStage)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "plan.md"), []byte(plan), 0644); err != nil {
		t.Fatal(err)
	}
	return stageDir
}

func TestOfflineApprove(t *testing.T) {
	chdirTemp(t)
	runDir := makeRunState(t, "flow-20260101-120000", offlineStage, state.StatusAwaitingApproval)

	out, err := runOffline(t, newApproveCmd(), offlineStage)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if want := "approved stage \"impl\": ready to run\n"; out != want {
		t.Errorf("stdout = %q, want %q", out, want)
	}
	tr := lastTransition(t, runDir, 2)
	if tr.StageID != offlineStage || tr.From != state.StatusAwaitingApproval || tr.To != state.StatusReady || tr.Event != "cli_approve" {
		t.Errorf("transition = %+v", tr)
	}
	if got := loadRunState(t, runDir).Stages[offlineStage].Status; got != state.StatusReady {
		t.Errorf("state.json status = %s, want ready", got)
	}
}

func TestOfflineRetry(t *testing.T) {
	chdirTemp(t)
	runDir := makeRunState(t, "flow-20260101-120000", offlineStage, state.StatusFailed)

	out, err := runOffline(t, newRetryCmd(), offlineStage)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if want := "stage \"impl\" retried: run 'afm run' to restart\n"; out != want {
		t.Errorf("stdout = %q, want %q", out, want)
	}
	tr := lastTransition(t, runDir, 2)
	if tr.From != state.StatusFailed || tr.To != state.StatusPending || tr.Event != "cli_retry" {
		t.Errorf("transition = %+v", tr)
	}
	if got := loadRunState(t, runDir).Stages[offlineStage].Status; got != state.StatusPending {
		t.Errorf("state.json status = %s, want pending", got)
	}
}

func TestOfflineRevise(t *testing.T) {
	chdirTemp(t)
	runDir := makeRunState(t, "flow-20260101-120000", offlineStage, state.StatusAwaitingApproval)
	stageDir := writePlan(t, runDir, "# Plan\n\nstep 1")

	out, err := runOffline(t, newReviseCmd(), "--feedback", "add rollback section", offlineStage)
	if err != nil {
		t.Fatalf("revise: %v", err)
	}
	if want := "feedback saved for stage \"impl\" -- orchestrator will re-plan\n"; out != want {
		t.Errorf("stdout = %q, want %q", out, want)
	}

	// Текущий план переименован в версию, новых файлов плана нет.
	if _, err := os.Stat(filepath.Join(stageDir, "plan.md")); !os.IsNotExist(err) {
		t.Errorf("plan.md must be renamed away, stat err=%v", err)
	}
	if got, err := os.ReadFile(filepath.Join(stageDir, "plan.v1.md")); err != nil || string(got) != "# Plan\n\nstep 1" {
		t.Errorf("plan.v1.md = %q, err=%v", got, err)
	}
	fb, err := os.ReadFile(filepath.Join(stageDir, "feedback.md"))
	if err != nil || !strings.Contains(string(fb), "--- revision 1 | ") || !strings.HasSuffix(string(fb), "add rollback section\n") {
		t.Errorf("feedback.md = %q, err=%v", fb, err)
	}

	tr := lastTransition(t, runDir, 2)
	if tr.From != state.StatusAwaitingApproval || tr.To != state.StatusRevising || tr.Event != "cli_revise" || tr.Reason != "add rollback section" {
		t.Errorf("transition = %+v", tr)
	}
	if got := loadRunState(t, runDir).Stages[offlineStage].Status; got != state.StatusRevising {
		t.Errorf("state.json status = %s, want revising", got)
	}
}

// Вторая ревизия получает следующую версию плана и следующий номер feedback.
func TestOfflineRevise_SecondRoundIncrementsVersions(t *testing.T) {
	chdirTemp(t)
	runDir := makeRunState(t, "flow-20260101-120000", offlineStage, state.StatusAwaitingApproval)
	stageDir := writePlan(t, runDir, "plan one")
	if _, err := runOffline(t, newReviseCmd(), "--feedback", "first", offlineStage); err != nil {
		t.Fatal(err)
	}

	// Оркестратор перепланировал: стадия снова ждёт approval с новым plan.md.
	store, err := implstate.Open(runDir, []string{offlineStage})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(&state.Transition{StageID: offlineStage, From: state.StatusRevising, To: state.StatusAwaitingApproval, Event: "test_replan"}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	writePlan(t, runDir, "plan two")

	if _, err := runOffline(t, newReviseCmd(), "--feedback", "second", offlineStage); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"plan.v1.md": "plan one", "plan.v2.md": "plan two"} {
		if got, err := os.ReadFile(filepath.Join(stageDir, name)); err != nil || string(got) != want {
			t.Errorf("%s = %q, err=%v, want %q", name, got, err, want)
		}
	}
	fb, _ := os.ReadFile(filepath.Join(stageDir, "feedback.md"))
	if !strings.Contains(string(fb), "--- revision 2 | ") || !strings.Contains(string(fb), "first") || !strings.Contains(string(fb), "second") {
		t.Errorf("feedback.md = %q", fb)
	}
}

func TestOfflineRevise_FeedbackFromStdin(t *testing.T) {
	chdirTemp(t)
	runDir := makeRunState(t, "flow-20260101-120000", offlineStage, state.StatusAwaitingApproval)
	stageDir := writePlan(t, runDir, "plan")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString("  from stdin \n\n"); err != nil {
		t.Fatal(err)
	}
	w.Close()
	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = origStdin; r.Close() })

	if _, err := runOffline(t, newReviseCmd(), offlineStage); err != nil {
		t.Fatalf("revise: %v", err)
	}
	if tr := lastTransition(t, runDir, 2); tr.Reason != "from stdin" {
		t.Errorf("reason = %q, want trimmed stdin", tr.Reason)
	}
	fb, _ := os.ReadFile(filepath.Join(stageDir, "feedback.md"))
	if !strings.HasSuffix(string(fb), "from stdin\n") {
		t.Errorf("feedback.md = %q", fb)
	}
}

func TestOfflineRevise_EmptyFeedback(t *testing.T) {
	chdirTemp(t)
	runDir := makeRunState(t, "flow-20260101-120000", offlineStage, state.StatusAwaitingApproval)
	writePlan(t, runDir, "plan")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = origStdin; r.Close() })

	_, err = runOffline(t, newReviseCmd(), offlineStage)
	if err == nil || err.Error() != "feedback is required (use --feedback or stdin)" {
		t.Fatalf("err = %v", err)
	}
	lastTransition(t, runDir, 1)
}

func TestOfflineRevise_MissingPlanChangesNothing(t *testing.T) {
	chdirTemp(t)
	runDir := makeRunState(t, "flow-20260101-120000", offlineStage, state.StatusAwaitingApproval)

	_, err := runOffline(t, newReviseCmd(), "--feedback", "x", offlineStage)
	if err == nil || !strings.HasPrefix(err.Error(), "version plan: plan.md not found: ") {
		t.Fatalf("err = %v", err)
	}
	lastTransition(t, runDir, 1)
	if _, err := os.Stat(filepath.Join(runDir, offlineStage, "feedback.md")); !os.IsNotExist(err) {
		t.Errorf("feedback.md must not be written, stat err=%v", err)
	}
}

// Неверный статус: точные тексты, переход не записан.
func TestOfflineWrongStatus(t *testing.T) {
	cases := []struct {
		name    string
		newCmd  func() *cobra.Command
		args    []string
		status  state.StageStatus
		wantErr string
	}{
		{"approve", newApproveCmd, []string{offlineStage}, state.StatusRunning, `stage "impl" is running, not awaiting_approval`},
		{"retry", newRetryCmd, []string{offlineStage}, state.StatusAwaitingApproval, `stage "impl" is awaiting_approval, not failed`},
		{"revise", newReviseCmd, []string{"--feedback", "x", offlineStage}, state.StatusFailed, `stage "impl" is failed, not awaiting_approval`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			chdirTemp(t)
			runDir := makeRunState(t, "flow-20260101-120000", offlineStage, c.status)
			out, err := runOffline(t, c.newCmd(), c.args...)
			if err == nil || err.Error() != c.wantErr {
				t.Fatalf("err = %v, want %q", err, c.wantErr)
			}
			if out != "" {
				t.Errorf("stdout = %q, want empty", out)
			}
			lastTransition(t, runDir, 1)
		})
	}
}

func TestOfflineUnknownStage(t *testing.T) {
	for name, newCmd := range map[string]func() *cobra.Command{"approve": newApproveCmd, "retry": newRetryCmd} {
		t.Run(name, func(t *testing.T) {
			chdirTemp(t)
			makeRunState(t, "flow-20260101-120000", "other", state.StatusAwaitingApproval)
			_, err := runOffline(t, newCmd(), offlineStage)
			if err == nil || err.Error() != `no active run found for stage "impl"` {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// Активный ран держит lock: команда отвечает прежним текстом и не трогает журнал.
func TestOfflineActiveRunIsRejectedWithoutTouchingJournal(t *testing.T) {
	cases := []struct {
		name    string
		newCmd  func() *cobra.Command
		args    []string
		status  state.StageStatus
		wantErr string
	}{
		{"approve", newApproveCmd, []string{offlineStage}, state.StatusAwaitingApproval,
			"run is active — approve via the dashboard, or stop `afm run` first"},
		{"retry", newRetryCmd, []string{offlineStage}, state.StatusFailed,
			"run is active — retry via the dashboard, or stop `afm run` first"},
		{"revise", newReviseCmd, []string{"--feedback", "x", offlineStage}, state.StatusAwaitingApproval,
			"run is active — revise via the dashboard, or stop `afm run` first"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			chdirTemp(t)
			runDir := makeRunState(t, "flow-20260101-120000", offlineStage, c.status)
			stageDir := writePlan(t, runDir, "plan")

			// Активный ран = открытый Store с эксклюзивным lock.
			holder, err := implstate.Open(runDir, []string{offlineStage})
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Close()
			before, err := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
			if err != nil {
				t.Fatal(err)
			}

			out, err := runOffline(t, c.newCmd(), c.args...)
			if err == nil || err.Error() != c.wantErr {
				t.Fatalf("err = %v, want %q", err, c.wantErr)
			}
			if out != "" {
				t.Errorf("stdout = %q, want empty", out)
			}
			after, err := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Errorf("events.jsonl changed while the run was active:\nbefore=%q\nafter=%q", before, after)
			}
			if got, err := os.ReadFile(filepath.Join(stageDir, "plan.md")); err != nil || string(got) != "plan" {
				t.Errorf("plan.md = %q, err=%v; a rejected command must not version the plan", got, err)
			}
			if _, err := os.Stat(filepath.Join(stageDir, "feedback.md")); !os.IsNotExist(err) {
				t.Errorf("feedback.md must not be written, stat err=%v", err)
			}
		})
	}
}

// Стадия в awaiting_assistants: вместо общей ошибки статуса — объяснение с блокирующей задачей.
func TestOfflineAwaitingAssistantsExplanation(t *testing.T) {
	setup := func(t *testing.T) string {
		chdirTemp(t)
		runDir := makeRunState(t, "flow-20260101-120000", offlineStage, state.StatusAwaitingAssistants)
		appendControlLine(t, runDir, 2, implstate.Control{
			Kind:   "task_created",
			Actor:  state.Actor{Kind: "user"},
			Target: implstate.Target{TaskID: "sec-1", StageID: offlineStage},
			Payload: mustJSON(t, implstate.TaskCreatedPayload{
				ProfileID: "security-reviewer", Title: "Security pass", Mode: "required", CreatedBy: "user",
			}),
		})
		return runDir
	}

	blocked := "stage \"impl\" is awaiting_assistants: blocked by required helper task sec-1 (status waiting_parent" +
		", profile security-reviewer): Security pass.\n" +
		"To unblock it: resolve the task itself (let it finish, retry it, or give it a follow-up) from the " +
		"dashboard, or explicitly waive the requirement (POST /api/assistants/sec-1/waive) if collaboration " +
		"should not have required it here. If collaboration is disabled for this run (collaboration.enabled " +
		"/ AFM_COLLABORATION), re-enable it to interact with the task normally — a disabled feature does not " +
		"remove an already-open required branch."

	for name, newCmd := range map[string]func() *cobra.Command{"approve": newApproveCmd, "retry": newRetryCmd} {
		t.Run(name, func(t *testing.T) {
			runDir := setup(t)
			out, err := runOffline(t, newCmd(), offlineStage)
			if err == nil || err.Error() != blocked {
				t.Fatalf("err = %q\nwant %q", err, blocked)
			}
			if out != "" {
				t.Errorf("stdout = %q, want empty", out)
			}
			if got := loadStatusFromLog(t, runDir); got != state.StatusAwaitingAssistants {
				t.Errorf("status = %s, want awaiting_assistants unchanged", got)
			}
		})
	}

	t.Run("revise_does_not_version_plan", func(t *testing.T) {
		runDir := setup(t)
		stageDir := writePlan(t, runDir, "plan")
		_, err := runOffline(t, newReviseCmd(), "--feedback", "x", offlineStage)
		if err == nil || err.Error() != blocked {
			t.Fatalf("err = %q\nwant %q", err, blocked)
		}
		if _, err := os.Stat(filepath.Join(stageDir, "plan.md")); err != nil {
			t.Errorf("plan.md must stay: %v", err)
		}
		if _, err := os.Stat(filepath.Join(stageDir, "feedback.md")); !os.IsNotExist(err) {
			t.Errorf("feedback.md must not be written, stat err=%v", err)
		}
	})

	t.Run("no_current_blocker", func(t *testing.T) {
		chdirTemp(t)
		makeRunState(t, "flow-20260101-120000", offlineStage, state.StatusAwaitingAssistants)
		_, err := runOffline(t, newApproveCmd(), offlineStage)
		want := `stage "impl" is awaiting_assistants, but its required task(s) have no current blocker — try again in a moment`
		if err == nil || err.Error() != want {
			t.Fatalf("err = %v, want %q", err, want)
		}
	})
}

func loadStatusFromLog(t *testing.T, runDir string) state.StageStatus {
	t.Helper()
	rs, err := implstate.LoadRunState(runDir)
	if err != nil {
		t.Fatal(err)
	}
	return rs.Stages[offlineStage].Status
}
