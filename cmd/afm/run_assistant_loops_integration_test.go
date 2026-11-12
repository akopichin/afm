package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm/pkg/server"
)

// Loops declared in flow.yaml through a real `afm run`: only the mesh API, the journal and the helper's call log
// are observed. Ticks are real (every: 1s), so assertions are "not earlier than" and deadline waits.

const loopFlowEvery = time.Second

// loopProfileConfig enables collaboration with the one profile the loop flows declare.
func loopProfileConfig(collaboration bool) string {
	return "server:\n  open_browser: false\ncollaboration:\n  enabled: " + strconv.FormatBool(collaboration) +
		"\n  profiles:\n    watcher:\n      title: Conditions watcher\n      command: codex-as-claude\n      instructions: Report anything that changed.\n"
}

type loopFixture struct {
	*modeFixture
	helperLog string
}

// newLoopFixture installs a helper that answers with a valid result and logs the time of every call; the stage's
// agent is gated, so the loop lives until the test releases the stage.
func newLoopFixture(t *testing.T, mode, flowFile string, collaboration bool) *loopFixture {
	t.Helper()
	helperLog := filepath.Join(t.TempDir(), "helper-calls.log")
	binDir := writeFakeCodexHelper(t, "pass", "Nothing changed.")
	script := filepath.Join(binDir, "codex-as-claude")
	body, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	// BSD date has no %N: perl gives the same nanosecond clock there.
	logCall := fmt.Sprintf("now=$(date +%%s%%N)\ncase $now in *N) now=$(perl -MTime::HiRes=time -e 'printf \"%%d\", time*1e9');; esac\necho \"$now\" >> '%s'\n", helperLog)
	withLog := strings.Replace(string(body), "#!/bin/sh\n", "#!/bin/sh\n"+logCall, 1)
	if err := os.WriteFile(script, []byte(withLog), 0o755); err != nil { //nolint:gosec // test helper script must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	fx := newModeFixture(t, mode, flowFile, "")
	writeFile(t, filepath.Join(fx.root, ".afm", "config.yaml"), loopProfileConfig(collaboration))
	fx.gateImplementation()
	return &loopFixture{modeFixture: fx, helperLog: helperLog}
}

// helperCalls are the times the helper was started, oldest first.
func (fx *loopFixture) helperCalls() []time.Time {
	fx.t.Helper()
	raw, err := os.ReadFile(fx.helperLog)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		fx.t.Fatal(err)
	}
	var calls []time.Time
	for _, field := range strings.Fields(string(raw)) {
		nanos, err := strconv.ParseInt(field, 10, 64)
		if err != nil {
			fx.t.Fatalf("helper log entry %q: %v", field, err)
		}
		calls = append(calls, time.Unix(0, nanos))
	}
	return calls
}

type loopInfo struct {
	ID          string `json:"id"`
	CreatorID   string `json:"creator_id"`
	TargetID    string `json:"target_id"`
	Every       string `json:"every"`
	OriginStage string `json:"origin_stage"`
	UntilStage  string `json:"until_stage"`
	Status      string `json:"status"`
	EndReason   string `json:"end_reason"`
}

func (fx *loopFixture) loops() []loopInfo {
	fx.t.Helper()
	code, body, _ := fx.call(http.MethodGet, "/api/mesh/loops", "", nil)
	if code != http.StatusOK {
		fx.t.Fatalf("GET /api/mesh/loops = %d: %s", code, body)
	}
	var page struct {
		Loops []loopInfo `json:"loops"`
	}
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		fx.t.Fatalf("decode loops %s: %v", body, err)
	}
	return page.Loops
}

// waitLoop waits for the first loop to be listed and returns it.
func (fx *loopFixture) waitLoop() loopInfo {
	fx.t.Helper()
	var loop loopInfo
	waitFor(fx.t, "the declared loop", func() bool {
		loops := fx.loops()
		if len(loops) == 0 {
			return false
		}
		loop = loops[0]
		return true
	})
	return loop
}

// stageHelpers are the ids of the helper tasks attached to stage build.
func (fx *loopFixture) stageHelpers() []string {
	fx.t.Helper()
	code, body, _ := fx.call(http.MethodGet, "/api/stages/build/assistants", "", nil)
	if code != http.StatusOK {
		fx.t.Fatalf("GET assistants = %d: %s", code, body)
	}
	var out struct {
		Tasks []server.AssistantTaskView `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		fx.t.Fatalf("decode assistants %s: %v", body, err)
	}
	var ids []string
	for _, task := range out.Tasks {
		ids = append(ids, task.ID)
	}
	return ids
}

// committedAt is the journal time of the first commit whose operation id starts with prefix.
func committedAt(t *testing.T, runDir, prefix string) time.Time {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range bytes.Split(raw, []byte("\n")) {
		var rec struct {
			Time        time.Time `json:"time"`
			OperationID string    `json:"operation_id"`
		}
		if json.Unmarshal(line, &rec) == nil && strings.HasPrefix(rec.OperationID, prefix) {
			return rec.Time
		}
	}
	t.Fatalf("no commit %q in the journal", prefix)
	return time.Time{}
}

// journalHas reports whether the run journal mentions an effect or event kind.
func journalHas(t *testing.T, runDir, kind string) bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Contains(raw, []byte(`"kind":"`+kind+`"`))
}

// The loop appears with the helper, first ticks no earlier than every after the attach, and ends with its stage
// without keeping the flow alive.
func TestDeclaredAssistantLoop_TicksAndEndsWithStage(t *testing.T) {
	for _, mode := range bothModes {
		t.Run(mode, func(t *testing.T) {
			fx := newLoopFixture(t, mode, "assistant-loop.yaml", true)
			child := fx.start()
			runID := fx.waitReady(child)

			loop := fx.waitLoop()
			helpers := fx.stageHelpers()
			if len(helpers) != 1 {
				t.Fatalf("stage helpers = %v, want the one declared helper", helpers)
			}
			want := loopInfo{
				ID: "L1", CreatorID: "user", TargetID: "helper:" + helpers[0], Every: "1s",
				OriginStage: "build", UntilStage: "build", Status: "active",
			}
			if loop != want {
				t.Fatalf("loop = %+v, want %+v", loop, want)
			}

			attachedAt := committedAt(t, fx.runDir(runID), "stage-assistants:build:watcher")
			waitFor(t, "the first helper call", func() bool { return len(fx.helperCalls()) > 0 })
			if first := fx.helperCalls()[0]; first.Sub(attachedAt) < loopFlowEvery {
				t.Errorf("first helper call %v after the attach, want at least %v", first.Sub(attachedAt), loopFlowEvery)
			}

			fx.release("build")
			fx.waitRunStatus("finished")
			waitFor(t, "the loop to end with its stage", func() bool { return fx.loops()[0].Status == "ended" })
			if reason := fx.loops()[0].EndReason; reason == "" {
				t.Error("an ended loop carries no end_reason")
			}

			// A call already running when the stage finished may still be logged; wait for it, then expect silence.
			var settled int
			waitFor(t, "the helper calls to settle", func() bool {
				before := len(fx.helperCalls())
				time.Sleep(500 * time.Millisecond)
				settled = len(fx.helperCalls())
				return settled == before
			})
			time.Sleep(2 * loopFlowEvery)
			if got := len(fx.helperCalls()); got != settled {
				t.Errorf("helper calls grew from %d to %d after the loop ended", settled, got)
			}

			if mode == modeDashboard {
				if code := child.exitCode(t); code != 0 {
					t.Fatalf("dashboard exit code = %d, want 0 (a loop must not keep the flow alive)\n%s", code, child.out.String())
				}
				return
			}
			child.waitOutput(t, holdingFinalState)
			if code := child.interrupt(t); code != 0 {
				t.Fatalf("core exit code after the final hold = %d, want 0\n%s", code, child.out.String())
			}
		})
	}
}

// A loop the human stopped stays stopped when the same run is resumed, and the helper is not attached twice.
func TestDeclaredAssistantLoop_StopSurvivesResume(t *testing.T) {
	fx := newLoopFixture(t, modeCore, "assistant-loop.yaml", true)
	child := fx.start()
	runID := fx.waitReady(child)
	loop := fx.waitLoop()
	helpers := fx.stageHelpers()

	fx.mustPost("/api/mesh/loop", fmt.Sprintf(`{"request_id":"stop-1","action":"stop","loop_id":%q}`, loop.ID))
	waitFor(t, "the loop to be stopped", func() bool { return fx.loops()[0].Status == "stopped" })
	if code := child.interrupt(t); code != interruptedExitCode {
		t.Fatalf("exit code = %d, want %d\n%s", code, interruptedExitCode, child.out.String())
	}
	callsBefore := len(fx.helperCalls())

	again := fx.start()
	if got := fx.waitReady(again); got != runID {
		t.Fatalf("restart opened run %s, want the interrupted %s", got, runID)
	}
	fx.waitStage("build", "running")
	loops := fx.loops()
	if len(loops) != 1 || loops[0].ID != loop.ID || loops[0].Status != "stopped" {
		t.Fatalf("loops after resume = %+v, want the single stopped %s", loops, loop.ID)
	}
	if got := fx.stageHelpers(); len(got) != 1 || got[0] != helpers[0] {
		t.Fatalf("stage helpers after resume = %v, want only %v", got, helpers)
	}
	time.Sleep(2 * loopFlowEvery)
	if got := len(fx.helperCalls()); got != callsBefore {
		t.Errorf("helper calls grew from %d to %d after resume with a stopped loop", callsBefore, got)
	}
}

// No loop exists while the stage only plans: helpers attach at execution, and without collaboration neither the
// helper nor its loop is ever created.
func TestDeclaredAssistantLoop_NotAttachedDuringPlanningOrWithoutCollaboration(t *testing.T) {
	t.Run("planning", func(t *testing.T) {
		fx := newLoopFixture(t, modeCore, "assistant-loop-planned.yaml", true)
		child := fx.start()
		fx.waitReady(child)
		fx.waitStage("build", "awaiting_approval")
		time.Sleep(2 * loopFlowEvery)
		if loops := fx.loops(); len(loops) != 0 {
			t.Fatalf("loops while the plan awaits approval = %+v, want none", loops)
		}
		if calls := fx.helperCalls(); len(calls) != 0 {
			t.Fatalf("the helper was called %d times before the plan was approved", len(calls))
		}

		fx.mustPost("/api/stages/build/approve", "{}")
		if loop := fx.waitLoop(); loop.Status != "active" {
			t.Fatalf("loop after approval = %+v, want active", loop)
		}
	})
	t.Run("collaboration off", func(t *testing.T) {
		fx := newLoopFixture(t, modeCore, "assistant-loop.yaml", false)
		child := fx.start()
		runID := fx.waitReady(child)
		fx.waitStage("build", "running")
		time.Sleep(2 * loopFlowEvery)
		fx.release("build")
		fx.waitRunStatus("finished")

		for _, kind := range []string{"loop_created", "task_created"} {
			if journalHas(t, fx.runDir(runID), kind) {
				t.Errorf("the journal has %s although collaboration is off", kind)
			}
		}
		if calls := fx.helperCalls(); len(calls) != 0 {
			t.Errorf("the helper was called %d times although collaboration is off", len(calls))
		}
	})
}
