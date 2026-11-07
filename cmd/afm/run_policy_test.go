package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	implstate "github.com/akopichin/afm-engine/testkit/state"
	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/contracts/flow"
	"github.com/akopichin/afm/contracts/state"
)

// Политика headless-рана без dashboard (server port 0): предупреждение про interactive, auto-approve,
// провал стадии при --require-approval и итог рана. Агент подменён скриптом claude в PATH.

const policyPlan = `## Tasks\n- one\n\n## Assumptions\n- none\n\n## Acceptance Criteria\n- done`

// installPolicyAgent кладёт в PATH claude, который печатает валидный план, а при наличии plan.md (фаза
// implementation) ещё и пишет .done.
func installPolicyAgent(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", filepath.Dir(writePolicyAgent(t))+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// writePolicyAgent пишет тот же фейковый агент и возвращает абсолютный путь к нему, без PATH.
func writePolicyAgent(t *testing.T) string {
	t.Helper()
	script := "#!/bin/sh\ncat >/dev/null\n" +
		"if [ -f \"$AFM_STAGE_DIR/plan.md\" ]; then echo done > \"$AFM_STAGE_DIR/.done\"; fi\n" +
		"cat <<'EOF'\n" +
		`{"type":"assistant","message":{"content":[{"type":"text","text":"` + policyPlan + `"}]}}` + "\n" +
		`{"type":"result","subtype":"success"}` + "\nEOF\n"
	path := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil { //nolint:gosec // test fixture: an executable fake agent
		t.Fatal(err)
	}
	return path
}

const (
	interactiveWarning = "warning: stage \"build\": interactive requires dashboard (server port > 0); running as non-interactive\n"
	headlessApproval   = "approval required but no dashboard running (use --port or server.port in config)"
	buildFlow          = "name: rp\nstages:\n  - id: build\n    name: Build\n    agents: [planning, implementation]\n"
	interactiveFlow    = buildFlow + "    interactive: true\n"
	autoApproveFlow    = buildFlow + "    auto_approve: true\n"
)

type headlessRun struct {
	code           int
	stdout, stderr string
	runDir         string
}

// runHeadless запускает `afm run --port=0` целиком, как пользователь без dashboard.
func runHeadless(t *testing.T, flowYAML string, extraArgs ...string) headlessRun {
	t.Helper()
	fx := newExitOnFailFixture(t)
	installPolicyAgent(t)
	fx.write("flow.yaml", flowYAML)
	var res headlessRun
	res.stdout, res.stderr = captureOutput(t, func() {
		args := append([]string{"run", fx.flowPath, "--dir", fx.root, "--port=0"}, extraArgs...)
		res.code = runMain(args)
	})
	dirs := fx.runDirs()
	if len(dirs) != 1 {
		t.Fatalf("run dirs = %v, want one", dirs)
	}
	res.runDir = filepath.Join(fx.root, ".afm", "runs", dirs[0])
	return res
}

// stageEvents возвращает события журнала для стадии build в порядке записи.
func stageEvents(t *testing.T, runDir string) []string {
	t.Helper()
	var out []string
	for _, tr := range readEvents(t, runDir) {
		if tr.StageID == "build" {
			out = append(out, tr.Event+":"+string(tr.To))
		}
	}
	return out
}

func wantStageEvents(t *testing.T, runDir string, want ...string) {
	t.Helper()
	got := stageEvents(t, runDir)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("stage events = %v, want %v", got, want)
	}
}

func TestHeadlessPolicy_AutoApprovesWithoutRequireApproval(t *testing.T) {
	res := runHeadless(t, buildFlow)
	if res.code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", res.code, res.stderr)
	}
	// Никто не нажимает Approve: план одобряется сам, стадия идёт до done, ран завершается.
	wantStageEvents(t, res.runDir, "start_planning:planning", "plan_ready:awaiting_approval", "approve:ready", "start_run:running", "complete:done")
	if !strings.Contains(res.stdout, `afm: flow "rp" completed`) {
		t.Errorf("stdout = %q", res.stdout)
	}
	if res.stderr != "" {
		t.Errorf("stderr = %q, want empty", res.stderr)
	}
	if rs, _ := implstate.LoadRunState(res.runDir); rs.RunStatus != state.RunStatusFinished {
		t.Errorf("run status = %s, want finished", rs.RunStatus)
	}
}

func TestHeadlessPolicy_RequireApprovalFailsTheStage(t *testing.T) {
	res := runHeadless(t, buildFlow, "--require-approval")

	// Провал стадии не меняет код выхода без --exit-on-fail; implementation не стартует.
	if res.code != 0 {
		t.Fatalf("exit code = %d, want legacy 0; stderr=%q", res.code, res.stderr)
	}
	wantStageEvents(t, res.runDir, "start_planning:planning", "plan_ready:awaiting_approval", "fail:failed")
	var failure state.Transition
	for _, tr := range readEvents(t, res.runDir) {
		if tr.Event == "fail" {
			failure = tr
		}
	}
	if failure.Reason != headlessApproval {
		t.Errorf("fail reason = %q, want %q", failure.Reason, headlessApproval)
	}
	if strings.Contains(res.stdout, "implementation agent") {
		t.Errorf("implementation must not start after the approval failure: %q", res.stdout)
	}
	if rs, _ := implstate.LoadRunState(res.runDir); rs.RunStatus != state.RunStatusFailed {
		t.Errorf("run status = %s, want failed", rs.RunStatus)
	}
}

func TestHeadlessPolicy_RequireApprovalWithExitOnFail(t *testing.T) {
	res := runHeadless(t, buildFlow, "--require-approval", "--exit-on-fail")
	if res.code != 1 {
		t.Fatalf("exit code = %d, want 1", res.code)
	}
	if !strings.Contains(res.stderr, headlessApproval) || !strings.HasPrefix(res.stderr, "afm: stage build failed: "+headlessApproval+" (run dir: ") {
		t.Errorf("stderr = %q", res.stderr)
	}
	if strings.Contains(res.stdout, `flow "rp" completed`) {
		t.Errorf("stdout = %q, must not report completion", res.stdout)
	}
}

// auto_approve стадии побеждает и headless-проверку, и --require-approval.
func TestHeadlessPolicy_AutoApproveStageIgnoresRequireApproval(t *testing.T) {
	res := runHeadless(t, autoApproveFlow, "--require-approval")
	if res.code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", res.code, res.stderr)
	}
	wantStageEvents(t, res.runDir, "start_planning:planning", "plan_ready:awaiting_approval", "approve:ready", "start_run:running", "complete:done")
}

func TestHeadlessPolicy_InteractiveStageRunsAsNonInteractive(t *testing.T) {
	for name, args := range map[string][]string{"default": nil, "require_approval": {"--require-approval"}} {
		t.Run(name, func(t *testing.T) {
			res := runHeadless(t, interactiveFlow, args...)
			if res.stderr != interactiveWarning {
				t.Fatalf("stderr = %q, want exactly %q", res.stderr, interactiveWarning)
			}
			for _, ev := range stageEvents(t, res.runDir) {
				if strings.HasSuffix(ev, ":"+string(state.StatusAwaitingUserInput)) {
					t.Fatalf("non-interactive run must not ask the user: %v", stageEvents(t, res.runDir))
				}
			}
		})
	}
}

// Итог executeFlow: Completed только когда все стадии done; провал стадии — не ошибка вызова.
func TestHeadlessPolicy_CycleResult(t *testing.T) {
	cases := []struct {
		name            string
		requireApproval bool
		wantCompleted   bool
	}{
		{"auto_approved", false, true},
		{"approval_required_failure", true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fx := newExitOnFailFixture(t)
			installPolicyAgent(t)
			fx.write("flow.yaml", buildFlow)
			f, err := flow.ParseFile(fx.flowPath)
			if err != nil {
				t.Fatal(err)
			}
			cfg := config.Default()
			port := 0
			cfg.Server.Port = &port

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			var res cycleResult
			captureOutput(t, func() {
				res, err = executeFlow(ctx, f, cfg, combineRunHooks(cfg, f), runOptions{requireApproval: c.requireApproval, runName: f.Name})
			})
			if err != nil {
				t.Fatalf("executeFlow: %v", err)
			}
			if res.Completed != c.wantCompleted || res.Resumed || res.RunID == "" {
				t.Fatalf("result = %+v, want Completed=%v", res, c.wantCompleted)
			}
		})
	}
}
