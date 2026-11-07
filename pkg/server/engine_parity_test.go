package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akopichin/afm-engine/testkit/engine"
	"github.com/akopichin/afm-engine/testkit/fixtures"
	implstate "github.com/akopichin/afm-engine/testkit/state"
	"github.com/akopichin/afm/contracts/browser"
	"github.com/akopichin/afm/contracts/config"
	"github.com/akopichin/afm/contracts/flow"
	"github.com/akopichin/afm/contracts/state"
)

// Parity between the HTTP commands and engine.StageControl on one real run: a command refused by the engine is
// refused over HTTP with the status and body writeCommandError gives that error, and an accepted one changes
// the run the same way through either door.

const parityDeadline = 20 * time.Second

type parityEnv struct {
	run    *engine.Run
	srv    *Server
	runDir string
}

// newParityEnv opens a real run of stages and serves it over HTTP the way cmd/afm does. The run is started.
func newParityEnv(t *testing.T, stages ...flow.Stage) *parityEnv {
	t.Helper()
	t.Setenv("AFM_SIDEAGENT", "0")
	root := t.TempDir()
	afmDir := filepath.Join(root, ".afm")
	runsDir := filepath.Join(afmDir, "runs")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), parityDeadline)
	defer cancel()
	run, err := engine.Open(ctx, engine.Spec{Flow: &flow.Flow{Name: "parity", Stages: stages}, Config: config.DefaultExecution()}, engine.Options{
		Paths:    engine.Paths{AfmRoot: root, AfmDir: afmDir, RunsDir: runsDir, Executable: exe},
		RunName:  "parity",
		Policies: engine.Policies{HumanControl: true},
		Browser:  func() (*browser.Descriptor, func(), error) { return nil, func() {}, nil },
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	runCtx, stop := context.WithCancel(context.Background())
	t.Cleanup(func() {
		// Cancel the run context, wait for the agents, then close the Store (AGENTS.md, clean shutdown).
		stop()
		shutdownCtx, done := context.WithTimeout(context.Background(), parityDeadline)
		defer done()
		_ = run.Shutdown(shutdownCtx)
		_ = run.Close()
	})
	if err := run.Start(runCtx, engine.Endpoints{}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	read := run.Read()
	configs := StaticStageConfigs{}
	for _, s := range stages {
		configs[s.ID] = StageConfig{Name: s.Name, Interactive: s.Interactive, IsScript: s.IsScript(), Buttons: s.Buttons.Labels()}
	}
	srv := New(Config{
		RunDir: read.RunDir(), Stages: configs, Store: read, UIBus: read.Events(),
		Actions: run.Stages(), Secondary: run.Stages(),
	})
	return &parityEnv{run: run, srv: srv, runDir: read.RunDir()}
}

func (e *parityEnv) waitStatus(t *testing.T, stageID string, want state.StageStatus) {
	t.Helper()
	deadline := time.Now().Add(parityDeadline)
	var got state.StageStatus
	for time.Now().Before(deadline) {
		if rs, err := implstate.LoadRunState(e.runDir); err == nil {
			if got = rs.Stages[stageID].Status; got == want {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("stage %s did not reach %v, last status %v", stageID, want, got)
}

func (e *parityEnv) post(path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(w, req)
	return w
}

// wantSameRefusal compares an HTTP response with how the engine error is rendered by the HTTP adapter.
func wantSameRefusal(t *testing.T, what string, got *httptest.ResponseRecorder, engineErr error) {
	t.Helper()
	if engineErr == nil {
		t.Fatalf("%s: the engine accepted a command the test expects it to refuse", what)
	}
	want := httptest.NewRecorder()
	writeCommandError(want, engineErr)
	if got.Code != want.Code || got.Body.String() != want.Body.String() || got.Header().Get("Content-Type") != want.Header().Get("Content-Type") {
		t.Errorf("%s: HTTP answered %d %q (%s); the engine error %q renders as %d %q (%s)",
			what, got.Code, got.Body.String(), got.Header().Get("Content-Type"), engineErr, want.Code, want.Body.String(), want.Header().Get("Content-Type"))
	}
}

func parityScript(releaseDir, id string) flow.Stage {
	return flow.Stage{ID: id, Name: id, Script: fmt.Sprintf(`while [ ! -e '%s/%s' ]; do sleep 0.02; done`, releaseDir, id)}
}

func parityAgent(t *testing.T, id, script string, deps ...string) flow.Stage {
	t.Helper()
	command, err := fixtures.WriteAgentScript(t.TempDir(), script)
	if err != nil {
		t.Fatal(err)
	}
	return flow.Stage{
		ID: id, Name: id, DependsOn: deps, Command: command,
		Agents:      []flow.AgentType{flow.AgentPlanning, flow.AgentImplementation},
		Interactive: strings.HasPrefix(script, "dialog"),
	}
}

func TestEngineParity_RefusedStageCommands(t *testing.T) {
	releaseDir := t.TempDir()
	t.Setenv("AFM_IT_RELEASE_DIR", releaseDir)
	env := newParityEnv(t, parityScript(releaseDir, "work"), flow.Stage{ID: "tail", Name: "tail", Script: "true", DependsOn: []string{"work"}})
	env.waitStatus(t, "work", state.StatusRunning)
	stages := env.run.Stages()
	ctx := context.Background()

	cases := []struct {
		name   string
		path   string
		body   string
		engine func() error
	}{
		{"approve a running stage", "/api/stages/work/approve", "", func() error { return stages.Approve(ctx, "work") }},
		{"retry a running stage", "/api/stages/work/retry", "", func() error { return stages.Retry(ctx, "work") }},
		{"continue a running stage", "/api/stages/work/continue", "", func() error { return stages.Continue(ctx, "work") }},
		{"pause a running script", "/api/stages/work/pause", "", func() error { return stages.Pause(ctx, "work") }},
		{"revise a script stage", "/api/stages/work/revise", `{"feedback":"x"}`, func() error { return stages.Revise(ctx, "work", "x") }},
		{"button on a script stage", "/api/stages/work/button", `{"name":"go"}`, func() error { return stages.Button(ctx, "work", "go") }},
		{"note for a running stage", "/api/stages/work/note", `{"note":"x"}`, func() error { return stages.SetPreNote("work", "x") }},
		{"note for a pending script stage", "/api/stages/tail/note", `{"note":"x"}`, func() error { return stages.SetPreNote("tail", "x") }},
		{"answer without a question", "/api/stages/work/dialog/answer", `{"id":"q1","phase":"planning","answer":"x"}`, func() error { return stages.Answer("work", "planning", "q1", "x", false) }},
		{"answer in an invalid phase", "/api/stages/work/dialog/answer", `{"id":"q1","phase":"bogus","answer":"x"}`, func() error { return stages.Answer("work", "bogus", "q1", "x", false) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantSameRefusal(t, tc.name, env.post(tc.path, tc.body), tc.engine())
		})
	}
	if rs, err := implstate.LoadRunState(env.runDir); err != nil || rs.Stages["work"].Status != state.StatusRunning {
		t.Fatalf("after the refused commands: %v, %v; want the stage still running", rs.Stages["work"].Status, err)
	}
	touch(t, filepath.Join(releaseDir, "work"))
}

func TestEngineParity_AcceptedAnswerAndNote(t *testing.T) {
	releaseDir := t.TempDir()
	t.Setenv("AFM_IT_RELEASE_DIR", releaseDir)
	t.Setenv("AFM_IT_RUNS_LOG", filepath.Join(t.TempDir(), "runs.log"))
	env := newParityEnv(t,
		parityScript(releaseDir, "gate"),
		parityAgent(t, "askhttp", "dialog_agent.sh"),
		parityAgent(t, "askengine", "dialog_agent.sh"),
		parityAgent(t, "notehttp", "agent.sh", "gate"),
		parityAgent(t, "noteengine", "agent.sh", "gate"),
	)
	env.waitStatus(t, "askhttp", state.StatusAwaitingUserInput)
	env.waitStatus(t, "askengine", state.StatusAwaitingUserInput)

	w := env.post("/api/stages/askhttp/dialog/answer", `{"id":"q1","phase":"planning","answer":"postgres"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP answer = %d %q, want 200", w.Code, w.Body.String())
	}
	if err := env.run.Stages().Answer("askengine", "planning", "q1", "postgres", false); err != nil {
		t.Fatalf("engine Answer: %v", err)
	}
	env.waitStatus(t, "askhttp", state.StatusAwaitingApproval)
	env.waitStatus(t, "askengine", state.StatusAwaitingApproval)
	for _, id := range []string{"askhttp", "askengine"} {
		got, err := os.ReadFile(filepath.Join(env.runDir, id, "planning.q1.answer.json"))
		if err != nil || !strings.Contains(string(got), `"postgres"`) {
			t.Fatalf("%s answer file = %q, %v; want the answer recorded the same way", id, got, err)
		}
	}

	if w := env.post("/api/stages/notehttp/note", `{"note":"use staging"}`); w.Code != http.StatusOK {
		t.Fatalf("HTTP note = %d %q, want 200", w.Code, w.Body.String())
	}
	if err := env.run.Stages().SetPreNote("noteengine", "use staging"); err != nil {
		t.Fatalf("engine SetPreNote: %v", err)
	}
	for _, id := range []string{"notehttp", "noteengine"} {
		if got := implstate.LoadPreNote(filepath.Join(env.runDir, id)); got != "use staging" {
			t.Fatalf("%s pre-note = %q, want the note stored the same way", id, got)
		}
	}
	touch(t, filepath.Join(releaseDir, "gate"))
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// footprint is what a command leaves behind for one stage: its status, how many times its agent or script
// ran and which notices were recorded. Twins that went through different doors must match.
type footprint struct {
	Status  state.StageStatus
	Runs    int
	Notices []string
}

// runsLog is where the test fixtures record one line per invocation, "<stage>" or "<stage>:<phase>".
func (e *parityEnv) footprint(t *testing.T, runsLog, stageID string) footprint {
	t.Helper()
	rs, err := implstate.LoadRunState(e.runDir)
	if err != nil {
		t.Fatal(err)
	}
	fp := footprint{Status: rs.Stages[stageID].Status}
	if f, err := os.Open(runsLog); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if line := sc.Text(); line == stageID || strings.HasPrefix(line, stageID+":") {
				fp.Runs++
			}
		}
	}
	if f, err := os.Open(filepath.Join(e.runDir, "notices.jsonl")); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			var n struct {
				Type    string `json:"type"`
				StageID string `json:"stage_id"`
			}
			if json.Unmarshal(sc.Bytes(), &n) == nil && n.StageID == stageID {
				fp.Notices = append(fp.Notices, n.Type)
			}
		}
	}
	return fp
}

func wantSameFootprint(t *testing.T, what string, viaHTTP, viaEngine footprint) {
	t.Helper()
	if viaHTTP.Status != viaEngine.Status || viaHTTP.Runs != viaEngine.Runs || strings.Join(viaHTTP.Notices, ",") != strings.Join(viaEngine.Notices, ",") {
		t.Errorf("%s: over HTTP the stage ended as %+v, through the engine as %+v", what, viaHTTP, viaEngine)
	}
}

// pairStages returns a stage twin for each door: <name>-http is driven over HTTP, <name>-engine directly.
func pairStages(name string, build func(id string) flow.Stage) []flow.Stage {
	return []flow.Stage{build(name + "-http"), build(name + "-engine")}
}

func (e *parityEnv) wantOK(t *testing.T, what, path, body string) {
	t.Helper()
	if w := e.post(path, body); w.Code != http.StatusOK {
		t.Fatalf("%s over HTTP = %d %q, want 200", what, w.Code, w.Body.String())
	}
}

// Every stage command that the engine accepts is accepted over HTTP, and the stage ends up the same way:
// status, agent or script runs and notices.
func TestEngineParity_AcceptedStageCommands(t *testing.T) {
	releaseDir := t.TempDir()
	runsLog := filepath.Join(t.TempDir(), "runs.log")
	hookMarker := filepath.Join(releaseDir, "hook-ok")
	t.Setenv("AFM_IT_RELEASE_DIR", releaseDir)
	t.Setenv("AFM_IT_RUNS_LOG", runsLog)

	plan := func(id string) flow.Stage { return parityAgent(t, id, "agent.sh") }
	button := func(id string) flow.Stage {
		st := parityAgent(t, id, "agent.sh")
		st.Buttons = flow.Buttons{{Label: "Tighten", Prompt: "make it shorter"}}
		return st
	}
	failing := func(id string) flow.Stage {
		return flow.Stage{ID: id, Name: id, Script: fmt.Sprintf("echo run >> '%s/%s.runs'; exit 1", releaseDir, id)}
	}
	held := func(id string) flow.Stage {
		st := parityAgent(t, id, "agent.sh")
		st.Agents = []flow.AgentType{flow.AgentAuto}
		return st
	}
	withHook := func(script string) func(string) flow.Stage {
		return func(id string) flow.Stage {
			st := held(id)
			st.ScriptBefore = script
			return st
		}
	}
	var stages []flow.Stage
	for _, group := range [][]flow.Stage{
		pairStages("approve", plan), pairStages("revise", plan), pairStages("button", button),
		pairStages("retry", failing), pairStages("pause", held),
		pairStages("hookretry", withHook(fmt.Sprintf("[ -e '%s' ] || exit 1", hookMarker))),
		pairStages("hookskip", withHook("exit 1")),
	} {
		stages = append(stages, group...)
	}
	for _, st := range stages {
		if !strings.HasPrefix(st.ID, "pause") && !st.IsScript() {
			touch(t, filepath.Join(releaseDir, st.ID)) // only the pause pair is held
		}
	}
	env := newParityEnv(t, stages...)
	stagesAPI := env.run.Stages()
	ctx := context.Background()

	for _, name := range []string{"approve", "revise", "button"} {
		env.waitStatus(t, name+"-http", state.StatusAwaitingApproval)
		env.waitStatus(t, name+"-engine", state.StatusAwaitingApproval)
	}
	scriptRuns := func(id string) int {
		data, _ := os.ReadFile(filepath.Join(releaseDir, id+".runs"))
		return strings.Count(string(data), "run")
	}
	runsBeforeRetry := map[string]int{}
	for _, name := range []string{"retry-http", "retry-engine"} {
		env.waitStatus(t, name, state.StatusFailed)
		runsBeforeRetry[name] = scriptRuns(name)
	}
	for _, name := range []string{"pause-http", "pause-engine"} {
		env.waitStatus(t, name, state.StatusRunning)
	}
	for _, name := range []string{"hookretry-http", "hookretry-engine", "hookskip-http", "hookskip-engine"} {
		env.waitStatus(t, name, state.StatusHookFailed)
	}

	type step struct {
		name, command, body string
		engine              func(id string) error
		want                state.StageStatus
	}
	steps := []step{
		{"approve", "approve", "", func(id string) error { return stagesAPI.Approve(ctx, id) }, state.StatusDone},
		{"revise", "revise", `{"feedback":"tighten it"}`, func(id string) error { return stagesAPI.Revise(ctx, id, "tighten it") }, state.StatusAwaitingApproval},
		{"button", "button", `{"name":"Tighten"}`, func(id string) error { return stagesAPI.Button(ctx, id, "Tighten") }, state.StatusAwaitingApproval},
		{"retry", "retry", "", func(id string) error { return stagesAPI.Retry(ctx, id) }, state.StatusFailed},
		{"pause", "pause", "", func(id string) error { return stagesAPI.Pause(ctx, id) }, state.StatusPaused},
	}
	touch(t, hookMarker)
	steps = append(steps,
		step{"hookretry", "retry-hook", "", stagesAPI.RetryHook, state.StatusDone},
		step{"hookskip", "skip-hook", "", stagesAPI.SkipHook, state.StatusDone},
	)
	for _, st := range steps {
		env.wantOK(t, st.name+" over HTTP", "/api/stages/"+st.name+"-http/"+st.command, st.body)
		if err := st.engine(st.name + "-engine"); err != nil {
			t.Fatalf("%s through the engine: %v", st.name, err)
		}
	}
	for _, st := range steps {
		env.waitStatus(t, st.name+"-http", st.want)
		env.waitStatus(t, st.name+"-engine", st.want)
	}
	// The retried scripts failed again after a full second attempt cycle, through either door.
	for _, name := range []string{"retry-http", "retry-engine"} {
		want := 2 * runsBeforeRetry[name]
		deadline := time.Now().Add(parityDeadline)
		for scriptRuns(name) < want && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		env.waitStatus(t, name, state.StatusFailed)
		if got := scriptRuns(name); got != want {
			t.Errorf("%s script runs = %d, want %d: one more attempt cycle after the retry", name, got, want)
		}
	}

	// A paused stage continues the same way through either door, and finishes once its agent is let go.
	env.wantOK(t, "continue over HTTP", "/api/stages/pause-http/continue", "")
	if err := stagesAPI.Continue(ctx, "pause-engine"); err != nil {
		t.Fatalf("continue through the engine: %v", err)
	}
	for _, id := range []string{"pause-http", "pause-engine"} {
		env.waitStatus(t, id, state.StatusRunning)
		touch(t, filepath.Join(releaseDir, id))
	}
	for _, id := range []string{"pause-http", "pause-engine"} {
		env.waitStatus(t, id, state.StatusDone)
	}

	for _, st := range steps {
		if st.name == "retry" {
			continue // a script stage records no agent runs; its script runs are compared above
		}
		wantSameFootprint(t, st.name, env.footprint(t, runsLog, st.name+"-http"), env.footprint(t, runsLog, st.name+"-engine"))
	}
	wantSameFootprint(t, "continue", env.footprint(t, runsLog, "pause-http"), env.footprint(t, runsLog, "pause-engine"))
}

const dialogAnswerFmt = "/api/stages/%s/dialog/answer"

func answerBody(id, answer string, fromOptions bool) string {
	return fmt.Sprintf(`{"id":%q,"phase":"planning","answer":%q,"from_options":%t}`, id, answer, fromOptions)
}

// The question with fixed options reaches the dashboard with them, refusals about it match, and the accepted
// answers of the two doors leave the same files, notices and status.
func TestEngineParity_OptionQuestionAnswers(t *testing.T) {
	runsLog := filepath.Join(t.TempDir(), "runs.log")
	t.Setenv("AFM_IT_RUNS_LOG", runsLog)
	env := newParityEnv(t, pairStages("ask", func(id string) flow.Stage { return parityAgent(t, id, "dialog_options_agent.sh") })...)
	env.waitStatus(t, "ask-http", state.StatusAwaitingUserInput)
	env.waitStatus(t, "ask-engine", state.StatusAwaitingUserInput)
	stages := env.run.Stages()

	req := httptest.NewRequest(http.MethodGet, "/api/stages/ask-http/dialog", nil)
	w := httptest.NewRecorder()
	env.srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"postgres"`) || !strings.Contains(w.Body.String(), `"mysql"`) {
		t.Fatalf("dialog = %d %q, want the options of the question", w.Code, w.Body.String())
	}

	refusals := []struct {
		name, id, answer string
	}{
		{"an answer outside the options", "q1", "oracle"},
		{"a later question before the current one", "q2", "redis"},
	}
	for _, tc := range refusals {
		wantSameRefusal(t, tc.name, env.post(fmt.Sprintf(dialogAnswerFmt, "ask-http"), answerBody(tc.id, tc.answer, false)),
			stages.Answer("ask-http", "planning", tc.id, tc.answer, false))
	}
	if _, err := os.Stat(filepath.Join(env.runDir, "ask-http", "planning.q1.answer.json")); !os.IsNotExist(err) {
		t.Fatalf("a refused answer left a file behind: %v", err)
	}

	env.wantOK(t, "answer q1", fmt.Sprintf(dialogAnswerFmt, "ask-http"), answerBody("q1", "postgres", true))
	if err := stages.Answer("ask-engine", "planning", "q1", "postgres", true); err != nil {
		t.Fatalf("engine answer q1: %v", err)
	}
	env.wantOK(t, "answer q2", fmt.Sprintf(dialogAnswerFmt, "ask-http"), answerBody("q2", "redis", false))
	if err := stages.Answer("ask-engine", "planning", "q2", "redis", false); err != nil {
		t.Fatalf("engine answer q2: %v", err)
	}
	env.waitStatus(t, "ask-http", state.StatusAwaitingApproval)
	env.waitStatus(t, "ask-engine", state.StatusAwaitingApproval)

	for _, q := range []string{"q1", "q2"} {
		file := "planning." + q + ".answer.json"
		viaHTTP, errHTTP := os.ReadFile(filepath.Join(env.runDir, "ask-http", file))
		viaEngine, errEngine := os.ReadFile(filepath.Join(env.runDir, "ask-engine", file))
		if errHTTP != nil || errEngine != nil || string(viaHTTP) != string(viaEngine) {
			t.Errorf("%s: HTTP wrote %q (%v), the engine %q (%v)", file, viaHTTP, errHTTP, viaEngine, errEngine)
		}
	}
	wantSameFootprint(t, "answers", env.footprint(t, runsLog, "ask-http"), env.footprint(t, runsLog, "ask-engine"))

	// An answered question cannot be answered again.
	wantSameRefusal(t, "a duplicate answer", env.post(fmt.Sprintf(dialogAnswerFmt, "ask-http"), answerBody("q2", "memcached", false)),
		stages.Answer("ask-http", "planning", "q2", "memcached", false))
}

// While the flow is paused for review, an answer is refused the same way through both doors and writes
// nothing; after the resume it is accepted.
func TestEngineParity_AnswerDuringReviewPause(t *testing.T) {
	env := newParityEnv(t, parityAgent(t, "ask", "dialog_options_agent.sh"))
	env.waitStatus(t, "ask", state.StatusAwaitingUserInput)
	ctx := context.Background()

	if _, err := env.run.Flow().PauseFlow(ctx); err != nil {
		t.Fatalf("PauseFlow: %v", err)
	}
	wantSameRefusal(t, "an answer during the review pause", env.post(fmt.Sprintf(dialogAnswerFmt, "ask"), answerBody("q1", "postgres", true)),
		env.run.Stages().Answer("ask", "planning", "q1", "postgres", true))
	if _, err := os.Stat(filepath.Join(env.runDir, "ask", "planning.q1.answer.json")); !os.IsNotExist(err) {
		t.Fatalf("an answer refused for the review pause left a file behind: %v", err)
	}

	if err := env.run.Flow().CancelNotesAndResume(ctx); err != nil {
		t.Fatalf("CancelNotesAndResume: %v", err)
	}
	// The resume runs in the background: the answer is accepted as soon as it has finished.
	deadline := time.Now().Add(parityDeadline)
	for {
		w := env.post(fmt.Sprintf(dialogAnswerFmt, "ask"), answerBody("q1", "postgres", true))
		if w.Code == http.StatusOK {
			break
		}
		if w.Code != http.StatusConflict || time.Now().After(deadline) {
			t.Fatalf("answer after the resume = %d %q, want 200", w.Code, w.Body.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}
